package opus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TepqeT/voice-relay/internal/telemetry"
)

// A frame that lands between two grid ticks must leave at once instead of
// waiting up to 20 ms for the next tick.
func TestMixerForwardsWithoutWaitingForTheGrid(t *testing.T) {
	m, err := NewMixer(telemetry.OpusRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out []time.Time
	m.SetSink(func(pkt []byte) {
		mu.Lock()
		out = append(out, time.Now())
		mu.Unlock()
		PutEncodedFrame(pkt)
	})
	src := NewSourceBuffer(nil)
	if err := m.AddInput(newID(), src); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	frames := generateTestFrames(t, 440, 1, testAmplitude)
	time.Sleep(27 * time.Millisecond) // land off the 20 ms grid
	var worst time.Duration
	for i := range 10 {
		sent := time.Now()
		pcm := GetPCM()
		copy(pcm, frames[0].pcm)
		src.Feed(Frame{PCM: pcm, Opus: append([]byte(nil), frames[0].opus...), CreatedAt: sent})
		deadline := time.Now().Add(time.Second)
		for {
			mu.Lock()
			n := len(out)
			var at time.Time
			if n > i {
				at = out[i]
			}
			mu.Unlock()
			if n > i {
				if d := at.Sub(sent); d > worst {
					worst = d
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("frame %d never left the mixer", i)
			}
			time.Sleep(200 * time.Microsecond)
		}
		time.Sleep(20*time.Millisecond - time.Since(sent))
	}
	// The grid alone would add 0 to 20 ms (10 ms on average); allow for a
	// loaded CI machine but stay well under one frame.
	if worst > 8*time.Millisecond {
		t.Errorf("worst mixer delay %v, want under 8 ms", worst)
	}
}

// An input that keeps one frame queued is behind the live edge: after
// trimAfterTicks ticks the older frame is dropped.
func TestMixerTrimsPersistentBacklog(t *testing.T) {
	m, err := NewMixer(telemetry.OpusRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	m.SetSink(PutEncodedFrame)
	src := NewSourceBuffer(nil)
	if err := m.AddInput(newID(), src); err != nil {
		t.Fatal(err)
	}
	frames := generateTestFrames(t, 440, 1, testAmplitude)
	feed := func() {
		pcm := GetPCM()
		copy(pcm, frames[0].pcm)
		src.Feed(Frame{PCM: pcm, Opus: append([]byte(nil), frames[0].opus...)})
	}
	feed() // one frame of backlog, then one frame per tick
	for range trimAfterTicks {
		feed()
		if err := m.tick(); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.Trimmed(); got != 1 {
		t.Fatalf("trimmed = %d, want 1", got)
	}
	if n := src.Len(); n != 0 {
		t.Fatalf("backlog = %d frames after the trim, want 0", n)
	}
	// Back at the live edge: no further trim.
	for range 2 * trimAfterTicks {
		feed()
		if err := m.tick(); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.Trimmed(); got != 1 {
		t.Fatalf("trimmed = %d at the live edge, want still 1", got)
	}
}

// A short jitter burst is absorbed without dropping anything.
func TestMixerKeepsShortBurst(t *testing.T) {
	m, err := NewMixer(telemetry.OpusRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	m.SetSink(PutEncodedFrame)
	src := NewSourceBuffer(nil)
	_ = m.AddInput(newID(), src)
	frames := generateTestFrames(t, 440, 1, testAmplitude)
	feed := func() {
		pcm := GetPCM()
		copy(pcm, frames[0].pcm)
		src.Feed(Frame{PCM: pcm, Opus: append([]byte(nil), frames[0].opus...)})
	}
	feed()
	feed() // burst of two
	for range trimAfterTicks - 2 {
		_ = m.tick()
		feed()
	}
	_ = m.tick()
	_ = m.tick() // the burst has drained: underrun resets the count
	if got := m.Trimmed(); got != 0 {
		t.Fatalf("trimmed = %d for a short burst, want 0", got)
	}
}

// The provider skips one frame once a frame has stayed queued for
// trimAfterTicks calls, and leaves a short burst alone.
func TestVoiceProviderTrimsPersistentBacklog(t *testing.T) {
	ch := make(chan []byte, AudioChanBuf)
	var drops atomic.Int32
	vp := NewVoiceProvider(ch, telemetry.OpusRecorder{}.WithDrop(func() { drops.Add(1) }))
	next := byte(0)
	push := func() { next++; ch <- []byte{next} }

	push() // one frame of backlog, then one new frame per call
	var last byte
	for range trimAfterTicks {
		push()
		got, err := vp.ProvideOpusFrame()
		if err != nil {
			t.Fatal(err)
		}
		last = got[0]
	}
	if drops.Load() != 1 || len(ch) != 0 || last != next {
		t.Fatalf("drops=%d queued=%d last=%d newest=%d: want one skip back to the live edge", drops.Load(), len(ch), last, next)
	}
	for range 2 * trimAfterTicks {
		push()
		if _, err := vp.ProvideOpusFrame(); err != nil {
			t.Fatal(err)
		}
	}
	if drops.Load() != 1 {
		t.Fatalf("drops=%d at the live edge, want still 1", drops.Load())
	}
}
