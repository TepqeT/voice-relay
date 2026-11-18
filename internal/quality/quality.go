// Package quality builds the end-of-raid audio quality report.
//
// It reads the instruments the bot already records (frame drops, mixer
// latency, DAVE decrypt outcomes, reconnects, gateway latency) from the
// in-process metric reader, takes one snapshot when a raid starts and one when
// it stops, and reports the difference. No external monitoring stack is needed.
package quality

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/telemetry"
	"github.com/TepqeT/voice-relay/internal/version"
)

// Instrument names read by the report. They must match internal/telemetry.
const (
	metricDropped        = "gdc.fanout.frames.dropped.total"
	metricReceive        = "gdc.opus.receive.duration"
	metricPipeline       = "gdc.mixer.pipeline.latency"
	metricDave           = "gdc.dave.decrypt.total"
	metricReconnects     = "gdc.pool.reconnect.attempts.total"
	metricReconnectFails = "gdc.pool.reconnect.failures.total"
	metricGatewayLatency = "gdc.bot.gateway.latency"
)

// Level grades the report as a whole and each finding.
type Level string

const (
	LevelOK    Level = "ok"
	LevelWatch Level = "watch"
	LevelBad   Level = "bad"
)

func (l Level) rank() int {
	switch l {
	case LevelBad:
		return 2
	case LevelWatch:
		return 1
	default:
		return 0
	}
}

// Thresholds. Percentages are of received packets (drops) or of decrypt
// attempts (DAVE). Latency is the relay's own added latency, p95.
const (
	dropWatchPct      = 1.0
	dropBadPct        = 5.0
	daveWatchPct      = 1.0
	daveBadPct        = 5.0
	latencyWatchMs    = 50.0
	latencyBadMs      = 80.0
	gatewayWatchMs    = 300.0
	warningsWatch     = 20
	errorsBad         = 10
	frameDurationSecs = 0.02
)

// Snapshot is the state of every instrument at one instant.
type Snapshot struct {
	At       time.Time
	Metrics  metricdata.ResourceMetrics
	Warnings int64
	Errors   int64
}

// Take captures a snapshot. A collection failure is logged and yields an
// empty snapshot, so the report degrades instead of failing the raid.
func Take(ctx context.Context) Snapshot {
	rm, err := telemetry.CollectLocal(ctx)
	if err != nil {
		slog.WarnContext(ctx, "quality: metric snapshot failed", slog.Any("err", err))
	}
	w, e := telemetry.LogCounts()
	return Snapshot{At: time.Now(), Metrics: rm, Warnings: w, Errors: e}
}

// Finding is one notable fact of a report.
type Finding struct {
	Code  string `json:"code"`
	Level Level  `json:"level"`
}

// Report is the audio quality summary of one raid.
type Report struct {
	Version     string    `json:"version"`
	GuildID     string    `json:"guild_id"`
	Mode        string    `json:"mode"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	DurationSec float64   `json:"duration_sec"`

	// Outputs is the number of bot channels the received audio is relayed to.
	Outputs        int              `json:"outputs"`
	FramesReceived int64            `json:"frames_received"`
	VoiceSec       float64          `json:"voice_sec"`
	FramesDropped  int64            `json:"frames_dropped"`
	DropsByPath    map[string]int64 `json:"drops_by_path,omitempty"`
	// DropPct is the share of relayed frames lost, over all outputs.
	DropPct float64 `json:"drop_pct"`

	LatencySamples int64   `json:"latency_samples"`
	LatencyP50Ms   float64 `json:"latency_p50_ms"`
	LatencyP95Ms   float64 `json:"latency_p95_ms"`
	LatencyP99Ms   float64 `json:"latency_p99_ms"`

	DaveSuccess     int64   `json:"dave_success"`
	DavePassthrough int64   `json:"dave_passthrough"`
	DaveDropped     int64   `json:"dave_dropped"`
	DaveFailure     int64   `json:"dave_failure"`
	DaveFailPct     float64 `json:"dave_fail_pct"`
	// DaveFailReasons counts dropped and failed decrypts by reason, to tell a
	// key transition at join time from a lasting decrypt problem.
	DaveFailReasons map[string]int64 `json:"dave_fail_reasons,omitempty"`

	Reconnects        int64 `json:"reconnects"`
	ReconnectFailures int64 `json:"reconnect_failures"`
	// GatewayLatencyEndMs is the highest bot heartbeat latency when the raid ended.
	GatewayLatencyEndMs float64 `json:"gateway_latency_end_ms"`

	Warnings int64 `json:"warnings"`
	Errors   int64 `json:"errors"`

	Verdict  Level     `json:"verdict"`
	Findings []Finding `json:"findings,omitempty"`

	// Diag is the detailed raid diagnostic (per raid lead, per bot output).
	Diag *diag.Summary `json:"diagnostics,omitempty"`
}

// Diagnostic thresholds.
const (
	netLossWatchPct  = 2.0
	netLossBadPct    = 5.0
	reorderWatchPct  = 1.0
	jitterWatchMs    = 30.0
	jitterBadMs      = 60.0
	stallsWatchPer1k = 5.0 // 60 ms playout stalls per 1000 relayed frames (20 s)
	stallsBadPer1k   = 20.0
	cpuWatchPct      = 80.0
)

// AttachDiag adds the detailed diagnostic to the report and grades it.
func (r *Report) AttachDiag(s diag.Summary) {
	if s.DurationSec == 0 && len(s.Sources) == 0 && len(s.Outputs) == 0 {
		return
	}
	r.Diag = &s
	if w, ok := s.WorstSource(); ok && w.Packets >= 250 { // 5 s of speech at least
		switch {
		case w.LostPct >= netLossBadPct:
			r.add("network_loss", LevelBad)
		case w.LostPct >= netLossWatchPct:
			r.add("network_loss", LevelWatch)
		}
		if w.ReorderPct >= reorderWatchPct {
			r.add("reorder", LevelWatch)
		}
	}
	switch j := s.MaxJitterMs(); {
	case j >= jitterBadMs:
		r.add("jitter", LevelBad)
	case j >= jitterWatchMs:
		r.add("jitter", LevelWatch)
	}
	var relayed int64
	for _, o := range s.Outputs {
		relayed += o.Relayed
	}
	if relayed >= 500 {
		per1k := 1000 * float64(s.Stalls()) / float64(relayed)
		switch {
		case per1k >= stallsBadPer1k:
			r.add("stalls", LevelBad)
		case per1k >= stallsWatchPer1k:
			r.add("stalls", LevelWatch)
		}
	}
	// Different media servers of one region add only a few milliseconds;
	// different regions can put squads tens of milliseconds apart.
	if s.DistinctRegions > 1 {
		r.add("voice_servers", LevelWatch)
	}
	if s.CPUMaxPct >= cpuWatchPct {
		r.add("cpu", LevelWatch)
	}
	// A relay bot unable to encrypt for two minutes while people sat in its
	// channel: they heard nothing (manager.startRelayHealthWatcher).
	if s.Events["silent_relay"] > 0 {
		r.add("silent_relay", LevelBad)
	}
}

// Compute builds the report of guildID between two snapshots. outputs is the
// number of bot channels each received frame is relayed to: drops are counted
// per output, so the loss rate is relative to received frames times outputs.
func Compute(guildID, mode string, outputs int, before, after Snapshot) Report {
	if outputs < 1 {
		outputs = 1
	}
	r := Report{
		Version:     version.Fork,
		GuildID:     guildID,
		Mode:        mode,
		Outputs:     outputs,
		Start:       before.At,
		End:         after.At,
		DurationSec: after.At.Sub(before.At).Seconds(),
		DropsByPath: map[string]int64{},
		Warnings:    nonNeg(after.Warnings - before.Warnings),
		Errors:      nonNeg(after.Errors - before.Errors),
	}
	b := index(before.Metrics)
	a := index(after.Metrics)
	guild := attribute.String("guild_id", guildID)

	for _, d := range sumDeltas(b[metricDropped], a[metricDropped], guild) {
		path, _ := d.attrs.Value("path")
		r.DropsByPath[path.AsString()] += d.value
		r.FramesDropped += d.value
	}
	if recv := histDelta(b[metricReceive], a[metricReceive], guild); recv != nil {
		r.FramesReceived = clampInt64(recv.count)
	}
	r.VoiceSec = float64(r.FramesReceived) * frameDurationSecs
	if r.FramesReceived > 0 {
		r.DropPct = 100 * float64(r.FramesDropped) / float64(r.FramesReceived*int64(outputs))
	}

	if lat := histDelta(b[metricPipeline], a[metricPipeline], guild); lat != nil && lat.count > 0 {
		r.LatencySamples = clampInt64(lat.count)
		r.LatencyP50Ms = lat.quantile(0.50)
		r.LatencyP95Ms = lat.quantile(0.95)
		r.LatencyP99Ms = lat.quantile(0.99)
	}

	for _, d := range sumDeltas(b[metricDave], a[metricDave], attribute.KeyValue{}) {
		outcome, _ := d.attrs.Value("outcome")
		switch outcome.AsString() {
		case "success":
			r.DaveSuccess += d.value
		case "passthrough":
			r.DavePassthrough += d.value
		case "dropped", "failure":
			if outcome.AsString() == "dropped" {
				r.DaveDropped += d.value
			} else {
				r.DaveFailure += d.value
			}
			reason, _ := d.attrs.Value("reason")
			if r.DaveFailReasons == nil {
				r.DaveFailReasons = map[string]int64{}
			}
			r.DaveFailReasons[outcome.AsString()+":"+reason.AsString()] += d.value
		}
	}
	if attempts := r.DaveSuccess + r.DaveDropped + r.DaveFailure; attempts > 0 {
		r.DaveFailPct = 100 * float64(r.DaveDropped+r.DaveFailure) / float64(attempts)
	}

	for _, d := range sumDeltas(b[metricReconnects], a[metricReconnects], attribute.KeyValue{}) {
		r.Reconnects += d.value
	}
	for _, d := range sumDeltas(b[metricReconnectFails], a[metricReconnectFails], attribute.KeyValue{}) {
		r.ReconnectFailures += d.value
	}
	r.GatewayLatencyEndMs = gaugeMax(a[metricGatewayLatency])

	r.grade()
	return r
}

func (r *Report) add(code string, l Level) {
	r.Findings = append(r.Findings, Finding{Code: code, Level: l})
	if l.rank() > r.Verdict.rank() {
		r.Verdict = l
	}
}

func (r *Report) grade() {
	r.Verdict = LevelOK
	if r.FramesReceived == 0 {
		r.add("no_audio", LevelWatch)
	}
	switch {
	case r.DropPct >= dropBadPct:
		r.add("drops", LevelBad)
	case r.DropPct >= dropWatchPct:
		r.add("drops", LevelWatch)
	}
	switch {
	case r.DaveFailPct >= daveBadPct:
		r.add("dave", LevelBad)
	case r.DaveFailPct >= daveWatchPct:
		r.add("dave", LevelWatch)
	}
	switch {
	case r.LatencySamples > 0 && r.LatencyP95Ms >= latencyBadMs:
		r.add("latency", LevelBad)
	case r.LatencySamples > 0 && r.LatencyP95Ms >= latencyWatchMs:
		r.add("latency", LevelWatch)
	}
	switch {
	case r.ReconnectFailures > 0:
		r.add("reconnect_failures", LevelBad)
	case r.Reconnects > 0:
		r.add("reconnects", LevelWatch)
	}
	if r.GatewayLatencyEndMs >= gatewayWatchMs {
		r.add("gateway_latency", LevelWatch)
	}
	switch {
	case r.Errors >= errorsBad:
		r.add("errors", LevelBad)
	case r.Errors > 0:
		r.add("errors", LevelWatch)
	}
	if r.Warnings >= warningsWatch {
		r.add("warnings", LevelWatch)
	}
}

func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// index maps instrument name to its aggregation.
func index(rm metricdata.ResourceMetrics) map[string]metricdata.Aggregation {
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

// matches reports whether attrs carries filter. A zero filter matches all.
// A series that does not carry the filter key at all also matches: those
// instruments are per bot, not per guild, and one deployment serves one guild.
func matches(attrs attribute.Set, filter attribute.KeyValue) bool {
	if !filter.Valid() {
		return true
	}
	v, ok := attrs.Value(filter.Key)
	if !ok {
		return true
	}
	return v.Emit() == filter.Value.Emit()
}

type seriesDelta struct {
	attrs attribute.Set
	value int64
}

// sumDeltas returns, per series, how much a cumulative counter grew between
// the two snapshots. A series missing from the first snapshot started at zero;
// one that went down was reset, so its whole current value is the growth.
func sumDeltas(before, after metricdata.Aggregation, filter attribute.KeyValue) []seriesDelta {
	cur := int64Points(after)
	if len(cur) == 0 {
		return nil
	}
	prev := int64Points(before)
	out := make([]seriesDelta, 0, len(cur))
	for _, p := range cur {
		if !matches(p.Attributes, filter) {
			continue
		}
		v := p.Value
		for _, q := range prev {
			if q.Attributes.Equals(&p.Attributes) {
				if p.Value >= q.Value {
					v = p.Value - q.Value
				}
				break
			}
		}
		if v > 0 {
			out = append(out, seriesDelta{attrs: p.Attributes, value: v})
		}
	}
	return out
}

func int64Points(a metricdata.Aggregation) []metricdata.DataPoint[int64] {
	switch s := a.(type) {
	case metricdata.Sum[int64]:
		return s.DataPoints
	case metricdata.Sum[float64]:
		pts := make([]metricdata.DataPoint[int64], len(s.DataPoints))
		for i, p := range s.DataPoints {
			pts[i] = metricdata.DataPoint[int64]{Attributes: p.Attributes, Value: int64(math.Round(p.Value))}
		}
		return pts
	}
	return nil
}

// histogram is the merged growth of a histogram between two snapshots.
type histogram struct {
	bounds  []float64
	buckets []uint64
	count   uint64
}

func histDelta(before, after metricdata.Aggregation, filter attribute.KeyValue) *histogram {
	cur, ok := after.(metricdata.Histogram[float64])
	if !ok || len(cur.DataPoints) == 0 {
		return nil
	}
	prev, _ := before.(metricdata.Histogram[float64])
	var h *histogram
	for _, p := range cur.DataPoints {
		if !matches(p.Attributes, filter) {
			continue
		}
		buckets := append([]uint64(nil), p.BucketCounts...)
		count := p.Count
		for _, q := range prev.DataPoints {
			if !q.Attributes.Equals(&p.Attributes) || len(q.BucketCounts) != len(buckets) || q.Count > p.Count {
				continue
			}
			for i := range buckets {
				if buckets[i] >= q.BucketCounts[i] {
					buckets[i] -= q.BucketCounts[i]
				}
			}
			count -= q.Count
			break
		}
		if h == nil {
			h = &histogram{bounds: append([]float64(nil), p.Bounds...), buckets: buckets, count: count}
			continue
		}
		if len(h.buckets) == len(buckets) {
			for i := range buckets {
				h.buckets[i] += buckets[i]
			}
			h.count += count
		}
	}
	return h
}

// quantile estimates the q-quantile by linear interpolation inside the bucket
// that holds it, like Prometheus histogram_quantile. Values in the overflow
// bucket are reported as the highest bound.
func (h *histogram) quantile(q float64) float64 {
	if h == nil || h.count == 0 || len(h.buckets) == 0 {
		return 0
	}
	rank := q * float64(h.count)
	var cum float64
	for i, c := range h.buckets {
		prevCum := cum
		cum += float64(c)
		if cum < rank || c == 0 {
			continue
		}
		if i >= len(h.bounds) {
			if len(h.bounds) == 0 {
				return 0
			}
			return h.bounds[len(h.bounds)-1]
		}
		lower := 0.0
		if i > 0 {
			lower = h.bounds[i-1]
		}
		upper := h.bounds[i]
		return lower + (upper-lower)*(rank-prevCum)/float64(c)
	}
	if len(h.bounds) == 0 {
		return 0
	}
	return h.bounds[len(h.bounds)-1]
}

func gaugeMax(a metricdata.Aggregation) float64 {
	var vals []float64
	switch g := a.(type) {
	case metricdata.Gauge[float64]:
		for _, p := range g.DataPoints {
			vals = append(vals, p.Value)
		}
	case metricdata.Gauge[int64]:
		for _, p := range g.DataPoints {
			vals = append(vals, float64(p.Value))
		}
	}
	if len(vals) == 0 {
		return 0
	}
	sort.Float64s(vals)
	return vals[len(vals)-1]
}

// clampInt64 converts a histogram count to int64, saturating instead of
// wrapping on (theoretical) overflow.
func clampInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u) // #nosec G115 -- bounded by the check above
}
