package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/classsort"
	"github.com/TepqeT/voice-relay/internal/diag"
)

// Class sorting: players take one class role and join a waiting voice
// channel; during a raid the owner bot moves them into the squads following
// the raid leads' composition rule (see internal/classsort). Players with no
// or several class roles, and squad leads, stay in the waiting channel and
// are listed for the raid leads.

var (
	// ErrClassSortDisabled: CLASS_ROLES or WAITING_CHANNEL is not set.
	ErrClassSortDisabled = errors.New("class sorting is not configured")
	// ErrWaitingChannelMissing: the waiting channel cannot be found.
	ErrWaitingChannelMissing = errors.New("waiting channel not found")
	// ErrNoSquadChannels: the running raid has no squad channel.
	ErrNoSquadChannels = errors.New("the raid has no squad channel")
)

// sortDebounce batches the players joining the waiting channel together.
const sortDebounce = 2 * time.Second

// ClassSortOptions configures class sorting. Empty ClassRoles or Waiting
// disables it.
type ClassSortOptions struct {
	ClassRoles []string       // class roles, names or IDs
	Waiting    string         // waiting voice channel, name or ID (created by name with dynamic channels)
	Rule       classsort.Rule // composition rule
	RuleText   string         // CLASS_RULE as written, for logs
}

type sortState struct {
	opts ClassSortOptions

	mu     sync.Mutex
	timers map[snowflake.ID]*time.Timer
	run    sync.Map // guildID -> *sync.Mutex, one sort at a time per guild

	// move is the REST call that moves a member; replaced in tests.
	move func(ctx context.Context, guildID, userID, channelID snowflake.ID) error
}

// SortWaiting is one player left in the waiting channel.
type SortWaiting struct {
	UserID snowflake.ID
	Reason classsort.Reason
}

// SortReport tells the raid leads what a sort did.
type SortReport struct {
	Squads           int
	Moved            int
	Failed           int
	PermissionDenied bool // Discord refused a move (50013): Move Members missing
	Waiting          []SortWaiting
}

// ConfigureClassSort enables class sorting. Call once at startup, after
// ConfigureChannels.
func (m *Service) ConfigureClassSort(opts ClassSortOptions) {
	m.sort.opts = opts
	m.sort.timers = make(map[snowflake.ID]*time.Timer)
	if m.sort.move == nil {
		m.sort.move = m.moveMember
	}
	if !m.ClassSortEnabled() {
		return
	}
	slog.Info("class sorting enabled",
		slog.Any("classRoles", opts.ClassRoles),
		slog.String("waitingChannel", opts.Waiting),
		slog.String("rule", opts.RuleText),
		slog.Bool("dynamicChannels", m.channels.opts.Enabled),
	)
	known := make(map[string]bool, len(opts.ClassRoles))
	for _, c := range opts.ClassRoles {
		known[classsort.Normalize(c)] = true
	}
	for _, c := range opts.Rule.Classes() {
		if !known[c] {
			slog.Warn("CLASS_RULE names a class missing from CLASS_ROLES", slog.String("class", c))
		}
	}
}

// ClassSortEnabled reports whether class sorting is configured.
func (m *Service) ClassSortEnabled() bool {
	return len(m.sort.opts.ClassRoles) > 0 && strings.TrimSpace(m.sort.opts.Waiting) != ""
}

// ClassSortWaiting returns the configured waiting channel name.
func (m *Service) ClassSortWaiting() string { return m.sort.opts.Waiting }

// CanSortPlayers reports whether a member may run the sort: server managers,
// otherwise the speaker role.
func (m *Service) CanSortPlayers(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool {
	if !m.ClassSortEnabled() {
		return false
	}
	if perms.Has(discord.PermissionAdministrator) || perms.Has(discord.PermissionManageGuild) {
		return true
	}
	caller, ok := m.callerRole(guildID)
	return ok && slices.Contains(roleIDs, caller)
}

// OnVoiceChannelJoin schedules a sort when a member enters the waiting
// channel of a running raid. Players arriving together are sorted at once.
func (m *Service) OnVoiceChannelJoin(guildID, channelID snowflake.ID) {
	if !m.ClassSortEnabled() || !m.HasActiveSession(guildID) {
		return
	}
	if w, ok := m.waitingChannel(guildID); !ok || w != channelID {
		return
	}
	m.sort.mu.Lock()
	defer m.sort.mu.Unlock()
	if t, ok := m.sort.timers[guildID]; ok {
		t.Reset(sortDebounce)
		return
	}
	m.sort.timers[guildID] = time.AfterFunc(sortDebounce, func() {
		m.sort.mu.Lock()
		delete(m.sort.timers, guildID)
		m.sort.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := m.SortPlayers(ctx, guildID); err != nil {
			slog.Warn("automatic class sort skipped", slog.String("guildID", guildID.String()), slog.Any("err", err))
		}
	})
}

// SortPlayers moves the players of the waiting channel into the squads of the
// running raid and reports what happened.
func (m *Service) SortPlayers(ctx context.Context, guildID snowflake.ID) (SortReport, error) {
	if !m.ClassSortEnabled() {
		return SortReport{}, ErrClassSortDisabled
	}
	if !m.HasActiveSession(guildID) {
		return SortReport{}, ErrNoActiveSession
	}
	lock, _ := m.sort.run.LoadOrStore(guildID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	waitingID, ok := m.waitingChannel(guildID)
	if !ok {
		return SortReport{}, fmt.Errorf("%w: %q", ErrWaitingChannelMissing, m.sort.opts.Waiting)
	}
	squadIDs := m.squadChannels(guildID)
	if len(squadIDs) == 0 {
		return SortReport{}, ErrNoSquadChannels
	}

	classes := m.classRoles(guildID)
	squadRole := m.resolveSquadRole(guildID)
	byChannel := m.voicePlayers(guildID, classes, squadRole)
	waiting := byChannel[waitingID]
	squads := make([][]classsort.Player, len(squadIDs))
	for i, id := range squadIDs {
		squads[i] = byChannel[id]
	}
	plan := classsort.Plan(waiting, squads, m.sort.opts.Rule)

	rep := SortReport{Squads: len(squadIDs)}
	for _, mv := range plan.Moves {
		uid, err := snowflake.Parse(mv.PlayerID)
		if err != nil {
			continue
		}
		if err := m.sort.move(ctx, guildID, uid, squadIDs[mv.Squad]); err != nil {
			rep.Failed++
			if isPermissionError(err) {
				rep.PermissionDenied = true
			}
			slog.Warn("class sort: move refused",
				slog.String("guildID", guildID.String()),
				slog.String("userID", uid.String()),
				slog.String("channelID", squadIDs[mv.Squad].String()),
				slog.Any("err", err))
			continue
		}
		rep.Moved++
	}
	for _, w := range plan.Waiting {
		if uid, err := snowflake.Parse(w.PlayerID); err == nil {
			rep.Waiting = append(rep.Waiting, SortWaiting{UserID: uid, Reason: w.Reason})
		}
	}
	diag.For(guildID).Event("class_sort", map[string]any{
		"moved": rep.Moved, "failed": rep.Failed, "waiting": len(rep.Waiting), "squads": rep.Squads,
	})
	slog.Info("class sort done",
		slog.String("guildID", guildID.String()),
		slog.Int("moved", rep.Moved),
		slog.Int("failed", rep.Failed),
		slog.Int("waiting", len(rep.Waiting)),
		slog.Int("squads", rep.Squads))
	return rep, nil
}

func (m *Service) moveMember(ctx context.Context, guildID, userID, channelID snowflake.ID) error {
	_, err := m.ownerClient.Rest.UpdateMember(guildID, userID, discord.MemberUpdate{ChannelID: &channelID},
		rest.WithCtx(ctx), rest.WithReason("Voice Relay class sort"))
	return err
}

// waitingChannel returns the waiting channel of guildID: the one created for
// the raid with dynamic channels, else the configured one by ID or name.
func (m *Service) waitingChannel(guildID snowflake.ID) (snowflake.ID, bool) {
	if m.channels.opts.Enabled {
		rc, ok := m.RaidChannelsOf(guildID)
		return rc.Waiting, ok && rc.Waiting != 0
	}
	want := strings.TrimSpace(strings.TrimPrefix(m.sort.opts.Waiting, "#"))
	var chans []discord.GuildChannel
	for ch := range m.ownerClient.Caches.ChannelsForGuild(guildID) {
		if ch.Type() == discord.ChannelTypeGuildVoice || ch.Type() == discord.ChannelTypeGuildStageVoice {
			chans = append(chans, ch)
		}
	}
	id := findChannel(chans, want)
	return id, id != 0
}

// findChannel returns the channel whose ID or name (case-insensitive) is want.
func findChannel(chans []discord.GuildChannel, want string) snowflake.ID {
	if id, err := snowflake.Parse(want); err == nil {
		for _, c := range chans {
			if c.ID() == id {
				return id
			}
		}
	}
	for _, c := range chans {
		if strings.EqualFold(strings.TrimSpace(c.Name()), want) {
			return c.ID()
		}
	}
	return 0
}

// squadChannels returns the squad channels of the running raid, in order.
func (m *Service) squadChannels(guildID snowflake.ID) []snowflake.ID {
	if m.channels.opts.Enabled {
		if rc, ok := m.RaidChannelsOf(guildID); ok {
			return slices.Clone(rc.Squads)
		}
		return nil
	}
	m.mu.RLock()
	st := m.statuses[guildID]
	var bots []snowflake.ID
	if st != nil && st.Session != nil {
		for _, sp := range st.Session.Speakers {
			bots = append(bots, sp.ID)
		}
	}
	m.mu.RUnlock()
	raidLead, _ := m.GetBoundChannel(guildID, m.ownerBotID)
	var out []snowflake.ID
	for _, id := range bots {
		if id == m.ownerBotID {
			continue
		}
		ch, ok := m.GetBoundChannel(guildID, id)
		if ok && ch != raidLead && !slices.Contains(out, ch) {
			out = append(out, ch)
		}
	}
	return out
}

// classRoles maps each resolved class role to its class name: the role name,
// so CLASS_RULE quotas match whether CLASS_ROLES lists names or IDs.
func (m *Service) classRoles(guildID snowflake.ID) map[snowflake.ID]string {
	out := make(map[snowflake.ID]string, len(m.sort.opts.ClassRoles))
	for _, want := range m.sort.opts.ClassRoles {
		id := m.resolveRole(guildID, want)
		if id == 0 {
			slog.Warn("class role not found", slog.String("guildID", guildID.String()), slog.String("role", want))
			continue
		}
		name := want
		if r, ok := m.ownerClient.Caches.Role(guildID, id); ok {
			name = r.Name
		}
		out[id] = name
	}
	return out
}

// voicePlayers groups the human members in voice by channel.
func (m *Service) voicePlayers(guildID snowflake.ID, classes map[snowflake.ID]string, squadRole snowflake.ID) map[snowflake.ID][]classsort.Player {
	var states []memberVoice
	for vs := range m.ownerClient.Caches.VoiceStates(guildID) {
		if vs.ChannelID == nil {
			continue
		}
		mem, ok := m.ownerClient.Caches.Member(guildID, vs.UserID)
		if !ok || mem.User.Bot {
			continue
		}
		states = append(states, memberVoice{UserID: vs.UserID, ChannelID: *vs.ChannelID, Roles: mem.RoleIDs})
	}
	return groupPlayers(states, classes, squadRole)
}

// memberVoice is one human member in a voice channel.
type memberVoice struct {
	UserID    snowflake.ID
	ChannelID snowflake.ID
	Roles     []snowflake.ID
}

// groupPlayers turns voice members into sort players by channel, sorted by
// user ID so a sort is reproducible.
func groupPlayers(states []memberVoice, classes map[snowflake.ID]string, squadRole snowflake.ID) map[snowflake.ID][]classsort.Player {
	slices.SortFunc(states, func(a, b memberVoice) int { return cmp.Compare(a.UserID, b.UserID) })
	out := make(map[snowflake.ID][]classsort.Player)
	for _, s := range states {
		p := classsort.Player{ID: s.UserID.String(), SquadLead: squadRole != 0 && slices.Contains(s.Roles, squadRole)}
		for _, r := range s.Roles {
			if c, ok := classes[r]; ok {
				p.Classes = append(p.Classes, c)
			}
		}
		out[s.ChannelID] = append(out[s.ChannelID], p)
	}
	return out
}
