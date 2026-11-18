package dave

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// notReadySession is a golibdave session that is not Ready: passthrough mode,
// or encryption on without a key ratchet. Encrypt passes frames through.
type notReadySession struct{ godave.Session }

func (notReadySession) Ready() bool { return false }
func (notReadySession) Encrypt(_ uint32, frame []byte, out []byte) (int, error) {
	return copy(out, frame), nil
}

// readyBackend is an encrypted session.
type readyBackend struct{ godave.Session }

func (readyBackend) Ready() bool { return true }

func instrumented(backend godave.Session) godave.Session {
	inner := func(*slog.Logger, godave.UserID, godave.Callbacks) godave.Session { return backend }
	return Instrument(inner, NewStats())(slog.Default(), "relay-1", nil)
}

// 2026-11-13: five relay bots out of eight never sent a frame for a whole
// session: disgo v0.20.0 skips audio while DAVE is not Ready, and a relay in
// passthrough or without a key ratchet never was. disgo must always be told
// Ready, and the diagnostics the real state.
func TestReadyAlwaysTrueStateTellsTheTruth(t *testing.T) {
	passthrough := instrumented(notReadySession{})
	if !passthrough.Ready() {
		t.Fatal("Ready false: disgo would never send this bot's audio")
	}
	if got := StateOf(passthrough); got != StatePassthrough {
		t.Fatalf("state = %q, want %q", got, StatePassthrough)
	}

	noKey := instrumented(&encryptingSession{err: errMissingKeyRatchet})
	_, _ = noKey.Encrypt(1, []byte("opus"), make([]byte, 64))
	if !noKey.Ready() || StateOf(noKey) != StateNoKey {
		t.Fatalf("no key ratchet: ready=%v state=%q", noKey.Ready(), StateOf(noKey))
	}

	if StateOf(instrumented(readyBackend{})) != StateEncrypted {
		t.Fatal("encrypted session not reported as such")
	}
	if StateOf(readyBackend{}) != "" {
		t.Fatal("a bare session has no state")
	}
}

var errNoFrame = errors.New("no frame")

// countingProvider counts how often the audio sender asks for a frame.
type countingProvider struct{ calls atomic.Int64 }

// ProvideOpusFrame fails so the sender stops before touching the UDP conn.
func (p *countingProvider) ProvideOpusFrame() ([]byte, error) {
	p.calls.Add(1)
	return nil, errNoFrame
}
func (p *countingProvider) Close() {}

// daveConn is a voice.Conn exposing only its DAVE session.
type daveConn struct {
	voice.Conn
	sess godave.Session
}

func (c daveConn) DAVE() godave.Session { return c.sess }

// Tripwire: disgo's audio sender pulls no frame while the session is not
// Ready. When this fails, disgo stopped gating on Ready and the always-true
// Ready above can go.
//
// UPSTREAM-TRIPWIRE(disgo v0.20.0): delete countingSession.Ready.
func TestDisgoSenderStillWaitsForDaveReady(t *testing.T) {
	gated, open := &countingProvider{}, &countingProvider{}
	logger := slog.New(slog.DiscardHandler)
	s1 := voice.NewAudioSender(logger, gated, daveConn{sess: notReadySession{}})
	s2 := voice.NewAudioSender(logger, open, daveConn{sess: instrumented(notReadySession{})})
	// s1 is never closed: disgo's Close races a sender that never pulled a
	// frame (the hazard pool.safeAudioSender guards in production). It
	// only sleeps and returns every 20 ms until the test binary exits.
	s1.Open()
	s2.Open()
	time.Sleep(200 * time.Millisecond)
	if open.calls.Load() > 0 {
		s2.Close() // started: its cancel func is set
	}
	if gated.calls.Load() != 0 {
		t.Errorf("disgo pulled %d frames from a not-ready session: its Ready gate is gone, delete countingSession.Ready", gated.calls.Load())
	}
	if open.calls.Load() == 0 {
		t.Error("the instrumented session must let the sender pull frames")
	}
}

// Ready of the backend left without a key ratchet: encryption is on but not
// usable.
func (s *encryptingSession) Ready() bool { return false }
