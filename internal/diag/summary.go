package diag

import (
	"math"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SourceTotal is the whole-raid receive summary of one raid lead.
type SourceTotal struct {
	UserID      string  `json:"user_id"`
	Packets     int64   `json:"packets"`
	Lost        int64   `json:"lost"`
	Reordered   int64   `json:"reordered"`
	Duplicates  int64   `json:"duplicates"`
	LostPct     float64 `json:"lost_pct"`
	ReorderPct  float64 `json:"reorder_pct"`
	Bursts      int64   `json:"bursts"`       // packets arriving < 5 ms after the previous one
	LatePackets int64   `json:"late_packets"` // packets arriving 40-200 ms after the previous one
	MaxGapMs    float64 `json:"max_gap_ms"`
	MaxJitterMs float64 `json:"max_jitter_ms"`
	TalkSpurts  int64   `json:"talk_spurts"`
	gaps        [8]int64
}

// OutputTotal is the whole-raid playout summary of one bot.
type OutputTotal struct {
	BotID         string  `json:"bot_id"`
	VoiceServer   string  `json:"voice_server,omitempty"`
	Relayed       int64   `json:"relayed"`
	Announcement  int64   `json:"announcement"`
	Drops         int64   `json:"drops"`
	DropPct       float64 `json:"drop_pct"`
	QueueMax      int64   `json:"queue_max"`
	Stalls40      int64   `json:"stalls_40ms"`
	Stalls60      int64   `json:"stalls_60ms"`
	MaxIntervalMs float64 `json:"max_interval_ms"`
	depthSum      float64
}

// Summary is the whole-raid diagnostic, attached to the quality report.
type Summary struct {
	DurationSec  float64           `json:"duration_sec"`
	File         string            `json:"file,omitempty"`
	Sources      []SourceTotal     `json:"sources,omitempty"`
	Outputs      []OutputTotal     `json:"outputs,omitempty"`
	VoiceServers map[string]string `json:"voice_servers,omitempty"`
	// DistinctVoiceServers counts the voice servers used by the bots. More
	// than one means squads may hear calls with different delays.
	DistinctVoiceServers int `json:"distinct_voice_servers"`
	// DistinctRegions counts the voice regions (for example c-ams, c-fra).
	DistinctRegions int `json:"distinct_regions"`
	// MixSeconds is the time spent in mix mode: several callers present in
	// the channel, whether they speak or not. A single speaker is still
	// forwarded untouched in this mode; only overlaps are re-encoded.
	MixSeconds  float64 `json:"mix_seconds"`
	MixSwitches int     `json:"mix_switches"`
	// OverlapSeconds is the time several callers actually spoke at once, the
	// only moments their voices are mixed and re-encoded.
	OverlapSeconds float64 `json:"overlap_seconds"`
	// MaxSimultaneous is the largest number of voices mixed into one frame.
	MaxSimultaneous int            `json:"max_simultaneous"`
	Events          map[string]int `json:"events,omitempty"`
	CPUMaxPct       float64        `json:"cpu_max_pct"`
	CPUAvgPct       float64        `json:"cpu_avg_pct"`
	GCPauseMax      float64        `json:"gc_pause_max_ms"`
	HeapMaxMB       float64        `json:"heap_max_mb"`

	sources map[string]*SourceTotal
	outputs map[string]*OutputTotal
	cpuSum  float64
	cpuN    int
	mixFrom time.Time
}

func newSummary() Summary {
	return Summary{
		Events:  map[string]int{},
		sources: map[string]*SourceTotal{},
		outputs: map[string]*OutputTotal{},
	}
}

func (s *Summary) add(srcs []SourceSnapshot, outs []OutputSnapshot, rt RuntimeSnapshot) {
	for _, x := range srcs {
		t := s.sources[x.UserID]
		if t == nil {
			t = &SourceTotal{UserID: x.UserID}
			s.sources[x.UserID] = t
		}
		t.Packets += x.Packets
		t.Lost += x.Lost
		t.Reordered += x.Reordered
		t.Duplicates += x.Duplicates
		t.TalkSpurts += x.Spurts
		for i := range t.gaps {
			if i < len(x.Gaps) {
				t.gaps[i] += x.Gaps[i]
			}
		}
		t.MaxGapMs = math.Max(t.MaxGapMs, x.MaxGapMs)
		t.MaxJitterMs = math.Max(t.MaxJitterMs, x.MaxJitterMs)
	}
	for _, x := range outs {
		t := s.outputs[x.BotID]
		if t == nil {
			t = &OutputTotal{BotID: x.BotID}
			s.outputs[x.BotID] = t
		}
		if x.VoiceServer != "" {
			t.VoiceServer = x.VoiceServer
		}
		t.Relayed += x.Relayed
		t.Announcement += x.Announcement
		t.Drops += x.Drops
		t.Stalls40 += x.Stalls40
		t.Stalls60 += x.Stalls60
		t.depthSum += x.DepthAvg * float64(x.Relayed)
		if x.DepthMax > t.QueueMax {
			t.QueueMax = x.DepthMax
		}
		t.MaxIntervalMs = math.Max(t.MaxIntervalMs, x.MaxIntervalMs)
	}
	s.cpuSum += rt.CPUPct
	s.cpuN++
	s.CPUMaxPct = math.Max(s.CPUMaxPct, rt.CPUPct)
	s.GCPauseMax = math.Max(s.GCPauseMax, rt.GCPauseMaxMs)
	s.HeapMaxMB = math.Max(s.HeapMaxMB, rt.HeapMB)
}

// addEvent counts events and tracks the time spent in mix mode, from the
// router transitions (mix = several callers present in a channel).
func (s *Summary) addEvent(kind string, fields map[string]any) {
	s.Events[kind]++
	if kind != "route" {
		return
	}
	to, _ := fields["to"].(string)
	now := time.Now()
	switch {
	case to == "mix" && s.mixFrom.IsZero():
		s.mixFrom = now
		s.MixSwitches++
	case to != "mix" && !s.mixFrom.IsZero():
		s.MixSeconds += now.Sub(s.mixFrom).Seconds()
		s.mixFrom = time.Time{}
	}
}

func (s *Summary) finish() {
	if !s.mixFrom.IsZero() {
		s.MixSeconds += time.Since(s.mixFrom).Seconds()
		s.mixFrom = time.Time{}
	}
	s.MixSeconds = round1(s.MixSeconds)
	if s.cpuN > 0 {
		s.CPUAvgPct = round1(s.cpuSum / float64(s.cpuN))
	}
	s.Sources = s.Sources[:0]
	for _, t := range s.sources {
		expected := t.Packets + t.Lost
		if expected > 0 {
			t.LostPct = round2(100 * float64(t.Lost) / float64(expected))
		}
		if t.Packets > 0 {
			t.ReorderPct = round2(100 * float64(t.Reordered) / float64(t.Packets))
		}
		t.Bursts = t.gaps[0]
		t.LatePackets = t.gaps[4] + t.gaps[5] + t.gaps[6]
		s.Sources = append(s.Sources, *t)
	}
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].Packets > s.Sources[j].Packets })
	s.Outputs = s.Outputs[:0]
	for _, t := range s.outputs {
		if total := t.Relayed + t.Drops; total > 0 {
			t.DropPct = round2(100 * float64(t.Drops) / float64(total))
		}
		s.Outputs = append(s.Outputs, *t)
	}
	sort.Slice(s.Outputs, func(i, j int) bool { return s.Outputs[i].BotID < s.Outputs[j].BotID })
	hosts, regions := map[string]bool{}, map[string]bool{}
	for _, e := range s.VoiceServers {
		h := voiceHost(e)
		hosts[h] = true
		regions[voiceRegion(h)] = true
	}
	s.DistinctVoiceServers = len(hosts)
	s.DistinctRegions = len(regions)
}

// voiceRegion keeps the letters before the first digit of a voice host:
// "c-ams18-50a8bfa.discord.media" gives "c-ams".
func voiceRegion(host string) string {
	for i, c := range host {
		if c >= '0' && c <= '9' {
			return host[:i]
		}
	}
	return host
}

// voiceHost strips the port so "c-ams12-1234.discord.media:443" and the same
// host on another port count once.
func voiceHost(endpoint string) string {
	if i := strings.LastIndex(endpoint, ":"); i > 0 {
		return endpoint[:i]
	}
	return endpoint
}

// Totals helpers for the report.

// WorstSource returns the source with the highest loss, or false.
func (s Summary) WorstSource() (SourceTotal, bool) {
	if len(s.Sources) == 0 {
		return SourceTotal{}, false
	}
	w := s.Sources[0]
	for _, x := range s.Sources[1:] {
		if x.LostPct+x.ReorderPct > w.LostPct+w.ReorderPct {
			w = x
		}
	}
	return w, true
}

// Stalls returns the total mid-speech playout stalls of 60 ms or more.
func (s Summary) Stalls() int64 {
	var n int64
	for _, o := range s.Outputs {
		n += o.Stalls60
	}
	return n
}

// MaxJitterMs is the highest smoothed jitter seen on any source.
func (s Summary) MaxJitterMs() float64 {
	var m float64
	for _, x := range s.Sources {
		m = math.Max(m, x.MaxJitterMs)
	}
	return m
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// RuntimeSnapshot is the process state at one snapshot.
type RuntimeSnapshot struct {
	CPUPct       float64 `json:"cpu_pct"`
	Goroutines   int     `json:"goroutines"`
	HeapMB       float64 `json:"heap_mb"`
	GCPauseMaxMs float64 `json:"gc_pause_max_ms"`
}

type cpuSample struct {
	at    time.Time
	ticks float64
}

// readCPU reads this process's CPU time from /proc (Linux, the container).
// On other systems it returns a zero sample and CPU stays at 0.
func readCPU() cpuSample {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return cpuSample{at: time.Now()}
	}
	s := string(b)
	// Fields after the command name, which is in parentheses and may contain spaces.
	if i := strings.LastIndexByte(s, ')'); i >= 0 {
		s = s[i+1:]
	}
	f := strings.Fields(s)
	// utime and stime are fields 14 and 15 of the full line, 12 and 13 here.
	if len(f) < 13 {
		return cpuSample{at: time.Now()}
	}
	u, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return cpuSample{at: time.Now(), ticks: u + st}
}

// clockTicks is the kernel USER_HZ, 100 on every mainstream Linux build.
const clockTicks = 100.0

// gcPauseMetric is the stop-the-world GC pause histogram (Go 1.22 and later).
const gcPauseMetric = "/sched/pauses/total/gc:seconds"

// runtimeRef carries the previous readings, so each snapshot reports the
// interval and not the whole process lifetime.
type runtimeRef struct {
	cpu      cpuSample
	gcCounts []uint64
}

func newRuntimeRef() runtimeRef {
	ref := runtimeRef{cpu: readCPU()}
	sample := []metrics.Sample{{Name: gcPauseMetric}}
	metrics.Read(sample)
	if sample[0].Value.Kind() == metrics.KindFloat64Histogram {
		ref.gcCounts = append([]uint64(nil), sample[0].Value.Float64Histogram().Counts...)
	}
	return ref
}

func readRuntime(ref *runtimeRef) RuntimeSnapshot {
	rt := RuntimeSnapshot{Goroutines: runtime.NumGoroutine()}

	cur := readCPU()
	if el := cur.at.Sub(ref.cpu.at).Seconds(); el > 0 && cur.ticks >= ref.cpu.ticks {
		rt.CPUPct = round1(100 * (cur.ticks - ref.cpu.ticks) / clockTicks / el)
	}
	ref.cpu = cur

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rt.HeapMB = round1(float64(ms.HeapAlloc) / (1 << 20))

	sample := []metrics.Sample{{Name: gcPauseMetric}}
	metrics.Read(sample)
	if sample[0].Value.Kind() == metrics.KindFloat64Histogram {
		h := sample[0].Value.Float64Histogram()
		for i := len(h.Counts) - 1; i >= 0; i-- {
			var before uint64
			if i < len(ref.gcCounts) {
				before = ref.gcCounts[i]
			}
			if h.Counts[i] > before {
				b := h.Buckets[i+1]
				if math.IsInf(b, 1) {
					b = h.Buckets[i]
				}
				rt.GCPauseMaxMs = round1(b * 1000)
				break
			}
		}
		ref.gcCounts = append(ref.gcCounts[:0], h.Counts...)
	}
	return rt
}
