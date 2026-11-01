package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/TepqeT/voice-relay/internal/diag"
)

// Process-wide count of warning and error records, read by the end-of-raid
// quality report to tell whether the logs deserve a look.
var (
	warnRecords  atomic.Int64
	errorRecords atomic.Int64
)

// LogCounts returns how many warning and error records were logged since start.
func LogCounts() (warnings, errs int64) {
	return warnRecords.Load(), errorRecords.Load()
}

// countingHandler counts the warning and error records it lets through.
type countingHandler struct{ slog.Handler }

func newCountingHandler(h slog.Handler) slog.Handler { return countingHandler{h} }

func (h countingHandler) Handle(ctx context.Context, r slog.Record) error {
	switch {
	case r.Level >= slog.LevelError:
		errorRecords.Add(1)
	case r.Level >= slog.LevelWarn:
		warnRecords.Add(1)
	}
	if r.Level >= slog.LevelWarn {
		// Copy into the raid diagnostics so a warning sits next to the audio
		// figures of the same moment.
		diag.LogRecord(r.Level, r.Message)
	}
	return h.Handler.Handle(ctx, r)
}

func (h countingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return countingHandler{h.Handler.WithAttrs(attrs)}
}

func (h countingHandler) WithGroup(name string) slog.Handler {
	return countingHandler{h.Handler.WithGroup(name)}
}

// fanoutHandler sends every record to each handler that accepts its level.
type fanoutHandler []slog.Handler

func (f fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
