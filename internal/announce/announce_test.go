package announce

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// oggPage builds one Ogg page carrying the given packets. A packet of 255
// bytes or more is split over several lacing values, as the spec requires.
func oggPage(serial, seq uint32, packets ...[]byte) []byte {
	var segs []byte
	var body []byte
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			segs = append(segs, 255)
			n -= 255
		}
		segs = append(segs, byte(n))
		body = append(body, p...)
	}
	h := make([]byte, 27)
	copy(h, "OggS")
	binary.LittleEndian.PutUint32(h[14:], serial)
	binary.LittleEndian.PutUint32(h[18:], seq)
	h[26] = byte(len(segs))
	out := append(h, segs...)
	return append(out, body...)
}

// celt20 is a CELT fullband 20 ms single-frame packet (config 31, code 0).
func celt20(payload byte, size int) []byte {
	p := make([]byte, size)
	p[0] = 31 << 3
	for i := 1; i < size; i++ {
		p[i] = payload
	}
	return p
}

func opusFile(packets ...[]byte) []byte {
	var buf bytes.Buffer
	buf.Write(oggPage(7, 0, append([]byte("OpusHead"), make([]byte, 11)...)))
	buf.Write(oggPage(7, 1, append([]byte("OpusTags"), make([]byte, 8)...)))
	buf.Write(oggPage(7, 2, packets...))
	return buf.Bytes()
}

func TestDecodeOggOpus(t *testing.T) {
	big := celt20(2, 300) // spans two lacing values
	frames, err := decodeOggOpus(bytes.NewReader(opusFile(celt20(1, 40), big, celt20(3, 10))))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	if !bytes.Equal(frames[1], big) {
		t.Fatalf("multi-segment packet not reassembled")
	}
}

func TestDecodeOggOpusRejectsWrongFrameSize(t *testing.T) {
	p := []byte{30 << 3, 1, 2} // CELT 10 ms
	if _, err := decodeOggOpus(bytes.NewReader(opusFile(p))); err == nil {
		t.Fatal("10 ms packets must be rejected")
	}
}

func TestDecodeOggOpusRejectsNonOgg(t *testing.T) {
	if _, err := decodeOggOpus(bytes.NewReader([]byte("RIFF....WAVEfmt "))); err == nil {
		t.Fatal("a WAV file must be rejected")
	}
}

func TestOpusPacketDuration(t *testing.T) {
	cases := []struct {
		pkt  []byte
		want int
	}{
		{[]byte{1 << 3}, 20000},           // SILK NB 20 ms
		{[]byte{(15 << 3)}, 20000},        // Hybrid FB 20 ms
		{[]byte{(29 << 3) | 1}, 10000},    // CELT 5 ms x2
		{[]byte{(30 << 3) | 3, 2}, 20000}, // CELT 10 ms, code 3, 2 frames
		{[]byte{(3 << 3) | 0}, 60000},     // SILK 60 ms
	}
	for _, c := range cases {
		got, err := opusPacketDurationMicros(c.pkt)
		if err != nil || got != c.want {
			t.Errorf("toc %08b: got %d (%v), want %d", c.pkt[0], got, err, c.want)
		}
	}
}

func TestLoadSkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Regroupement.ogg"), opusFile(celt20(1, 20), celt20(1, 20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.ogg"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lib, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Len() != 1 {
		t.Fatalf("got %d sounds, want 1: %v", lib.Len(), lib.Names())
	}
	s, ok := lib.Get("regroupement")
	if !ok || s.Duration != 40*time.Millisecond {
		t.Fatalf("unexpected sound %+v", s)
	}
}

func TestLoadMissingDir(t *testing.T) {
	lib, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil || lib.Len() != 0 {
		t.Fatalf("missing dir: lib=%v err=%v", lib.Names(), err)
	}
}

func TestRegistryPlayReachesAttachedSlotsOnly(t *testing.T) {
	r := NewRegistry()
	g1, g2 := snowflake.ID(1), snowflake.ID(2)
	a := r.Attach(g1, 10)
	b := r.Attach(g1, 11)
	other := r.Attach(g2, 12)
	b.Release()

	snd := &Sound{Name: "x", Frames: [][]byte{{1}, {2}}, Duration: 40 * time.Millisecond}
	if n := r.Play(g1, snd); n != 1 {
		t.Fatalf("played on %d slots, want 1", n)
	}
	select {
	case <-a.Wake():
	default:
		t.Fatal("wake signal missing")
	}
	f1, ok1 := a.Next()
	f2, ok2 := a.Next()
	_, ok3 := a.Next()
	if !ok1 || !ok2 || ok3 || f1[0] != 1 || f2[0] != 2 {
		t.Fatalf("unexpected sequence %v %v %v", f1, f2, ok3)
	}
	if _, ok := other.Next(); ok {
		t.Fatal("other guild must not play")
	}
	// The copy handed to a slot must not alias the library's packets.
	snd.Frames[0][0] = 9
	r.Play(g1, snd)
	snd.Frames[0][0] = 1
	if f, _ := a.Next(); f[0] != 9 {
		t.Fatalf("expected the queued copy, got %v", f)
	}
}

func TestRegistryReattachKeepsSlot(t *testing.T) {
	r := NewRegistry()
	oldH := r.Attach(1, 10)
	newH := r.Attach(1, 10) // reconnect builds a new provider first
	oldH.Release()
	oldH.Release() // idempotent
	if n := r.Play(1, &Sound{Frames: [][]byte{{1}}, Duration: 20 * time.Millisecond}); n != 1 {
		t.Fatalf("slot lost after reconnect, played on %d", n)
	}
	if _, ok := oldH.Next(); ok {
		t.Fatal("a replaced provider must not steal the announcement")
	}
	if _, ok := newH.Next(); !ok {
		t.Fatal("new provider must receive the announcement")
	}
	newH.Release()
	if n := r.Play(1, &Sound{Frames: [][]byte{{1}}, Duration: 20 * time.Millisecond}); n != 0 {
		t.Fatalf("released slot still targeted (%d)", n)
	}
}

func TestSlotExpiry(t *testing.T) {
	r := NewRegistry()
	h := r.Attach(1, 10)
	h.slot.enqueue([][]byte{{1}}, time.Now().Add(-time.Second))
	if _, ok := h.Next(); ok {
		t.Fatal("expired announcement must not play")
	}
}

func TestRegistryStaleHandleBeforeRelease(t *testing.T) {
	r := NewRegistry()
	stale := r.Attach(1, 10)
	cur := r.Attach(1, 10) // stale provider never closed (reconnect)
	r.Play(1, &Sound{Frames: [][]byte{{1}, {2}}, Duration: 40 * time.Millisecond})
	select {
	case <-stale.Wake():
		t.Fatal("stale provider must not be woken")
	default:
	}
	if _, ok := stale.Next(); ok {
		t.Fatal("stale provider must not play")
	}
	if f, ok := cur.Next(); !ok || f[0] != 1 {
		t.Fatalf("current provider got %v %v", f, ok)
	}
}

func TestRegistryResetGuild(t *testing.T) {
	r := NewRegistry()
	h := r.Attach(1, 10)
	other := r.Attach(2, 10)
	r.ResetGuild(1)
	snd := &Sound{Frames: [][]byte{{1}}, Duration: 20 * time.Millisecond}
	if n := r.Play(1, snd); n != 0 {
		t.Fatalf("reset guild still has %d targets", n)
	}
	if _, ok := h.Next(); ok {
		t.Fatal("reset handle must not play")
	}
	h.Release() // must not panic nor touch other guilds
	if n := r.Play(2, snd); n != 1 {
		t.Fatalf("other guild lost its slot (%d)", n)
	}
	_ = other
}

func TestAttachWakesWhenAnnouncementPending(t *testing.T) {
	r := NewRegistry()
	oldH := r.Attach(1, 10)
	r.Play(1, &Sound{Frames: [][]byte{{1}}, Duration: 20 * time.Millisecond})
	newH := r.Attach(1, 10)
	select {
	case <-newH.Wake():
	default:
		t.Fatal("new provider must be woken for the pending announcement")
	}
	_ = oldH
}
