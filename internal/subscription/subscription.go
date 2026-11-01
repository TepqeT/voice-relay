// Package subscription records who may use the relay. A subscription belongs
// to a Discord account (people sign in with Discord, there is no separate
// account); its owner activates it on the servers they manage, as many as
// the plan allows. Data is kept in a JSON file next to the store until the
// service moves to a database.
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

// Subscription is the right of one Discord account to run the relay.
type Subscription struct {
	UserID    snowflake.ID `json:"user_id"`
	UserName  string       `json:"user_name,omitempty"`
	Plan      Plan         `json:"plan"`
	Expires   time.Time    `json:"expires,omitzero"` // PlanUntil only, exclusive
	MaxGuilds int          `json:"max_guilds,omitempty"`
	Gift      bool         `json:"gift"` // offered by an administrator
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

// LastDay is the last day covered by a PlanUntil subscription.
func (s Subscription) LastDay() time.Time { return s.Expires.Add(-time.Nanosecond) }

// Link activates a subscription on a server.
type Link struct {
	GuildID   snowflake.ID `json:"guild_id"`
	GuildName string       `json:"guild_name,omitempty"`
	UserID    snowflake.ID `json:"user_id"`
	LinkedAt  time.Time    `json:"linked_at"`
}

// State is what a server's coverage looks like now.
type State int

const (
	// StateNone: no subscription was ever activated on the server.
	StateNone State = iota
	// StateActive: covered.
	StateActive
	// StateExpired: the subscription activated on the server has ended or
	// was removed.
	StateExpired
)

// Coverage tells whether a server may run raids, and why.
type Coverage struct {
	State State
	Link  Link
	Sub   Subscription // zero when the subscription was removed
}

var (
	// ErrInvalid is returned for a subscription that cannot be stored.
	ErrInvalid = errors.New("invalid subscription")
	// ErrNotFound is returned for an unknown account or server.
	ErrNotFound = errors.New("not found")
	// ErrNoSubscription: the account has no active subscription.
	ErrNoSubscription = errors.New("no active subscription")
	// ErrLimit: the plan's server limit is reached.
	ErrLimit = errors.New("server limit reached")
	// ErrTaken: another account's subscription covers the server.
	ErrTaken = errors.New("server covered by another account")
)

type fileData struct {
	Subscriptions []Subscription `json:"subscriptions"`
	Links         []Link         `json:"links"`
}

// Store keeps subscriptions and links, saved on every change.
type Store struct {
	path  string
	mu    sync.RWMutex
	subs  map[snowflake.ID]Subscription // by user
	links map[snowflake.ID]Link         // by guild
	now   func() time.Time
}

// Open loads path, or starts empty when it does not exist. An empty path
// keeps everything in memory (tests).
func Open(path string) (*Store, error) {
	s := &Store{path: path, subs: map[snowflake.ID]Subscription{}, links: map[snowflake.ID]Link{}, now: time.Now}
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
	var f fileData
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, sub := range f.Subscriptions {
		s.subs[sub.UserID] = sub
	}
	for _, l := range f.Links {
		s.links[l.GuildID] = l
	}
	return s, nil
}

// Now is the store's clock.
func (s *Store) Now() time.Time { return s.now() }

// Get returns the subscription of userID.
func (s *Store) Get(userID snowflake.ID) (Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subs[userID]
	return sub, ok
}

// Coverage describes guildID's coverage now.
func (s *Store) Coverage(guildID snowflake.ID) Coverage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.links[guildID]
	if !ok {
		return Coverage{State: StateNone}
	}
	sub, ok := s.subs[l.UserID]
	if ok && sub.Active(s.now()) {
		return Coverage{State: StateActive, Link: l, Sub: sub}
	}
	return Coverage{State: StateExpired, Link: l, Sub: sub}
}

// Active reports whether guildID is covered now.
func (s *Store) Active(guildID snowflake.ID) bool { return s.Coverage(guildID).State == StateActive }

// Guilds lists the servers userID's subscription is activated on.
func (s *Store) Guilds(userID snowflake.ID) []Link {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.guildsLocked(userID)
}

func (s *Store) guildsLocked(userID snowflake.ID) []Link {
	var out []Link
	for _, l := range s.links {
		if l.UserID == userID {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b Link) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.GuildName), strings.ToLower(b.GuildName)), cmp.Compare(a.GuildID, b.GuildID))
	})
	return out
}

// List returns every subscription, by account name.
func (s *Store) List() []Subscription {
	s.mu.RLock()
	out := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	s.mu.RUnlock()
	slices.SortFunc(out, func(a, b Subscription) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.UserName), strings.ToLower(b.UserName)), cmp.Compare(a.UserID, b.UserID))
	})
	return out
}

// Grant creates or replaces the subscription of sub.UserID. Its servers stay
// linked.
func (s *Store) Grant(sub Subscription) error {
	if sub.UserID == 0 {
		return fmt.Errorf("%w: missing account ID", ErrInvalid)
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
	if sub.MaxGuilds < 0 {
		return fmt.Errorf("%w: negative server limit", ErrInvalid)
	}
	if sub.GrantedAt.IsZero() {
		sub.GrantedAt = s.now().UTC()
	}
	return s.mutate(func() { s.subs[sub.UserID] = sub })
}

// Revoke removes userID's subscription. Its servers stay linked and report
// StateExpired, so their members are told why raids stopped.
func (s *Store) Revoke(userID snowflake.ID) error {
	s.mu.RLock()
	_, ok := s.subs[userID]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	return s.mutate(func() { delete(s.subs, userID) })
}

// Activate links guildID to userID's subscription.
func (s *Store) Activate(userID, guildID snowflake.ID, guildName string) error {
	s.mu.RLock()
	sub, ok := s.subs[userID]
	cur, linked := s.links[guildID]
	n := len(s.guildsLocked(userID))
	s.mu.RUnlock()
	switch {
	case !ok || !sub.Active(s.now()):
		return ErrNoSubscription
	case linked && cur.UserID == userID:
		return nil
	case linked && s.Active(guildID):
		return ErrTaken
	case sub.MaxGuilds > 0 && n >= sub.MaxGuilds:
		return ErrLimit
	}
	l := Link{GuildID: guildID, GuildName: guildName, UserID: userID, LinkedAt: s.now().UTC()}
	return s.mutate(func() { s.links[guildID] = l })
}

// Deactivate unlinks guildID from userID's subscription.
func (s *Store) Deactivate(userID, guildID snowflake.ID) error {
	s.mu.RLock()
	cur, ok := s.links[guildID]
	s.mu.RUnlock()
	if !ok || cur.UserID != userID {
		return ErrNotFound
	}
	return s.mutate(func() { delete(s.links, guildID) })
}

// mutate applies fn under the lock and saves; on a save failure the change is
// rolled back.
func (s *Store) mutate(fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, links := maps(s.subs), maps(s.links)
	fn()
	if err := s.saveLocked(); err != nil {
		s.subs, s.links = subs, links
		return err
	}
	return nil
}

func maps[V any](m map[snowflake.ID]V) map[snowflake.ID]V {
	out := make(map[snowflake.ID]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	var f fileData
	for _, sub := range s.subs {
		f.Subscriptions = append(f.Subscriptions, sub)
	}
	for _, l := range s.links {
		f.Links = append(f.Links, l)
	}
	slices.SortFunc(f.Subscriptions, func(a, b Subscription) int { return cmp.Compare(a.UserID, b.UserID) })
	slices.SortFunc(f.Links, func(a, b Link) int { return cmp.Compare(a.GuildID, b.GuildID) })
	data, err := json.MarshalIndent(f, "", "  ")
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
