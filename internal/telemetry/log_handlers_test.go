package telemetry

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestFanoutAndCounting(t *testing.T) {
	var a, b bytes.Buffer
	h := newCountingHandler(fanoutHandler{
		slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn}),
	})
	w0, e0 := LogCounts()
	log := slog.New(h).With("bot", "x")
	log.Info("hello")
	log.Warn("careful")
	log.Error("broken")
	log.Debug("hidden")

	if !strings.Contains(a.String(), "hello") || !strings.Contains(a.String(), "broken") || strings.Contains(a.String(), "hidden") {
		t.Errorf("info handler got %q", a.String())
	}
	if strings.Contains(b.String(), "hello") || !strings.Contains(b.String(), "careful") || !strings.Contains(b.String(), "bot=x") {
		t.Errorf("warn handler got %q", b.String())
	}
	w1, e1 := LogCounts()
	if w1-w0 != 1 || e1-e0 != 1 {
		t.Errorf("counts warn=%d err=%d, want 1 1", w1-w0, e1-e0)
	}
}
