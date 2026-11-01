package opus

import (
	"sync"
	"testing"
	"time"

	"github.com/TepqeT/voice-relay/internal/telemetry"
)

type fakeOverlay struct {
	mu       sync.Mutex
	frames   [][]byte
	wake     chan struct{}
	released int
}

func newFakeOverlay() *fakeOverlay { return &fakeOverlay{wake: make(chan struct{}, 1)} }

func (f *fakeOverlay) queue(frames ...[]byte) {
	f.mu.Lock()
	f.frames = append(f.frames, frames...)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeOverlay) Next() ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return nil, false
	}
	fr := f.frames[0]
	f.frames = f.frames[1:]
	return fr, true
}

func (f *fakeOverlay) Wake() <-chan struct{} { return f.wake }

func (f *fakeOverlay) Release() {
	f.mu.Lock()
	f.released++
	f.mu.Unlock()
}

func TestProviderOverlayWakesIdleProvider(t *testing.T) {
	ch := make(chan []byte, AudioChanBuf)
	p := NewVoiceProvider(ch, telemetry.OpusRecorder{})
	ov := newFakeOverlay()
	p.SetOverlay(ov)

	got := make(chan []byte, 1)
	go func() {
		f, err := p.ProvideOpusFrame()
		if err == nil {
			got <- f
		}
	}()
	time.Sleep(20 * time.Millisecond) // provider is now blocked on an idle relay
	ov.queue([]byte{0xAA})
	select {
	case f := <-got:
		if f[0] != 0xAA {
			t.Fatalf("got %v, want the announcement", f)
		}
	case <-time.After(time.Second):
		t.Fatal("announcement did not wake the idle provider")
	}
	p.Close()
}

func TestProviderOverlayTakesPrecedenceAndDrainsRelay(t *testing.T) {
	ch := make(chan []byte, AudioChanBuf)
	p := NewVoiceProvider(ch, telemetry.OpusRecorder{})
	ov := newFakeOverlay()
	p.SetOverlay(ov)
	ov.queue([]byte{1}, []byte{2})
	ch <- []byte{10}
	ch <- []byte{11}
	ch <- []byte{12}

	for _, want := range []byte{1, 2} {
		f, err := p.ProvideOpusFrame()
		if err != nil || f[0] != want {
			t.Fatalf("got %v %v, want announcement %d", f, err, want)
		}
	}
	// Two relay frames were discarded while the announcement played.
	f, err := p.ProvideOpusFrame()
	if err != nil || f[0] != 12 {
		t.Fatalf("got %v %v, want the newest relay frame", f, err)
	}
	p.Close()
	p.Close()
	if ov.released != 1 {
		t.Fatalf("overlay released %d times, want 1", ov.released)
	}
}
