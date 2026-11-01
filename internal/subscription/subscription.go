// Package subscription records which Discord servers may use the relay. A
// server is identified by its ID only: administrators sign in with Discord,
// there is no separate account. Subscriptions are kept in a JSON file next to
// the store until the service moves to a database.
package subscription

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// Plan is how long a subscription lasts.
type Plan string

const (
	// PlanLifetime never expires.
	PlanLifetime Plan = "lifetime"
	// PlanUntil expires at Expires.
	PlanUntil Plan = "until"
)

// Subscription is the right of one server to use the relay.
type Subscription struct {
	GuildID   snowflake.ID `json:"guild_id"`
	GuildName string       `json:"guild_name,omitempty"`
	Plan      Plan         `json:"plan"`
	Expires   time.Time    `json:"expires,omitzero"` // PlanUntil only
	Gift      bool         `json:"gift"`             // offered by an administrator, nothing to pay
	Note      string       `json:"note,omitempty"`
	GrantedBy snowflake.ID `json:"granted_by,omitempty"`
	GrantedAt time.Time    `json:"granted_at"`
}

// Active reports whether the subscription covers now.
func (s Subscription) Active(now time.Time) bool {
	switch s.Plan {
	case PlanLifetime:
		return true
	case PlanUntil:
		return now.Before(s.Expires)
	}
	return false
}

// Label describes the subscription in French, dates as jj/mm/aaaa.
func (s Subscription) Label(now time.Time) string {
	gift := ""
	if s.Gift {
		gift = " (offert)"
	}
	switch {
	case s.Plan == PlanLifetime:
		return "À vie" + gift
	case s.Active(now):
		// Expires is exclusive: show the last day still covered.
		return "Jusqu'au " + s.Expires.Add(-time.Nanosecond).Format("02/01/2006") + gift
	default:
		return "Expiré le " + s.Expires.Format("02/01/2006")
	}
}

var (
	// ErrInvalid is returned for a subscription that cannot be stored.
	ErrInvalid = errors.New("invalid subscription")
	// ErrNotFound is returned when revoking a server without subscription.
	ErrNotFound = errors.New("no subscription for this server")
)

// Store keeps the subscriptions, saved to a JSON file on every change.
type Store struct {
	path string
	mu   sync.RWMutex
	m    map[snowflake.ID]Subscription
	now  func() time.Time
}

// Open loads path, or starts empty when it does not exist yet. An empty path
// keeps everything in memory (tests).
func Open(path string) (*Store, error) {
	s := &Store{path: path, m: make(map[snowflake.ID]Subscription), now: time.Now}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- operator-configured data file
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Subscription
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, sub := range list {
		s.m[sub.GuildID] = sub
	}
	return s, nil
}

// Get returns the subscription of guildID.
func (s *Store) Get(guildID snowflake.ID) (Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.m[guildID]
	return sub, ok
}

// Active reports whether guildID has a subscription covering now.
func (s *Store) Active(guildID snowflake.ID) bool {
	sub, ok := s.Get(guildID)
	return ok && sub.Active(s.now())
}

// List returns every subscription, by server name.
func (s *Store) List() []Subscription {
	s.mu.RLock()
	out := make([]Subscription, 0, len(s.m))
	for _, sub := range s.m {
		out = append(out, sub)
	}
	s.mu.RUnlock()
	slices.SortFunc(out, func(a, b Subscription) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.GuildName), strings.ToLower(b.GuildName)), cmp.Compare(a.GuildID, b.GuildID))
	})
	return out
}

// Grant creates or replaces the subscription of sub.GuildID.
func (s *Store) Grant(sub Subscription) error {
	if sub.GuildID == 0 {
		return fmt.Errorf("%w: missing server ID", ErrInvalid)
	}
	switch sub.Plan {
	case PlanLifetime:
		sub.Expires = time.Time{}
	case PlanUntil:
		if !sub.Expires.After(s.now()) {
			return fmt.Errorf("%w: the end date must be in the future", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown plan %q", ErrInvalid, sub.Plan)
	}
	if sub.GrantedAt.IsZero() {
		sub.GrantedAt = s.now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.m[sub.GuildID]
	s.m[sub.GuildID] = sub
	if err := s.saveLocked(); err != nil {
		if had {
			s.m[sub.GuildID] = prev
		} else {
			delete(s.m, sub.GuildID)
		}
		return err
	}
	return nil
}

// Revoke removes the subscription of guildID.
func (s *Store) Revoke(guildID snowflake.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.m[guildID]
	if !ok {
		return ErrNotFound
	}
	delete(s.m, guildID)
	if err := s.saveLocked(); err != nil {
		s.m[guildID] = prev
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]Subscription, 0, len(s.m))
	for _, sub := range s.m {
		list = append(list, sub)
	}
	slices.SortFunc(list, func(a, b Subscription) int { return cmp.Compare(a.GuildID, b.GuildID) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
