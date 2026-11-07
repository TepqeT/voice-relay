// Package stats counts what happens on the site, per day: page views,
// sign-ins, installations, donations. Only numbers are kept (data/stats.json),
// never who did what: the distinct visitors of a day are told apart by a hash
// of their address with a salt that lives in memory and changes every day.
package stats

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/TepqeT/voice-relay/internal/statefile"
)

// Visitors is the event counted once per distinct visitor and day.
const Visitors = "visitors"

// Keep is how many days of counts are kept.
const Keep = 400

// Store keeps the daily counters.
type Store struct {
	file string
	Now  func() time.Time // clock, for tests

	mu      sync.Mutex
	days    map[string]map[string]int // "2006-01-02" -> event -> count
	salt    []byte
	seenDay string
	seen    map[string]bool // visitor hashes seen on seenDay
}

// Open loads the counters kept in file ("" keeps them in memory).
func Open(file string) *Store {
	s := &Store{file: file, Now: time.Now, days: map[string]map[string]int{}, seen: map[string]bool{}}
	if file != "" {
		if b, err := statefile.ReadFile(file); err == nil {
			_ = json.Unmarshal(b, &s.days)
		}
	}
	return s
}

// Count adds one to event today.
func (s *Store) Count(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add(s.day(), event)
	s.save()
}

// Visit adds one to event today and counts visitor (an address) once per
// day under Visitors.
func (s *Store) Visit(event, visitor string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := s.day()
	s.add(day, event)
	if visitor != "" {
		if s.seenDay != day {
			s.seenDay, s.seen, s.salt = day, map[string]bool{}, nil
		}
		if s.salt == nil {
			s.salt = make([]byte, 16)
			if _, err := rand.Read(s.salt); err != nil {
				s.salt = []byte(day)
			}
		}
		sum := sha256.Sum256(append(append([]byte{}, s.salt...), visitor...))
		h := hex.EncodeToString(sum[:8])
		if !s.seen[h] {
			s.seen[h] = true
			s.add(day, Visitors)
		}
	}
	s.save()
}

// Totals is a count of event over the last day, 7 days, 30 days and all
// the days kept.
type Totals struct {
	Today, Week, Month, All int
}

// Totals sums event over the periods ending today.
func (s *Store) Totals(event string) Totals {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	var t Totals
	for day, events := range s.days {
		n := events[event]
		if n == 0 {
			continue
		}
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			continue
		}
		age := int(now.Sub(d).Hours() / 24)
		t.All += n
		if age < 30 {
			t.Month += n
		}
		if age < 7 {
			t.Week += n
		}
		if day == now.Format("2006-01-02") {
			t.Today += n
		}
	}
	return t
}

func (s *Store) day() string { return s.Now().Format("2006-01-02") }

func (s *Store) add(day, event string) {
	s.prune()
	m := s.days[day]
	if m == nil {
		m = map[string]int{}
		s.days[day] = m
	}
	m[event]++
}

// prune forgets the days older than Keep.
func (s *Store) prune() {
	limit := s.Now().AddDate(0, 0, -Keep).Format("2006-01-02")
	for day := range s.days {
		if day < limit {
			delete(s.days, day)
		}
	}
}

func (s *Store) save() {
	if s.file == "" {
		return
	}
	b, err := json.Marshal(s.days)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(s.file), statefile.DirPerm); err == nil {
			err = os.WriteFile(s.file, b, 0o600)
		}
	}
	if err != nil {
		slog.Warn("stats: not saved", slog.String("file", s.file), slog.Any("err", err))
	}
}
