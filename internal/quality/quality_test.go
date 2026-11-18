package quality

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TepqeT/voice-relay/internal/diag"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type rig struct {
	reader   *sdkmetric.ManualReader
	dropped  metric.Int64Counter
	receive  metric.Float64Histogram
	pipeline metric.Float64Histogram
	dave     metric.Int64Counter
	reconn   metric.Int64Counter
}

func newRig(t *testing.T) *rig {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := mp.Meter("test")
	r := &rig{reader: reader}
	var err error
	if r.dropped, err = m.Int64Counter(metricDropped); err != nil {
		t.Fatal(err)
	}
	if r.receive, err = m.Float64Histogram(metricReceive, metric.WithExplicitBucketBoundaries(0.5, 1, 2, 5, 12, 20)); err != nil {
		t.Fatal(err)
	}
	if r.pipeline, err = m.Float64Histogram(metricPipeline, metric.WithExplicitBucketBoundaries(10, 15, 20, 25, 30, 35, 40, 50, 60, 80, 120)); err != nil {
		t.Fatal(err)
	}
	if r.dave, err = m.Int64Counter(metricDave); err != nil {
		t.Fatal(err)
	}
	if r.reconn, err = m.Int64Counter(metricReconnects); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) snap(t *testing.T, at time.Time) Snapshot {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	return Snapshot{At: at, Metrics: rm}
}

func guildAttr(id string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("guild_id", id))
}

func TestComputeDeltasOnlyCountTheRaid(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	g := guildAttr("42")
	// Activity before the raid must not show in the report.
	r.dropped.Add(ctx, 7, metric.WithAttributes(attribute.String("guild_id", "42"), attribute.String("path", "provider")))
	for range 100 {
		r.receive.Record(ctx, 0.3, g)
	}
	t0 := time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC)
	before := r.snap(t, t0)

	r.dropped.Add(ctx, 3, metric.WithAttributes(attribute.String("guild_id", "42"), attribute.String("path", "provider")))
	r.dropped.Add(ctx, 2, metric.WithAttributes(attribute.String("guild_id", "42"), attribute.String("path", "receiver")))
	r.dropped.Add(ctx, 50, metric.WithAttributes(attribute.String("guild_id", "99"), attribute.String("path", "provider")))
	for range 1000 {
		r.receive.Record(ctx, 0.3, g)
	}
	for range 90 {
		r.pipeline.Record(ctx, 22, g)
	}
	for range 10 {
		r.pipeline.Record(ctx, 45, g)
	}
	r.dave.Add(ctx, 990, metric.WithAttributes(attribute.String("bot_id", "1"), attribute.String("outcome", "success")))
	r.dave.Add(ctx, 10, metric.WithAttributes(attribute.String("bot_id", "1"), attribute.String("outcome", "failure")))
	after := r.snap(t, t0.Add(90*time.Minute))

	rep := Compute("42", "one_caller", 1, before, after)
	if rep.FramesReceived != 1000 {
		t.Errorf("FramesReceived = %d, want 1000", rep.FramesReceived)
	}
	if rep.FramesDropped != 5 || rep.DropsByPath["provider"] != 3 || rep.DropsByPath["receiver"] != 2 {
		t.Errorf("drops = %d %v, want 5 {provider:3 receiver:2}", rep.FramesDropped, rep.DropsByPath)
	}
	if rep.DropPct < 0.49 || rep.DropPct > 0.51 {
		t.Errorf("DropPct = %f, want 0.5", rep.DropPct)
	}
	if rep.LatencySamples != 100 || rep.LatencyP50Ms < 20 || rep.LatencyP50Ms > 25 || rep.LatencyP95Ms < 40 || rep.LatencyP95Ms > 50 {
		t.Errorf("latency p50=%f p95=%f n=%d", rep.LatencyP50Ms, rep.LatencyP95Ms, rep.LatencySamples)
	}
	if rep.DaveFailPct < 0.99 || rep.DaveFailPct > 1.01 {
		t.Errorf("DaveFailPct = %f, want 1", rep.DaveFailPct)
	}
	if rep.DaveFailReasons["failure:"] != 10 {
		t.Errorf("DaveFailReasons = %v", rep.DaveFailReasons)
	}
	if rep.Verdict != LevelWatch {
		t.Errorf("Verdict = %s, want watch (DAVE at 1%%): %+v", rep.Verdict, rep.Findings)
	}
	if rep.DurationSec != 5400 {
		t.Errorf("DurationSec = %f", rep.DurationSec)
	}
}

func TestComputeCleanRaidIsOK(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	t0 := time.Now()
	before := r.snap(t, t0)
	for range 500 {
		r.receive.Record(ctx, 0.3, guildAttr("1"))
	}
	r.dave.Add(ctx, 500, metric.WithAttributes(attribute.String("outcome", "success")))
	rep := Compute("1", "one_caller", 2, before, r.snap(t, t0.Add(time.Minute)))
	if rep.Verdict != LevelOK || len(rep.Findings) != 0 {
		t.Fatalf("clean raid graded %s %+v", rep.Verdict, rep.Findings)
	}
	if rep.LatencySamples != 0 {
		t.Fatal("direct relay must not report a latency")
	}
}

func TestComputeNoAudioAndReconnects(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	t0 := time.Now()
	before := r.snap(t, t0)
	r.reconn.Add(ctx, 2, metric.WithAttributes(attribute.String("bot_id", "5")))
	after := r.snap(t, t0.Add(time.Minute))
	after.Errors = 1
	rep := Compute("1", "one_caller", 2, before, after)
	codes := map[string]Level{}
	for _, f := range rep.Findings {
		codes[f.Code] = f.Level
	}
	if codes["no_audio"] != LevelWatch || codes["reconnects"] != LevelWatch || codes["errors"] != LevelWatch {
		t.Fatalf("findings %+v", rep.Findings)
	}
	if rep.Reconnects != 2 {
		t.Fatalf("Reconnects = %d", rep.Reconnects)
	}
}

func TestQuantileOverflowBucket(t *testing.T) {
	h := &histogram{bounds: []float64{10, 20}, buckets: []uint64{0, 0, 5}, count: 5}
	if q := h.quantile(0.95); q != 20 {
		t.Fatalf("overflow quantile = %f, want 20", q)
	}
}

func TestRenderFrench(t *testing.T) {
	rep := Report{
		Mode: "one_caller", DurationSec: 3725, FramesReceived: 6000, VoiceSec: 120,
		FramesDropped: 3, DropPct: 0.05, DaveSuccess: 6000, Verdict: LevelWatch,
		Findings: []Finding{{Code: "reconnects", Level: LevelWatch}},
	}
	out := Render(rep, "fr")
	for _, want := range []string{"**Rapport Voice Relay** : à surveiller", "1 h 02 min", "un caller", "0,05 %", "relais direct", "100,00 % déchiffré", "s'est reconnecté"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(Render(rep, "en"), "Rapport") {
		t.Error("English render must not be French")
	}
}

func TestSavePrunes(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range keepReports + 3 {
		if _, err := Save(dir, Report{GuildID: "1", End: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != keepReports {
		t.Fatalf("kept %d reports, want %d", len(entries), keepReports)
	}
	if _, err := os.Stat(filepath.Join(dir, "rvr-20260101-000000-1.json")); !os.IsNotExist(err) {
		t.Fatal("oldest report must be pruned")
	}
}

func TestDropPctIsPerOutput(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	t0 := time.Now()
	before := r.snap(t, t0)
	for range 1000 {
		r.receive.Record(ctx, 0.3, guildAttr("1"))
	}
	r.dropped.Add(ctx, 20, metric.WithAttributes(attribute.String("guild_id", "1"), attribute.String("path", "direct")))
	rep := Compute("1", "one_caller", 10, before, r.snap(t, t0.Add(time.Minute)))
	if rep.DropPct < 0.199 || rep.DropPct > 0.201 {
		t.Fatalf("DropPct = %f, want 0.2 (20 lost over 1000 frames x 10 outputs)", rep.DropPct)
	}
	if rep.Verdict != LevelOK {
		t.Fatalf("0.2%% loss graded %s", rep.Verdict)
	}
}

func TestAttachDiagFindingsAndRender(t *testing.T) {
	rep := Report{Mode: "one_caller", DurationSec: 600, FramesReceived: 3000, VoiceSec: 60, Verdict: LevelOK}
	sum := diag.Summary{
		DurationSec: 600,
		Sources: []diag.SourceTotal{
			{UserID: "1", Packets: 3000, Lost: 120, LostPct: 3.85, ReorderPct: 0.2, MaxJitterMs: 35},
		},
		Outputs: []diag.OutputTotal{
			{BotID: "10", Relayed: 3000, Stalls60: 30},
			{BotID: "11", Relayed: 3000, Stalls60: 10},
		},
		DistinctVoiceServers: 3,
		DistinctRegions:      2,
		MixSeconds:           42,
		MixSwitches:          3,
		OverlapSeconds:       12.4,
		MaxSimultaneous:      3,
		CPUMaxPct:            35,
		CPUAvgPct:            12,
		File:                 "/data/diagnostics/rvr-20261014-200000-1.jsonl",
	}
	rep.AttachDiag(sum)
	got := map[string]Level{}
	for _, f := range rep.Findings {
		got[f.Code] = f.Level
	}
	want := map[string]Level{"network_loss": LevelWatch, "jitter": LevelWatch, "stalls": LevelWatch, "voice_servers": LevelWatch}
	for code, lvl := range want {
		if got[code] != lvl {
			t.Errorf("finding %s = %q, want %q (all: %+v)", code, got[code], lvl, rep.Findings)
		}
	}
	if _, ok := got["cpu"]; ok {
		t.Error("35% CPU must not be flagged")
	}
	if rep.Verdict != LevelWatch || rep.Diag == nil {
		t.Fatalf("verdict %s diag %v", rep.Verdict, rep.Diag)
	}
	out := Render(rep, "fr")
	for _, w := range []string{"3,85 % perdus", "40 micro-coupure(s) sur 2 salon(s)", "42 s (3 fois)", "Voix superposées : 12 s, jusqu'à 3 en même temps", "Serveurs vocaux Discord : 3, région(s) : 2", "rvr-20261014-200000-1.jsonl", "même région"} {
		if !strings.Contains(out, w) {
			t.Errorf("render missing %q:\n%s", w, out)
		}
	}
}

func TestAttachDiagEmptyIsIgnored(t *testing.T) {
	rep := Report{Verdict: LevelOK}
	rep.AttachDiag(diag.Summary{})
	if rep.Diag != nil || len(rep.Findings) != 0 {
		t.Fatalf("empty summary attached: %+v", rep)
	}
}

func TestRenderEventLabel(t *testing.T) {
	r := Report{GuildID: "1", Mode: "one_caller", DurationSec: 90, Verdict: LevelOK}
	for _, lang := range []string{"fr", "en"} {
		if def, lab := Render(r, lang), RenderWithLabel(r, lang, ""); def != lab {
			t.Fatalf("%s: empty label changed the report:\n%s\n%s", lang, def, lab)
		}
		if got := RenderWithLabel(r, lang, "GvG"); strings.Contains(got, "Voice Relay") || !strings.Contains(got, "GvG") {
			t.Fatalf("%s: %q", lang, got)
		}
	}
	if !strings.HasPrefix(Render(r, "fr"), "**Rapport Voice Relay** :") {
		t.Fatalf("default title changed: %q", Render(r, "fr"))
	}
}

// 2026-11-13: relay bots stayed silent in their channels for a whole session
// and the report blamed "high audio loss". A relay the session watcher saw
// unable to encrypt while people listened is named as such.
func TestSilentRelayFinding(t *testing.T) {
	var r Report
	r.Verdict = LevelOK
	r.AttachDiag(diag.Summary{DurationSec: 600, Events: map[string]int{"silent_relay": 1}})
	found := false
	for _, f := range r.Findings {
		if f.Code == "silent_relay" && f.Level == LevelBad {
			found = true
		}
	}
	if !found || r.Verdict != LevelBad {
		t.Fatalf("findings = %+v, verdict %s", r.Findings, r.Verdict)
	}
	for _, lang := range []string{"fr", "en"} {
		if out := Render(r, lang); !strings.Contains(out, "DAVE") {
			t.Errorf("%s render misses the silent relay text:\n%s", lang, out)
		}
	}
}
