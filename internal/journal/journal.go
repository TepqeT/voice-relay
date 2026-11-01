// Package journal keeps the recent installation and setup events (bot
// added or removed, missing permissions, refused channel or role creation,
// failed sign-in...) so the administrator can see on the site why an
// installation does not work, and logs each of them.
package journal

import (
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// Level of an event.
type Level string

const (
	Info    Level = "info"
	Warning Level = "warning"
	Problem Level = "problem"
)

// Event is one installation or setup event.
type Event struct {
	At      time.Time
	Level   Level
	GuildID snowflake.ID // 0 when not about a server
	Guild   string       // server name when known
	UserID  snowflake.ID // who did it, 0 when nobody (Discord event)
	Kind    string       // short key, e.g. "bot_joined", "missing_permissions"
	Detail  string       // human readable, English
}

const capacity = 300

var (
	mu     sync.Mutex
	events []Event
	now    = time.Now
)

// Record adds e (timestamped now) and logs it.
func Record(e Event) {
	e.At = now().UTC()
	if e.Level == "" {
		e.Level = Info
	}
	attrs := []any{slog.String("kind", e.Kind), slog.String("detail", e.Detail)}
	if e.GuildID != 0 {
		attrs = append(attrs, slog.String("guildID", e.GuildID.String()), slog.String("guild", e.Guild))
	}
	if e.UserID != 0 {
		attrs = append(attrs, slog.String("userID", e.UserID.String()))
	}
	switch e.Level {
	case Problem:
		slog.Error("setup: "+e.Kind, attrs...)
	case Warning:
		slog.Warn("setup: "+e.Kind, attrs...)
	default:
		slog.Info("setup: "+e.Kind, attrs...)
	}
	mu.Lock()
	defer mu.Unlock()
	events = append(events, e)
	if len(events) > capacity {
		events = append([]Event(nil), events[len(events)-capacity:]...)
	}
}

// Recent returns up to n events, newest first; problemsOnly drops Info.
func Recent(n int, problemsOnly bool) []Event {
	mu.Lock()
	defer mu.Unlock()
	var out []Event
	for i := len(events) - 1; i >= 0 && len(out) < n; i-- {
		if problemsOnly && events[i].Level == Info {
			continue
		}
		out = append(out, events[i])
	}
	return out
}

// ForGuild returns up to n events of guildID, newest first.
func ForGuild(guildID snowflake.ID, n int) []Event {
	mu.Lock()
	defer mu.Unlock()
	var out []Event
	for i := len(events) - 1; i >= 0 && len(out) < n; i-- {
		if events[i].GuildID == guildID {
			out = append(out, events[i])
		}
	}
	return out
}

// ForGuildIssues returns up to n warnings and problems of guildID, newest
// first.
func ForGuildIssues(guildID snowflake.ID, n int) []Event {
	mu.Lock()
	defer mu.Unlock()
	var out []Event
	for i := len(events) - 1; i >= 0 && len(out) < n; i-- {
		if events[i].GuildID == guildID && events[i].Level != Info {
			out = append(out, events[i])
		}
	}
	return out
}

// reset empties the journal (tests).
func reset() {
	mu.Lock()
	events = nil
	mu.Unlock()
}
