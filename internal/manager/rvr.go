package manager

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/announce"
	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/opus"
	"github.com/TepqeT/voice-relay/internal/quality"
)

var (
	// ErrAnnouncementsDisabled is returned when no sound was loaded.
	ErrAnnouncementsDisabled = errors.New("announcements disabled: no sound loaded")
	// ErrUnknownSound is returned for a sound name that was not loaded.
	ErrUnknownSound = errors.New("unknown sound")
	// ErrNoAnnounceTarget is returned when no bot of the raid can play sound.
	ErrNoAnnounceTarget = errors.New("no speaker can play the announcement")
)

// maxOutroWait bounds how long /stop waits for the end-of-raid sound.
const maxOutroWait = 5 * time.Second

// RvROptions configures the RvR additions: announcements and quality reports.
type RvROptions struct {
	// Sounds is the announcement library; nil or empty disables announcements.
	Sounds *announce.Library
	// SoundOnStart is played in every squad channel when a raid starts ("" = none).
	SoundOnStart string
	// SoundOnStop is played before the bots leave on /stop ("" = none).
	SoundOnStop string
	// ReportsDir receives one JSON quality report per raid ("" = not saved).
	ReportsDir string
	// SquadLeadRole names the role (name or ID) whose members can call the
	// raid leads from their squad channel ("" = disabled).
	SquadLeadRole string
	// SquadLeadKey is SquadKeyPriority or SquadKeyAlways.
	SquadLeadKey string
	// SquadLeadsToSquads relays squad leads to the other squads as well.
	SquadLeadsToSquads bool
	// RaidLeadRole names the raid lead role (name or ID), used when no capture
	// role was bound with /setup ("" = none).
	RaidLeadRole string
}

type reportStart struct {
	snap    quality.Snapshot
	mode    string
	outputs int
}

type rvrState struct {
	opts     RvROptions
	registry *announce.Registry
	sink     func(quality.Report)

	mu     sync.Mutex
	starts map[snowflake.ID]reportStart
}

// ConfigureRvR applies opts. Call once at startup, before any raid.
func (m *Service) ConfigureRvR(opts RvROptions) {
	m.rvr.opts = opts
	m.rvr.registry = announce.NewRegistry()
	m.rvr.starts = make(map[snowflake.ID]reportStart)
	for _, auto := range []struct{ setting, name string }{
		{"SOUND_ON_START", opts.SoundOnStart},
		{"SOUND_ON_STOP", opts.SoundOnStop},
	} {
		if auto.name == "" {
			continue
		}
		if _, ok := opts.Sounds.Get(auto.name); !ok {
			slog.Warn("automatic sound not found, it will not play",
				slog.String("setting", auto.setting), slog.String("sound", auto.name), slog.Any("available", opts.Sounds.Names()))
		}
	}
	if s, ok := opts.Sounds.Get(opts.SoundOnStop); ok && s.Duration+300*time.Millisecond > maxOutroWait {
		slog.Warn("end-of-raid sound longer than the stop wait, it will be cut",
			slog.String("sound", s.Name), slog.Duration("duration", s.Duration), slog.Duration("maxWait", maxOutroWait))
	}
}

// SetReportSink registers fn to receive every end-of-raid quality report.
// Call once at startup.
func (m *Service) SetReportSink(fn func(quality.Report)) { m.rvr.sink = fn }

// SoundNames lists the announcements available to /announce.
func (m *Service) SoundNames() []string { return m.rvr.opts.Sounds.Names() }

// overlayFor returns the factory that attaches botID's voice provider in
// guildID to the announcement registry, or nil when announcements are off.
func (m *Service) overlayFor(guildID, botID snowflake.ID) func() opus.Overlay {
	if m.rvr.registry == nil || m.rvr.opts.Sounds.Len() == 0 {
		return nil
	}
	reg := m.rvr.registry
	return func() opus.Overlay { return reg.Attach(guildID, botID) }
}

// Announce plays the sound called name in every channel of the guild's active
// raid and returns its duration.
func (m *Service) Announce(guildID snowflake.ID, name string) (time.Duration, error) {
	if m.rvr.registry == nil || m.rvr.opts.Sounds.Len() == 0 {
		return 0, ErrAnnouncementsDisabled
	}
	sound, ok := m.rvr.opts.Sounds.Get(name)
	if !ok {
		return 0, ErrUnknownSound
	}
	if !m.HasActiveSession(guildID) {
		return 0, ErrNoActiveSession
	}
	n := m.rvr.registry.Play(guildID, sound)
	if n == 0 {
		return 0, ErrNoAnnounceTarget
	}
	diag.For(guildID).Event("announce", map[string]any{"sound": sound.Name, "bots": n})
	slog.Info("announcement played",
		slog.String("guildID", guildID.String()),
		slog.String("sound", sound.Name),
		slog.Int("bots", n),
	)
	return sound.Duration, nil
}

// playAuto plays an automatic announcement and logs instead of failing, since
// the raid itself must not depend on it.
func (m *Service) playAuto(guildID snowflake.ID, name string) time.Duration {
	if name == "" || m.rvr.opts.Sounds.Len() == 0 {
		return 0
	}
	if _, ok := m.rvr.opts.Sounds.Get(name); !ok {
		return 0
	}
	d, err := m.Announce(guildID, name)
	if err != nil {
		slog.Warn("automatic announcement skipped",
			slog.String("guildID", guildID.String()),
			slog.String("sound", name),
			slog.Any("err", err),
		)
		return 0
	}
	return d
}

// PlayOutro plays the end-of-raid sound, if any, and waits for it to finish so
// the bots do not leave mid-sound. Bounded by maxOutroWait.
func (m *Service) PlayOutro(guildID snowflake.ID) {
	d := m.playAuto(guildID, m.rvr.opts.SoundOnStop)
	if d <= 0 {
		return
	}
	wait := d + 300*time.Millisecond
	if wait > maxOutroWait {
		wait = maxOutroWait
	}
	time.Sleep(wait)
}

// beginReport snapshots the instruments when a raid starts. speakers counts
// every bot in the raid, the owner included; in one-caller mode the owner only
// listens, so it is not an output.
func (m *Service) beginReport(ctx context.Context, guildID snowflake.ID, mode guild.RaidMode, speakers int) {
	if m.rvr.starts == nil {
		return
	}
	outputs := speakers
	if !mode.WithCapture() {
		outputs--
	}
	snap := quality.Take(ctx)
	m.rvr.mu.Lock()
	m.rvr.starts[guildID] = reportStart{snap: snap, mode: string(mode), outputs: outputs}
	m.rvr.mu.Unlock()
	diag.For(guildID).Begin(string(mode), speakers)
}

// finishReport builds, logs, saves and publishes the raid's quality report.
// It runs in the background: a slow disk or Discord API never delays /stop.
func (m *Service) finishReport(guildID snowflake.ID) {
	if m.rvr.starts == nil {
		return
	}
	// Providers rebuilt by reconnects are not always closed explicitly, so
	// forget the guild's announcement slots now that its bots have left.
	m.rvr.registry.ResetGuild(guildID)
	m.squad.roles.Delete(guildID)
	m.rvr.mu.Lock()
	start, ok := m.rvr.starts[guildID]
	delete(m.rvr.starts, guildID)
	m.rvr.mu.Unlock()
	if !ok {
		return
	}
	// Ended here, not in the goroutine: a raid restarted right away would
	// otherwise begin a new recording before this one is summarised.
	sum := diag.For(guildID).End()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("quality report panicked", slog.Any("panic", r))
			}
		}()
		after := quality.Take(context.Background())
		rep := quality.Compute(guildID.String(), start.mode, start.outputs, start.snap, after)
		rep.AttachDiag(sum)
		slog.Info("rvr quality report", rep.LogAttrs()...)
		if path, err := quality.Save(m.rvr.opts.ReportsDir, rep); err != nil {
			slog.Warn("failed to save quality report", slog.Any("err", err))
		} else if path != "" {
			slog.Info("quality report saved", slog.String("path", path))
		}
		if m.rvr.sink != nil {
			m.rvr.sink(rep)
		}
	}()
}
