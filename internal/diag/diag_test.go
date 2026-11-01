package diag

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

func TestSourceStatsLossReorderDuplicate(t *testing.T) {
	var s SourceStats
	t0 := time.Unix(1000, 0)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * 20 * time.Millisecond) }
	ts := func(i int) uint32 { return uint32(i * 960) }

	s.observe(65534, ts(0), at(0))
	s.observe(65535, ts(1), at(1))
	s.observe(1, ts(3), at(3)) // wraps; seq 0 lost
	s.observe(0, ts(2), at(3)) // the lost one arrives late: reordered
	s.observe(1, ts(3), at(3)) // duplicate
	s.observe(2, ts(4), at(4))
	snap := s.take("u")
	if snap.Packets != 6 || snap.Lost != 1 || snap.Reordered != 1 || snap.Duplicates != 1 {
		t.Fatalf("got %+v", snap)
	}
	if snap.Spurts != 1 {
		t.Fatalf("spurts = %d, want 1", snap.Spurts)
	}
	if snap.JitterMs > 0.1 {
		t.Fatalf("regular arrivals must give ~0 jitter, got %v", snap.JitterMs)
	}
	if again := s.take("u"); again.Packets != 0 || again.Lost != 0 {
		t.Fatalf("counters not reset: %+v", again)
	}
}

func TestSourceStatsBurstsAndJitter(t *testing.T) {
	var s SourceStats
	t0 := time.Unix(1000, 0)
	// Four packets held back 60 ms then released together: one late, then a burst.
	s.observe(1, 0, t0)
	s.observe(2, 960, t0.Add(80*time.Millisecond))
	s.observe(3, 1920, t0.Add(81*time.Millisecond))
	s.observe(4, 2880, t0.Add(82*time.Millisecond))
	// Silence of 2 s, then speech again: a new spurt, not a gap.
	s.observe(5, 2880+96000, t0.Add(2082*time.Millisecond))
	snap := s.take("u")
	if snap.Gaps[0] != 2 { // two bursts < 5 ms
		t.Fatalf("bursts = %v", snap.Gaps)
	}
	if snap.Gaps[5] != 1 { // one 60-100 ms gap
		t.Fatalf("late = %v", snap.Gaps)
	}
	if snap.Spurts != 2 {
		t.Fatalf("spurts = %d, want 2", snap.Spurts)
	}
	if snap.MaxGapMs != 80 || snap.JitterMs <= 0 {
		t.Fatalf("max gap %v jitter %v", snap.MaxGapMs, snap.JitterMs)
	}
}

func TestOutputStatsStalls(t *testing.T) {
	var o OutputStats
	o.Frame(false, 2, 0)
	time.Sleep(70 * time.Millisecond)
	o.Frame(false, 0, 1)
	o.Frame(true, 0, 0)
	snap := o.take("b")
	if snap.Relayed != 2 || snap.Announcement != 1 || snap.Drops != 1 || snap.DepthMax != 2 || snap.DepthAvg != 1 {
		t.Fatalf("got %+v", snap)
	}
	if snap.Stalls60 != 1 || snap.MaxIntervalMs < 60 {
		t.Fatalf("stall not seen: %+v", snap)
	}
	var nilOut *OutputStats
	nilOut.Frame(false, 0, 0) // must not panic
}

func TestSummaryMixTime(t *testing.T) {
	s := newSummary()
	s.addEvent("route", map[string]any{"from": "copy", "to": "mix"})
	s.mixFrom = s.mixFrom.Add(-3 * time.Second)
	s.addEvent("route", map[string]any{"from": "mix", "to": "copy"})
	s.VoiceServers = map[string]string{"1": "c-ams1.discord.media:443", "2": "c-ams1.discord.media:8443", "3": "c-fra2.discord.media:443"}
	s.finish()
	if s.MixSwitches != 1 || s.MixSeconds < 2.9 || s.MixSeconds > 3.5 {
		t.Fatalf("mix %d %v", s.MixSwitches, s.MixSeconds)
	}
	if s.DistinctVoiceServers != 2 || s.DistinctRegions != 2 {
		t.Fatalf("distinct servers = %d regions = %d, want 2 2", s.DistinctVoiceServers, s.DistinctRegions)
	}
}

func TestRecordingWritesFile(t *testing.T) {
	dir := t.TempDir()
	Configure(Settings{Dir: dir, Interval: 20 * time.Millisecond, LogEvery: 1, Keep: 5})
	defer Configure(Settings{Dir: "", Interval: 10 * time.Second, LogEvery: 3, Keep: 50})

	g := For(snowflake.ID(4242))
	g.SetVoiceServer(10, "c-ams1.discord.media:443")
	g.Begin("one_caller", 3)
	out := g.Output(10)
	t0 := time.Now()
	for i := range 20 {
		g.ObservePacket(77, uint16(i), uint32(i*960), t0.Add(time.Duration(i)*20*time.Millisecond))
		out.Frame(false, 1, 0)
	}
	g.Event("announce", map[string]any{"sound": "debut"})
	time.Sleep(60 * time.Millisecond)
	sum := g.End()

	if len(sum.Sources) != 1 || sum.Sources[0].Packets != 20 || sum.Sources[0].UserID != "77" {
		t.Fatalf("sources %+v", sum.Sources)
	}
	if len(sum.Outputs) != 1 || sum.Outputs[0].Relayed != 20 || sum.Outputs[0].VoiceServer == "" {
		t.Fatalf("outputs %+v", sum.Outputs)
	}
	if sum.Events["announce"] != 1 || sum.Events["raid_start"] != 1 || sum.DistinctVoiceServers != 1 {
		t.Fatalf("events %+v servers %d", sum.Events, sum.DistinctVoiceServers)
	}
	f, err := os.Open(sum.File)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kinds := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("invalid JSON line %q: %v", sc.Text(), err)
		}
		kinds[line["type"].(string)]++
	}
	if kinds["snapshot"] < 2 || kinds["event"] < 2 || kinds["summary"] != 1 {
		t.Fatalf("line types %v", kinds)
	}
	if g.End().DurationSec != 0 {
		t.Fatal("second End must be a no-op")
	}
}

// forwardingHandler mimics the production logger: warnings are copied into
// the recordings. It must not deadlock when the recorder itself logs.
type forwardingHandler struct{ slog.Handler }

func (h forwardingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		LogRecord(r.Level, r.Message)
	}
	return h.Handler.Handle(ctx, r)
}

func TestNoDeadlockWhenRecorderLogs(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(forwardingHandler{slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})}))
	defer slog.SetDefault(prev)

	// A file where the directory should be makes MkdirAll fail and warn.
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	Configure(Settings{Dir: filepath.Join(bad, "sub"), Interval: 10 * time.Millisecond, LogEvery: 1})
	defer Configure(Settings{Dir: "", Interval: 10 * time.Second, LogEvery: 3, Keep: 50})

	done := make(chan struct{})
	go func() {
		g := For(snowflake.ID(5151))
		g.Begin("one_caller", 2)
		slog.Warn("something odd")
		time.Sleep(30 * time.Millisecond)
		sum := g.End()
		if sum.Events["log"] < 1 {
			t.Errorf("warning not recorded: %+v", sum.Events)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"rvr-1.jsonl", "rvr-2.jsonl", "rvr-3.jsonl", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prune(dir, 2)
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "keep.txt,rvr-2.jsonl,rvr-3.jsonl" {
		t.Fatalf("after prune: %v", names)
	}
}

func TestReadRuntime(t *testing.T) {
	ref := newRuntimeRef()
	rt := readRuntime(&ref)
	if rt.Goroutines <= 0 || rt.HeapMB <= 0 {
		t.Fatalf("runtime snapshot %+v", rt)
	}
}

func TestLogFloodCapped(t *testing.T) {
	Configure(Settings{Dir: "", Interval: time.Hour, LogEvery: 1})
	defer Configure(Settings{Dir: "", Interval: 10 * time.Second, LogEvery: 3, Keep: 50})
	g := For(snowflake.ID(6161))
	g.Begin("one_caller", 2)
	for range maxLogsPerInterval + 30 {
		LogRecord(slog.LevelWarn, "spam")
	}
	sum := g.End()
	if sum.Events["log"] != maxLogsPerInterval || sum.Events["log_suppressed"] != 30 {
		t.Fatalf("events %+v", sum.Events)
	}
}

func TestSourceStatsStreamRestart(t *testing.T) {
	var s SourceStats
	t0 := time.Unix(1000, 0)
	for i := range 10 {
		s.observe(uint16(30000+i), uint32(i*960), t0.Add(time.Duration(i)*20*time.Millisecond))
	}
	// The client rejoins: new stream starting far below the old sequence.
	t1 := t0.Add(5 * time.Second)
	for i := range 10 {
		s.observe(uint16(100+i), uint32(i*960), t1.Add(time.Duration(i)*20*time.Millisecond))
	}
	snap := s.take("u")
	if snap.Reordered != 0 || snap.Lost != 0 || snap.Packets != 20 {
		t.Fatalf("restart misread as reordering: %+v", snap)
	}
	// A short negative jump right after a packet is still a late packet.
	s.observe(105, 0, t1.Add(220*time.Millisecond))
	if snap := s.take("u"); snap.Reordered != 1 {
		t.Fatalf("late packet not counted: %+v", snap)
	}
}

func TestOverlapCounterFoldsMixers(t *testing.T) {
	var o overlapCounter
	base := time.Unix(1000, 0)
	// 3 destination mixers report the same 20 ms frame, 50 frames in a row.
	for i := range 50 {
		at := base.Add(time.Duration(i) * overlapTick)
		for m := range 3 {
			o.add(2+m%2, at.Add(time.Duration(m)*time.Millisecond))
		}
	}
	if got := o.seconds(); got != 1 {
		t.Fatalf("overlap = %v s, want 1 s (50 frames of 20 ms)", got)
	}
	if p := o.peak.Load(); p != 3 {
		t.Fatalf("peak = %d, want 3", p)
	}
	o.reset()
	if o.seconds() != 0 || o.peak.Load() != 0 {
		t.Fatal("reset must clear the counter")
	}
}

func TestMixFrameIgnoresSingleVoiceAndNil(t *testing.T) {
	var g *Guild
	g.MixFrame(3) // nil guild: no panic
	g2 := &Guild{}
	g2.MixFrame(1)
	if g2.overlap.frames.Load() != 0 {
		t.Fatal("a single voice is not an overlap")
	}
}

// A second raid in the same process used to crash with "sync: unlock of
// unlocked mutex": reset overwrote the held mutex of every known source.
func TestSecondRaidAfterTrafficDoesNotCrash(t *testing.T) {
	Configure(Settings{Dir: "", Interval: time.Hour, LogEvery: 3, Keep: 5})
	defer Configure(Settings{Dir: "", Interval: 10 * time.Second, LogEvery: 3, Keep: 50})

	g := For(snowflake.ID(5151))
	t0 := time.Now()
	for round := range 3 {
		g.Begin("one_many_guild_caller", 5)
		for i := range 10 {
			g.ObservePacket(77, uint16(i), uint32(i*960), t0.Add(time.Duration(i)*20*time.Millisecond))
		}
		g.End()
		t.Logf("round %d ok", round)
	}
}

func TestSourceStatsResetKeepsMutexUsable(t *testing.T) {
	var s SourceStats
	s.observe(1, 960, time.Unix(1000, 0))
	s.reset()
	s.reset()
	if snap := s.take("u"); snap.Packets != 0 {
		t.Fatalf("reset left %+v", snap)
	}
	s.observe(5, 960, time.Unix(1001, 0))
	if snap := s.take("u"); snap.Packets != 1 || snap.Lost != 0 {
		t.Fatalf("after reset: %+v", snap)
	}
}
