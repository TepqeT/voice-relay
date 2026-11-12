package gatewayguard

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

type recorder struct {
	mu     sync.Mutex
	alerts []string
	halts  []snowflake.ID
	dumps  int
	done   chan struct{}
}

func newRecorder() *recorder { return &recorder{done: make(chan struct{}, 16)} }

func (r *recorder) actions() Actions {
	return Actions{
		Alert: func(_ snowflake.ID, text string) {
			r.mu.Lock()
			r.alerts = append(r.alerts, text)
			r.mu.Unlock()
			r.done <- struct{}{}
		},
		Dump: func(snowflake.ID) string { r.mu.Lock(); r.dumps++; r.mu.Unlock(); return "/data/diagnostics/x.txt" },
		Halt: func(id snowflake.ID) { r.mu.Lock(); r.halts = append(r.halts, id); r.mu.Unlock() },
	}
}

func (r *recorder) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no alert")
	}
}

func clockAt(start time.Time) (func() time.Time, func(time.Duration)) {
	now := start
	var mu sync.Mutex
	return func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
}

func TestWarnsOnceThenHaltsBeforeDiscordLimit(t *testing.T) {
	const bot snowflake.ID = 42
	g := Open("", "v1")
	now, advance := clockAt(time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC))
	g.Now = now
	r := newRecorder()
	g.SetActions(r.actions())

	for i := 0; i < DefaultWarnPerHour-1; i++ {
		g.Ready(bot)
		advance(time.Minute)
	}
	if len(r.alerts) != 0 {
		t.Fatal("alerted below the hourly threshold")
	}
	g.Ready(bot) // 5th in the hour
	r.wait(t)
	if !strings.Contains(r.alerts[0], "une heure") {
		t.Fatalf("warn text = %q", r.alerts[0])
	}

	// The v66 loop: one session every three minutes. No second warning
	// before warnEvery, then the halt before 100 a day.
	for i := 0; i < 200 && !g.Blocked(bot); i++ {
		advance(3 * time.Minute)
		g.Ready(bot)
	}
	r.wait(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.halts) != 1 || r.halts[0] != bot {
		t.Fatalf("halts = %v, want one for the looping bot", r.halts)
	}
	last := r.alerts[len(r.alerts)-1]
	if !strings.Contains(last, "coupée") || !strings.Contains(last, "x.txt") {
		t.Fatalf("halt text = %q", last)
	}
	if !g.Blocked(bot) || g.Blocked(7) {
		t.Fatal("Blocked must concern the looping bot only")
	}
}

func TestHealthyBotNeverAlerts(t *testing.T) {
	g := Open("", "v1")
	now, advance := clockAt(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	g.Now = now
	r := newRecorder()
	g.SetActions(r.actions())
	for i := 0; i < 30; i++ { // one new session every 2 hours, for 2.5 days
		g.Ready(1)
		advance(2 * time.Hour)
	}
	time.Sleep(50 * time.Millisecond)
	if len(r.alerts) != 0 || len(r.halts) != 0 || g.Blocked(1) {
		t.Fatalf("healthy bot: alerts=%v halts=%v", r.alerts, r.halts)
	}
}

func TestCountsSurviveRestartButNotANewVersion(t *testing.T) {
	file := filepath.Join(t.TempDir(), "gateway-guard.json")
	now, _ := clockAt(time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC))
	g := Open(file, "v1")
	g.Now = now
	for i := 0; i < DefaultHaltPerDay; i++ {
		g.Ready(5)
	}
	if !g.Blocked(5) {
		t.Fatal("not blocked at the daily limit")
	}

	again := Open(file, "v1") // crash-restart, same version
	again.Now = now
	if !again.Blocked(5) {
		t.Fatal("a restart must not reset the count")
	}
	fixed := Open(file, "v2") // deploy of a fix
	fixed.Now = now
	if fixed.Blocked(5) {
		t.Fatal("a new version must start clean")
	}
}

func TestWriteGoroutines(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteGoroutines(dir, 9, time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path built by the test in its temp dir
	if err != nil || !strings.Contains(string(b), "TestWriteGoroutines") {
		t.Fatalf("dump %s: %v", path, err)
	}
	if _, err := WriteGoroutines("", 9, time.Now()); err == nil {
		t.Fatal("empty dir accepted")
	}
}

// A session that opens after the halt (a reconnect already in flight) is
// closed again, quietly.
func TestSessionAfterHaltIsClosedAgainWithoutNewAlert(t *testing.T) {
	g := Open("", "v1")
	g.HaltPerDay = 3
	now, _ := clockAt(time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC))
	g.Now = now
	r := newRecorder()
	g.SetActions(r.actions())
	for i := 0; i < 3; i++ {
		g.Ready(8)
	}
	r.wait(t) // halt alert
	g.Ready(8)
	time.Sleep(100 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.halts) != 2 || len(r.alerts) != 1 || r.dumps != 1 {
		t.Fatalf("halts=%d alerts=%d dumps=%d, want 2 halts, 1 alert, 1 dump", len(r.halts), len(r.alerts), r.dumps)
	}
}
