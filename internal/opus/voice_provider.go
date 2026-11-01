package opus

import (
	"fmt"
	"sync"
	"time"

	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/telemetry"
	"github.com/disgoorg/disgo/voice"
)

// VoiceProvider streams Opus frames from a channel into a voice connection.
type VoiceProvider struct {
	voice.OpusFrameProvider
	ch      <-chan []byte
	done    chan struct{}
	metrics telemetry.OpusRecorder // zero-value is safe (no-op); drop callback set via OpusRecorder.WithDrop
	// prev holds the buffer returned by the last ProvideOpusFrame call.
	// It is recycled via PutEncodedFrame at the start of the next call —
	// by that point disgo has finished sending the packet over UDP and no
	// longer holds a reference to the slice.
	prev []byte
	// overlay, when set, supplies announcement packets that take precedence
	// over the relayed audio while they last (see SetOverlay).
	overlay   Overlay
	closeOnce sync.Once
	// out, when set, records playout regularity for the raid diagnostics.
	out *diag.OutputStats
}

// SetDiag attaches the playout statistics of this bot. Call before the
// provider is handed to the voice connection.
func (v *VoiceProvider) SetDiag(o *diag.OutputStats) { v.out = o }

// Overlay is a source of short announcements played instead of the relayed
// audio. announce.Handle implements it.
type Overlay interface {
	// Next returns the next announcement packet, if one is due.
	Next() ([]byte, bool)
	// Wake fires when an announcement is queued.
	Wake() <-chan struct{}
	// Release detaches the overlay once the provider is closed.
	Release()
}

// SetOverlay attaches an announcement source. Call before the provider is
// handed to the voice connection; the overlay is released on Close.
func (v *VoiceProvider) SetOverlay(o Overlay) { v.overlay = o }

func NewVoiceProvider(ch <-chan []byte, metrics telemetry.OpusRecorder) *VoiceProvider {
	return &VoiceProvider{
		ch:      ch,
		done:    make(chan struct{}),
		metrics: metrics,
	}
}

// providerDrainThreshold is the maximum number of queued frames (beyond the
// one just read) before the provider drains excess frames. Normal timing
// jitter between the mixer tick and the disgo sender produces 0–2 queued
// frames; anything above the threshold indicates a stall that accumulated
// latency beyond an acceptable window.
// 3 frames × 20 ms = 60 ms of tolerated jitter before drain kicks in.
const providerDrainThreshold = 3

// AudioChanBuf is the buffer size for the single-producer/single-consumer
// Opus channels feeding VoiceProvider (chOut, drained by the disgo sender
// goroutine) and the relay bridge input (relayOpusIn, drained by the bridge
// goroutine). Matched to providerDrainThreshold so the bleed-off path engages
// exactly when the buffer fills — 3 frames × 20 ms = 60 ms.
const AudioChanBuf = providerDrainThreshold

// providerHardDrainThreshold is twice the soft threshold. Beyond this point
// the buffer is clearly recovering from a stall (not normal jitter), and the
// gentle 1-frame-per-call drain would take many calls to catch up while
// listeners hear accumulating latency. Above this threshold we drain
// (depth - soft threshold) frames in one go, accepting one audible gap
// instead of seconds of compounding lag.
const providerHardDrainThreshold = 2 * providerDrainThreshold

func (v *VoiceProvider) ProvideOpusFrame() ([]byte, error) {
	// Return the previous frame's buffer to the pool before blocking.
	// disgo calls ProvideOpusFrame only after finishing the UDP send for the
	// previous packet, so v.prev is no longer referenced at this point.
	// PutEncodedFrame is a no-op for passthrough slices (wrong cap), so this
	// is safe regardless of whether the frame came from the pool or the receiver.
	PutEncodedFrame(v.prev)
	v.prev = nil

	var wake <-chan struct{}
	if v.overlay != nil {
		wake = v.overlay.Wake()
	}
	for {
		if v.overlay != nil {
			if frame, ok := v.overlay.Next(); ok {
				// The announcement replaces the live relay while it plays.
				// Discard one queued relay frame per announcement frame so
				// the relay buffer does not pile up latency meanwhile; this
				// is intentional, so it is not counted as a drop.
				select {
				case stale, ok := <-v.ch:
					if ok {
						PutEncodedFrame(stale)
					}
				default:
				}
				// v.prev stays nil: announcement packets do not come from
				// the encoded-frame pool and must not be returned to it.
				v.out.Frame(true, 0, 0)
				return frame, nil
			}
		}
		select {
		case <-v.done:
			return nil, fmt.Errorf("voice provider is closed")
		case <-wake:
			continue
		case data, ok := <-v.ch:
			if !ok {
				return nil, fmt.Errorf("voice provider channel closed")
			}
			start := time.Now()
			dropped := 0
			// Bleed-off: when the buffer depth exceeds the soft threshold, drop
			// one extra frame per call (gradual catch-up; speech is not cut
			// mid-word). When depth crosses the HARD threshold, the buffer is
			// recovering from a real stall — drain everything beyond the soft
			// threshold in a single call rather than spending many ticks
			// chipping away while latency compounds.
			depth := len(v.ch)
			if depth > providerHardDrainThreshold {
				drop := depth - providerDrainThreshold
			hardDrain:
				for range drop {
					select {
					case newer, ok := <-v.ch:
						if !ok {
							break hardDrain
						}
						PutEncodedFrame(data)
						v.metrics.RecordDrop()
						dropped++
						data = newer
					default:
						break hardDrain
					}
				}
			} else if depth > providerDrainThreshold {
				select {
				case newer, ok := <-v.ch:
					if ok {
						PutEncodedFrame(data)
						v.metrics.RecordDrop()
						dropped++
						data = newer
					}
				default:
				}
			}
			v.prev = data
			v.metrics.RecordProvide(float64(time.Since(start).Microseconds()) / 1000.0)
			v.out.Frame(false, len(v.ch), dropped)
			return data, nil
		}
	}
}

// Close stops the provider. Safe to call more than once and concurrently:
// session cleanup and a reconnect may both close the same provider.
func (v *VoiceProvider) Close() {
	v.closeOnce.Do(func() {
		close(v.done)
		if v.overlay != nil {
			v.overlay.Release()
		}
	})
}

// EmptyVoiceProvider is a no-op OpusFrameProvider that never sends audio.
// ProvideOpusFrame blocks until Close is called, at which point it returns an
// error on that and every subsequent call.
//
// Closing the provider does NOT stop disgo's audio sender: its loop logs a
// provider error and keeps ticking every 20 ms, so a sender that outlives
// teardown turns into a 50 lines/second ERROR loop. Stopping the sender is
// pool.AudioSenderRegistry's job, driven from pool.GuildVoice.Leave.
type EmptyVoiceProvider struct {
	voice.OpusFrameProvider
	done chan struct{}
}

func NewEmptyVoiceProvider() *EmptyVoiceProvider {
	return &EmptyVoiceProvider{
		done: make(chan struct{}),
	}
}

func (v *EmptyVoiceProvider) ProvideOpusFrame() ([]byte, error) {
	<-v.done
	return nil, fmt.Errorf("empty voice provider closed")
}

func (v *EmptyVoiceProvider) Close() {
	select {
	case <-v.done:
	default:
		close(v.done)
	}
}
