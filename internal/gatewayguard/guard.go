// Package gatewayguard watches how often each bot opens a new gateway session
// (one Ready event per identify) and protects its token.
//
// Discord resets a bot's token after about 1000 identifies in a short period.
// Two bugs reached that loop in November 2026: an unknown channel type made
// disgo drop every GUILD_CREATE (token reset on 2026-11-10), then a cache
// deadlock froze the main bot's gateway reader so it re-identified every three
// minutes (~480 a day). The guard turns such a loop into a message to the
// operator, a goroutine dump, and, past a daily limit, a stopped gateway: an
// offline bot keeps its token, a reset one has to be fixed by hand.
//
// Counts are kept on disk so a crash-restart loop does not start them from
// zero; they are dropped when the version changes, so a deploy that fixes the
// cause starts clean.
package gatewayguard

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/statefile"
)

const (
	// DefaultWarnPerHour is the Ready count in one hour that alerts the
	// operator. A healthy bot identifies once per start, rarely more.
	DefaultWarnPerHour = 5
	// DefaultHaltPerDay is the Ready count in 24 hours past which the bot's
	// gateway is closed, well below Discord's ~1000.
	DefaultHaltPerDay = 100
	// warnEvery spaces the alerts of one bot.
	warnEvery = 6 * time.Hour
)

// Actions are what the guard does when a bot loops. Each runs off the gateway
// goroutine; any of them may be nil.
type Actions struct {
	// Alert tells the operator. botID is the looping bot.
	Alert func(botID snowflake.ID, text string)
	// Dump records the goroutines and returns where, "" when it could not.
	Dump func(botID snowflake.ID) string
	// Halt closes the bot's gateway for good (until the next start).
	Halt func(botID snowflake.ID)
}

// Guard counts Ready events per bot.
type Guard struct {
	file    string
	version string

	// Now is the clock, for tests.
	Now         func() time.Time
	WarnPerHour int
	HaltPerDay  int

	mu      sync.Mutex
	readies map[snowflake.ID][]time.Time
	warned  map[snowflake.ID]time.Time
	halted  map[snowflake.ID]bool
	actions Actions
}

type savedState struct {
	Version string                 `json:"version"`
	Readies map[string][]time.Time `json:"readies"`
}

// Open loads the counts kept in file for this version ("" keeps them in memory).
func Open(file, version string) *Guard {
	g := &Guard{
		file: file, version: version, Now: time.Now,
		WarnPerHour: DefaultWarnPerHour, HaltPerDay: DefaultHaltPerDay,
		readies: map[snowflake.ID][]time.Time{},
		warned:  map[snowflake.ID]time.Time{},
		halted:  map[snowflake.ID]bool{},
	}
	if file == "" {
		return g
	}
	b, err := statefile.ReadFile(file)
	if err != nil {
		return g
	}
	var st savedState
	if json.Unmarshal(b, &st) != nil || st.Version != version {
		return g
	}
	for k, ts := range st.Readies {
		if id, err := snowflake.Parse(k); err == nil {
			g.readies[id] = ts
		}
	}
	return g
}

// SetActions installs what to do when a bot loops. Safe while running.
func (g *Guard) SetActions(a Actions) {
	g.mu.Lock()
	g.actions = a
	g.mu.Unlock()
}

// Listener records every Ready of botID.
func (g *Guard) Listener(botID snowflake.ID) bot.EventListener {
	return bot.NewListenerFunc(func(*events.Ready) { g.Ready(botID) })
}

// Blocked reports whether botID reached the daily limit: its gateway must not
// be opened again before the counts expire or the version changes.
func (g *Guard) Blocked(botID snowflake.ID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.halted[botID] {
		return true
	}
	_, day := g.countLocked(botID)
	return day >= g.HaltPerDay
}

// Ready records one new gateway session of botID and reacts to a loop.
// It never blocks: the reactions run in their own goroutine.
func (g *Guard) Ready(botID snowflake.ID) {
	g.mu.Lock()
	now := g.Now()
	g.readies[botID] = append(g.readies[botID], now)
	hour, day := g.countLocked(botID)
	// A session that still opens past the limit (a reconnect already under
	// way when the gateway was closed) is closed again, without a new alert.
	halt := day >= g.HaltPerDay
	firstHalt := halt && !g.halted[botID]
	warn := !halt && hour >= g.WarnPerHour && now.Sub(g.warned[botID]) >= warnEvery
	if halt {
		g.halted[botID] = true
	}
	if warn {
		g.warned[botID] = now
	}
	g.saveLocked()
	a := g.actions
	haltAt := g.HaltPerDay
	g.mu.Unlock()

	if !warn && !halt {
		return
	}
	report := warn || firstHalt
	if report {
		slog.Error("gateway guard: bot keeps opening new sessions",
			slog.String("botID", botID.String()), slog.Int("lastHour", hour), slog.Int("last24h", day), slog.Bool("halt", halt))
	}
	go func() {
		where := ""
		if report && a.Dump != nil {
			where = a.Dump(botID)
		}
		if halt && a.Halt != nil {
			a.Halt(botID)
		}
		if report && a.Alert != nil {
			a.Alert(botID, alertText(halt, hour, day, haltAt, where))
		}
	}()
}

// countLocked drops the entries older than 24 hours and counts the last hour
// and the last day.
func (g *Guard) countLocked(botID snowflake.ID) (hour, day int) {
	now := g.Now()
	kept := g.readies[botID][:0]
	for _, t := range g.readies[botID] {
		if now.Sub(t) < 24*time.Hour {
			kept = append(kept, t)
			if now.Sub(t) < time.Hour {
				hour++
			}
		}
	}
	g.readies[botID] = kept
	return hour, len(kept)
}

func (g *Guard) saveLocked() {
	if g.file == "" {
		return
	}
	st := savedState{Version: g.version, Readies: make(map[string][]time.Time, len(g.readies))}
	for id, ts := range g.readies {
		st.Readies[id.String()] = ts
	}
	b, err := json.Marshal(st)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(g.file), statefile.DirPerm); err == nil {
			err = os.WriteFile(g.file, b, 0o600)
		}
	}
	if err != nil {
		slog.Warn("gateway guard: counts not saved", slog.String("file", g.file), slog.Any("err", err))
	}
}
