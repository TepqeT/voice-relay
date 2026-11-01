package diag

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/version"
)

// Settings configures the diagnostics. Set once at startup with Configure.
type Settings struct {
	// Dir receives one JSON Lines file per raid ("" = no file, health logs only).
	Dir string
	// Interval between two snapshots in the file. Default 10 s.
	Interval time.Duration
	// LogEvery writes a health line in the logs every N snapshots. Default 3.
	LogEvery int
	// Keep is the number of diagnostic files kept on disk. Default 50.
	Keep int
}

var (
	settingsMu sync.RWMutex
	settings   = Settings{Interval: 10 * time.Second, LogEvery: 3, Keep: 50}

	guildsMu sync.Mutex
	guilds   = map[snowflake.ID]*Guild{}
)

// Configure applies s, keeping defaults for zero fields.
func Configure(s Settings) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if s.Interval <= 0 {
		s.Interval = settings.Interval
	}
	if s.LogEvery <= 0 {
		s.LogEvery = settings.LogEvery
	}
	if s.Keep <= 0 {
		s.Keep = settings.Keep
	}
	settings = s
}

func currentSettings() Settings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return settings
}

// For returns the diagnostics of a guild, creating them on first use. The
// returned value lives for the whole process, so audio components can keep
// the pointer across raids and reconnects.
func For(guildID snowflake.ID) *Guild {
	guildsMu.Lock()
	defer guildsMu.Unlock()
	g, ok := guilds[guildID]
	if !ok {
		g = &Guild{id: guildID, sources: map[snowflake.ID]*SourceStats{}, outputs: map[snowflake.ID]*OutputStats{}, servers: map[snowflake.ID]string{}}
		guilds[guildID] = g
	}
	return g
}

// active returns the guilds with a raid being recorded.
func active() []*Guild {
	guildsMu.Lock()
	defer guildsMu.Unlock()
	out := make([]*Guild, 0, len(guilds))
	for _, g := range guilds {
		if g.recording() {
			out = append(out, g)
		}
	}
	return out
}

// LogRecord copies a warning or error log line into every raid being recorded,
// so the file shows it next to the audio figures of the same moment.
func LogRecord(level slog.Level, msg string) {
	for _, g := range active() {
		g.Event("log", map[string]any{"level": level.String(), "msg": msg})
	}
}

// Guild holds the per-guild statistics and the recording of the current raid.
type Guild struct {
	id snowflake.ID

	mu      sync.Mutex
	sources map[snowflake.ID]*SourceStats
	outputs map[snowflake.ID]*OutputStats
	servers map[snowflake.ID]string // bot -> voice server endpoint

	rec *recording

	// overlap measures the time several callers spoke at once (overlap.go).
	overlap overlapCounter
}

// ObservePacket records one packet received from userID. Hot path.
func (g *Guild) ObservePacket(userID snowflake.ID, seq uint16, ts uint32, at time.Time) {
	if g == nil {
		return
	}
	g.mu.Lock()
	s, ok := g.sources[userID]
	if !ok {
		s = &SourceStats{}
		g.sources[userID] = s
	}
	g.mu.Unlock()
	s.observe(seq, ts, at)
}

// Output returns the playout statistics of one bot.
func (g *Guild) Output(botID snowflake.ID) *OutputStats {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	o, ok := g.outputs[botID]
	if !ok {
		o = &OutputStats{}
		g.outputs[botID] = o
	}
	return o
}

// SetVoiceServer records the Discord voice server a bot is connected to.
// An empty endpoint forgets it.
func (g *Guild) SetVoiceServer(botID snowflake.ID, endpoint string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	prev := g.servers[botID]
	if endpoint == "" {
		delete(g.servers, botID)
	} else {
		g.servers[botID] = endpoint
	}
	g.mu.Unlock()
	if endpoint != "" && endpoint != prev {
		g.Event("voice_server", map[string]any{"bot_id": botID.String(), "endpoint": endpoint})
	}
}

func (g *Guild) voiceServers() map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]string, len(g.servers))
	for b, e := range g.servers {
		out[b.String()] = e
	}
	return out
}

// Event appends a timestamped event to the current recording, if any.
func (g *Guild) Event(kind string, fields map[string]any) {
	if g == nil {
		return
	}
	g.mu.Lock()
	rec := g.rec
	g.mu.Unlock()
	if rec == nil {
		return
	}
	rec.event(kind, fields)
}

func (g *Guild) recording() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rec != nil
}

// Begin starts recording a raid: counters are reset, a file is opened and
// the snapshot ticker starts. A recording already running is ended first.
func (g *Guild) Begin(mode string, bots int) {
	if g == nil {
		return
	}
	g.End()
	// Built before taking g.mu: it may log, and a warning is copied back into
	// the recordings through LogRecord, which takes g.mu.
	rec := newRecording(g, currentSettings(), mode)

	g.mu.Lock()
	for _, s := range g.sources {
		s.reset()
	}
	for _, o := range g.outputs {
		o.reset()
	}
	g.overlap.reset()
	g.rec = rec
	g.mu.Unlock()

	rec.event("raid_start", map[string]any{"mode": mode, "bots": bots, "voice_servers": g.voiceServers(), "version": version.Fork})
	go rec.run()
}

// End stops the recording and returns its summary (zero value when none ran).
func (g *Guild) End() Summary {
	if g == nil {
		return Summary{}
	}
	g.mu.Lock()
	rec := g.rec
	g.rec = nil
	g.mu.Unlock()
	if rec == nil {
		return Summary{}
	}
	sum := rec.stop()
	sum.OverlapSeconds = g.overlap.seconds()
	sum.MaxSimultaneous = int(g.overlap.peak.Load())
	return sum
}

// snapshot collects and resets the interval counters.
func (g *Guild) snapshot() ([]SourceSnapshot, []OutputSnapshot) {
	g.mu.Lock()
	srcIDs := make([]snowflake.ID, 0, len(g.sources))
	for id := range g.sources {
		srcIDs = append(srcIDs, id)
	}
	outIDs := make([]snowflake.ID, 0, len(g.outputs))
	for id := range g.outputs {
		outIDs = append(outIDs, id)
	}
	srcs := make([]*SourceStats, len(srcIDs))
	for i, id := range srcIDs {
		srcs[i] = g.sources[id]
	}
	outs := make([]*OutputStats, len(outIDs))
	for i, id := range outIDs {
		outs[i] = g.outputs[id]
	}
	servers := make(map[snowflake.ID]string, len(g.servers))
	for k, v := range g.servers {
		servers[k] = v
	}
	g.mu.Unlock()

	var ss []SourceSnapshot
	for i, s := range srcs {
		snap := s.take(srcIDs[i].String())
		if snap.Packets > 0 || snap.Reordered > 0 || snap.Duplicates > 0 {
			ss = append(ss, snap)
		}
	}
	var outSnaps []OutputSnapshot
	for i, o := range outs {
		snap := o.take(outIDs[i].String())
		snap.VoiceServer = servers[outIDs[i]]
		if snap.Relayed > 0 || snap.Announcement > 0 || snap.Drops > 0 {
			outSnaps = append(outSnaps, snap)
		}
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].UserID < ss[j].UserID })
	sort.Slice(outSnaps, func(i, j int) bool { return outSnaps[i].BotID < outSnaps[j].BotID })
	return ss, outSnaps
}

// recording is one raid being written to disk.
type recording struct {
	g    *Guild
	set  Settings
	mode string

	start time.Time
	stopc chan struct{}
	done  chan struct{}
	once  sync.Once

	mu    sync.Mutex
	file  *os.File
	w     *bufio.Writer
	path  string
	ticks int
	sum   Summary
	rtRef runtimeRef
	// logs counts log events in the current interval: a per-packet warning
	// burst (voice debug logs) must not flood the file.
	logs int
}

// maxLogsPerInterval caps the log events written per snapshot interval.
const maxLogsPerInterval = 50

func newRecording(g *Guild, set Settings, mode string) *recording {
	r := &recording{
		g:     g,
		set:   set,
		mode:  mode,
		start: time.Now(),
		stopc: make(chan struct{}),
		done:  make(chan struct{}),
		sum:   newSummary(),
	}
	r.rtRef = newRuntimeRef()
	if set.Dir != "" {
		if err := os.MkdirAll(set.Dir, 0o750); err != nil {
			slog.Warn("diag: cannot create diagnostics directory", slog.String("dir", set.Dir), slog.Any("err", err))
			return r
		}
		name := fmt.Sprintf("rvr-%s-%s.jsonl", r.start.UTC().Format("20060102-150405"), g.id)
		path := filepath.Join(set.Dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- operator-configured directory
		if err != nil {
			slog.Warn("diag: cannot create diagnostics file", slog.String("path", path), slog.Any("err", err))
			return r
		}
		r.file, r.w, r.path = f, bufio.NewWriter(f), path
		prune(set.Dir, set.Keep)
		slog.Info("diag: recording raid", slog.String("path", path))
	}
	return r
}

func (r *recording) write(v any) {
	if r.w == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = r.w.Write(b)     // errors surface on Flush
	_ = r.w.WriteByte('\n') // errors surface on Flush
}

func (r *recording) event(kind string, fields map[string]any) {
	line := map[string]any{"t": time.Now().UTC().Format(time.RFC3339Nano), "type": "event", "kind": kind}
	for k, v := range fields {
		line[k] = v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "log" {
		r.logs++
		if r.logs > maxLogsPerInterval {
			r.sum.Events["log_suppressed"]++
			return
		}
	}
	r.sum.addEvent(kind, fields)
	r.write(line)
	if r.w != nil {
		_ = r.w.Flush() // events are rare; a raid cut short still leaves them on disk
	}
}

func (r *recording) run() {
	defer close(r.done)
	t := time.NewTicker(r.set.Interval)
	defer t.Stop()
	for {
		select {
		case <-r.stopc:
			r.tick(true)
			return
		case <-t.C:
			r.tick(false)
		}
	}
}

// tick writes one snapshot and, every LogEvery ticks, a health log line.
func (r *recording) tick(final bool) {
	srcs, outs := r.g.snapshot()
	rt := readRuntime(&r.rtRef)

	r.mu.Lock()
	r.logs = 0
	r.ticks++
	ticks := r.ticks
	r.sum.add(srcs, outs, rt)
	r.write(map[string]any{
		"t":       time.Now().UTC().Format(time.RFC3339Nano),
		"type":    "snapshot",
		"elapsed": round1(time.Since(r.start).Seconds()),
		"sources": srcs,
		"outputs": outs,
		"runtime": rt,
	})
	var flushErr error
	if r.w != nil {
		flushErr = r.w.Flush()
	}
	r.mu.Unlock()

	// Logging happens without r.mu: a warning is copied back into this
	// recording through LogRecord, which needs the lock.
	if flushErr != nil {
		slog.Warn("diag: write failed", slog.String("path", r.path), slog.Any("err", flushErr))
	}
	if !final && ticks%r.set.LogEvery == 0 {
		slog.Info("rvr health", healthAttrs(r.g.id, srcs, outs, rt)...)
	}
}

func (r *recording) stop() Summary {
	r.once.Do(func() { close(r.stopc) })
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sum.DurationSec = round1(time.Since(r.start).Seconds())
	r.sum.VoiceServers = r.g.voiceServers()
	r.sum.finish()
	r.sum.File = r.path
	r.write(map[string]any{"t": time.Now().UTC().Format(time.RFC3339Nano), "type": "summary", "summary": r.sum})
	if r.w != nil {
		_ = r.w.Flush()
		_ = r.file.Close()
	}
	return r.sum
}

// healthAttrs condenses one snapshot into a log line: the worst source and the
// worst output, which is what tells whether the last seconds were clean.
func healthAttrs(guildID snowflake.ID, srcs []SourceSnapshot, outs []OutputSnapshot, rt RuntimeSnapshot) []any {
	var packets, lost, reordered int64
	var jitter, maxGap float64
	for _, s := range srcs {
		packets += s.Packets
		lost += s.Lost
		reordered += s.Reordered
		jitter = math.Max(jitter, s.JitterMs)
		maxGap = math.Max(maxGap, s.MaxGapMs)
	}
	var relayed, drops, stalls int64
	var maxIv float64
	for _, o := range outs {
		relayed += o.Relayed
		drops += o.Drops
		stalls += o.Stalls60
		maxIv = math.Max(maxIv, o.MaxIntervalMs)
	}
	return []any{
		slog.String("guildID", guildID.String()),
		slog.Int("talking", len(srcs)),
		slog.Int64("packets", packets),
		slog.Int64("lost", lost),
		slog.Int64("reordered", reordered),
		slog.Float64("jitterMaxMs", jitter),
		slog.Float64("gapMaxMs", maxGap),
		slog.Int("outputs", len(outs)),
		slog.Int64("relayed", relayed),
		slog.Int64("drops", drops),
		slog.Int64("stalls60", stalls),
		slog.Float64("sendIntervalMaxMs", maxIv),
		slog.Float64("cpuPct", rt.CPUPct),
		slog.Int("goroutines", rt.Goroutines),
		slog.Float64("gcPauseMaxMs", rt.GCPauseMaxMs),
	}
}

// prune keeps the most recent keep diagnostic files of dir.
func prune(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "rvr-") && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, e.Name())
		}
	}
	if len(files) <= keep {
		return
	}
	sort.Strings(files)
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(filepath.Join(dir, f))
	}
}
