package manager

import (
	"context"
	"fmt"

	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/opus"
	"github.com/TepqeT/voice-relay/internal/pool"
	"github.com/TepqeT/voice-relay/internal/telemetry"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// VoiceConnSetup is a builder that configures a voice connection's provider
// and receiver pair. Use the With* methods to select the desired mode, then
// call Apply to wire everything into a voice.Conn.
type VoiceConnSetup struct {
	userID     snowflake.ID
	diag       *diag.Guild
	overlayFn  func() opus.Overlay
	providerFn func(chOut <-chan []byte) (voice.OpusFrameProvider, error)
	receiverFn func() (voice.OpusFrameReceiver, *opus.FanoutHandle, error)
}

// NewVoiceConnSetup creates a new voice session builder.
func NewVoiceConnSetup(userID snowflake.ID) *VoiceConnSetup {
	return &VoiceConnSetup{userID: userID}
}

// WithVoiceProvider reads opus frames from chOut and plays them.
// metrics carries both the histogram recorder and (optionally) the drop callback —
// build it via GuildMetrics.Provider() to wire both in one shot.
func (v *VoiceConnSetup) WithVoiceProvider(metrics telemetry.OpusRecorder) *VoiceConnSetup {
	v.providerFn = func(chOut <-chan []byte) (voice.OpusFrameProvider, error) {
		p := opus.NewVoiceProvider(chOut, metrics)
		p.SetDiag(v.diag.Output(v.userID))
		if v.overlayFn != nil {
			if o := v.overlayFn(); o != nil {
				p.SetOverlay(o)
			}
		}
		return p, nil
	}
	return v
}

// WithDiag records this connection in the guild's raid diagnostics.
func (v *VoiceConnSetup) WithDiag(g *diag.Guild) *VoiceConnSetup {
	v.diag = g
	return v
}

// WithAnnouncements lets the provider play announcements (see
// internal/announce). fn is called once per provider built; a nil fn or a nil
// overlay disables announcements for this connection.
func (v *VoiceConnSetup) WithAnnouncements(fn func() opus.Overlay) *VoiceConnSetup {
	v.overlayFn = fn
	return v
}

// WithVoiceReceiver captures incoming voice frames filtered by allowUser.
// metrics carries both the histogram recorder and (optionally) the drop callback —
// build it via GuildMetrics.Receiver() to wire both in one shot.
//
// A FanoutHandle is created and attached to the receiver so the wiring code
// can later call handle.Install with the topology-specific targets. Frames
// received before Install are silently dropped (brief pre-install window).
func (v *VoiceConnSetup) WithVoiceReceiver(allowUser func(snowflake.ID) bool, metrics telemetry.OpusRecorder) *VoiceConnSetup {
	v.receiverFn = func() (voice.OpusFrameReceiver, *opus.FanoutHandle, error) {
		handle := opus.NewFanoutHandle()
		r := opus.NewVoiceReceiver(v.userID, allowUser, metrics, handle)
		r.SetDiag(v.diag)
		return r, handle, nil
	}
	return v
}

// Apply configures the voice connection with the session's provider and receiver,
// sets the speaking flag, and returns the FanoutHandle (nil when no receiver is
// configured) and a cleanup function.
//
// chOut is the playback source consumed by the provider; pass nil when no
// provider is configured. The handle must be passed to the wiring code so it
// can call handle.Install once the topology is built. The cleanup function
// calls handle.Close() so session-end teardown fires the install-time OnClose
// hook (e.g. RemoveInput).
//
// ctx must carry a deadline or timeout: SetSpeaking sends a gateway op and will
// block until Discord acknowledges or the context is cancelled.
func (v *VoiceConnSetup) Apply(ctx context.Context, conn voice.Conn, chOut <-chan []byte) (*opus.FanoutHandle, func(), error) {
	var provider voice.OpusFrameProvider
	if v.providerFn == nil {
		provider = opus.NewEmptyVoiceProvider()
	} else {
		p, err := v.providerFn(chOut)
		if err != nil {
			return nil, nil, fmt.Errorf("create voice provider: %w", err)
		}
		provider = p
	}

	var receiver voice.OpusFrameReceiver
	var handle *opus.FanoutHandle
	if v.receiverFn == nil {
		receiver = opus.NewEmptyVoiceReceiver()
	} else {
		r, h, err := v.receiverFn()
		if err != nil {
			provider.Close()
			return nil, nil, fmt.Errorf("create voice receiver: %w", err)
		}
		receiver = r
		handle = h
	}

	cleanup := func() {
		provider.Close()
		receiver.Close()
		if handle != nil {
			handle.Close()
		}
	}

	conn.SetOpusFrameProvider(provider)
	conn.SetOpusFrameReceiver(receiver)

	if err := conn.SetSpeaking(ctx, pool.SpeakingFlags()); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("set speaking flag: %w", err)
	}

	return handle, cleanup, nil
}
