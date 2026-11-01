package announce

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/disgoorg/snowflake/v2"
)

// MaxSounds is the number of sounds offered by the /announce command. Discord
// accepts at most 25 choices on a string option.
const MaxSounds = 25

// maxNameLength is Discord's limit on a command choice name and value.
const maxNameLength = 100

// maxSoundDuration caps one announcement. Packets are sent instead of the
// live relay while it plays, so a long file would mute the raid lead.
const maxSoundDuration = 15 * time.Second

// expiryMargin keeps a queued announcement alive a little longer than its
// audio, so a speaker whose sender ticks late still plays it to the end.
const expiryMargin = 2 * time.Second

// Sound is one announcement, held in memory as ready-to-send Opus packets.
type Sound struct {
	Name     string
	Frames   [][]byte
	Duration time.Duration
}

// Library is the set of sounds loaded from the sounds directory.
type Library struct {
	sounds map[string]*Sound
	names  []string
}

// Load reads every *.ogg and *.opus file of dir. A file that cannot be used is
// logged and skipped, so one bad recording never disables the others. A
// missing directory yields an empty library, not an error.
func Load(dir string) (*Library, error) {
	lib := &Library{sounds: make(map[string]*Sound)}
	if dir == "" {
		return lib, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("announce: sounds directory not found, announcements disabled", slog.String("dir", dir))
			return lib, nil
		}
		return nil, fmt.Errorf("read sounds directory %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".ogg" && ext != ".opus" {
			continue
		}
		name := normalizeName(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		if name == "" {
			continue
		}
		if utf8.RuneCountInString(name) > maxNameLength {
			// Discord rejects the whole command sync for a choice over 100
			// characters, which would take every slash command down.
			slog.Warn("announce: sound name too long, file ignored", slog.String("file", e.Name()), slog.Int("max", maxNameLength))
			continue
		}
		if _, dup := lib.sounds[name]; dup {
			slog.Warn("announce: duplicate sound name, file ignored", slog.String("file", e.Name()))
			continue
		}
		if len(lib.sounds) >= MaxSounds {
			slog.Warn("announce: too many sounds, file ignored", slog.String("file", e.Name()), slog.Int("max", MaxSounds))
			continue
		}
		s, err := loadFile(filepath.Join(dir, e.Name()), name)
		if err != nil {
			slog.Warn("announce: sound rejected", slog.String("file", e.Name()), slog.Any("err", err))
			continue
		}
		lib.sounds[name] = s
		lib.names = append(lib.names, name)
		slog.Info("announce: sound loaded", slog.String("name", name), slog.Duration("duration", s.Duration))
	}
	sort.Strings(lib.names)
	return lib, nil
}

func loadFile(path, name string) (*Sound, error) {
	f, err := os.Open(path) // #nosec G304 -- path comes from the operator's sounds directory
	if err != nil {
		return nil, err
	}
	defer f.Close()
	frames, err := decodeOggOpus(f)
	if err != nil {
		return nil, err
	}
	d := time.Duration(len(frames)) * 20 * time.Millisecond
	if d > maxSoundDuration {
		return nil, fmt.Errorf("sound lasts %s, maximum is %s", d, maxSoundDuration)
	}
	return &Sound{Name: name, Frames: frames, Duration: d}, nil
}

// normalizeName turns a file name into a command choice value: lower case,
// spaces replaced by dashes.
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// Get returns the sound called name.
func (l *Library) Get(name string) (*Sound, bool) {
	if l == nil {
		return nil, false
	}
	s, ok := l.sounds[normalizeName(name)]
	return s, ok
}

// Names lists the loaded sounds in alphabetical order.
func (l *Library) Names() []string {
	if l == nil {
		return nil
	}
	return append([]string(nil), l.names...)
}

// Len is the number of loaded sounds.
func (l *Library) Len() int {
	if l == nil {
		return 0
	}
	return len(l.names)
}

type slotKey struct {
	guildID snowflake.ID
	botID   snowflake.ID
}

// Registry tracks the playback slot of every bot voice connection, so an
// announcement reaches every speaker of a guild at once.
type Registry struct {
	mu    sync.Mutex
	slots map[slotKey]*Slot
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{slots: make(map[slotKey]*Slot)}
}

// Attach makes a new provider the current player of the slot of one bot in
// one guild and returns its handle. Only the most recent handle plays: a
// reconnect builds a new provider while the old one may still be blocked, and
// the old one must not steal the announcement. A pending announcement survives
// the swap. Release the handle when its provider closes.
func (r *Registry) Attach(guildID, botID snowflake.ID) *Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := slotKey{guildID: guildID, botID: botID}
	s, ok := r.slots[k]
	if !ok {
		s = &Slot{registry: r, key: k}
		r.slots[k] = s
	}
	h := &Handle{slot: s, wake: make(chan struct{}, 1)}
	s.mu.Lock()
	s.current = h
	pending := s.next < len(s.frames)
	s.mu.Unlock()
	if pending {
		h.signal()
	}
	return h
}

// release detaches h. The slot is dropped once no current handle remains.
func (r *Registry) release(h *Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := h.slot
	s.mu.Lock()
	if s.current == h {
		s.current = nil
		s.frames = nil
		s.next = 0
	}
	idle := s.current == nil
	s.mu.Unlock()
	if idle && r.slots[s.key] == s {
		delete(r.slots, s.key)
	}
}

// ResetGuild forgets every slot of guildID. Called when a raid ends, since
// providers rebuilt by reconnects are not always closed explicitly.
func (r *Registry) ResetGuild(guildID snowflake.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, s := range r.slots {
		if k.guildID != guildID {
			continue
		}
		s.mu.Lock()
		s.current = nil
		s.frames = nil
		s.next = 0
		s.mu.Unlock()
		delete(r.slots, k)
	}
}

// Play queues sound on every slot of guildID that has a current provider and
// returns how many bots will play it. Each slot gets its own copy of the
// packets because the voice sender may encrypt them in place.
func (r *Registry) Play(guildID snowflake.ID, sound *Sound) int {
	if sound == nil {
		return 0
	}
	r.mu.Lock()
	targets := make([]*Slot, 0, len(r.slots))
	for k, s := range r.slots {
		if k.guildID != guildID {
			continue
		}
		s.mu.Lock()
		active := s.current != nil
		s.mu.Unlock()
		if active {
			targets = append(targets, s)
		}
	}
	r.mu.Unlock()
	expires := time.Now().Add(sound.Duration + expiryMargin)
	n := 0
	for _, s := range targets {
		if s.enqueue(sound.Frames, expires) {
			n++
		}
	}
	return n
}

// Slot is the announcement queue of one bot voice connection.
type Slot struct {
	mu       sync.Mutex
	frames   [][]byte
	next     int
	expires  time.Time
	current  *Handle
	registry *Registry
	key      slotKey
}

// enqueue replaces the pending announcement and wakes the current provider.
// It reports false when the slot lost its provider in the meantime.
func (s *Slot) enqueue(frames [][]byte, expires time.Time) bool {
	cp := make([][]byte, len(frames))
	for i, f := range frames {
		cp[i] = append([]byte(nil), f...)
	}
	s.mu.Lock()
	h := s.current
	if h != nil {
		s.frames = cp
		s.next = 0
		s.expires = expires
	}
	s.mu.Unlock()
	if h == nil {
		return false
	}
	h.signal()
	return true
}

// nextFor returns the next packet for h, if h is the current provider.
func (s *Slot) nextFor(h *Handle) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != h || s.next >= len(s.frames) {
		return nil, false
	}
	if time.Now().After(s.expires) {
		s.frames = nil
		s.next = 0
		return nil, false
	}
	f := s.frames[s.next]
	s.next++
	if s.next >= len(s.frames) {
		s.frames = nil
		s.next = 0
	}
	return f, true
}

// Handle is one provider's attachment to a slot. It implements the overlay
// consumed by opus.VoiceProvider.
type Handle struct {
	slot *Slot
	wake chan struct{}
	once sync.Once
}

func (h *Handle) signal() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Next returns the next announcement packet, if one is due for this provider.
func (h *Handle) Next() ([]byte, bool) { return h.slot.nextFor(h) }

// Wake fires when an announcement is queued, so a provider blocked on an idle
// relay channel starts playing right away.
func (h *Handle) Wake() <-chan struct{} { return h.wake }

// Release hands the attachment back to the registry. Safe to call more than once.
func (h *Handle) Release() {
	h.once.Do(func() { h.slot.registry.release(h) })
}
