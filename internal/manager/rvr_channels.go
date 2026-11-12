package manager

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/guild"
)

// Dynamic channels: when enabled, starting a session creates the speaker
// channel and one listener channel per relay bot (restricted to the access
// role when one is set and the mode is restricted), binds the bots to them,
// and stopping the session deletes them.

var (
	// ErrRaidLeadRoleMissing is returned when no speaker role is bound and
	// SPEAKER_ROLE matches no role: the speaker channel would be open to
	// everyone.
	ErrRaidLeadRoleMissing = errors.New("raid lead role not found")
	// ErrChannelPermission is returned when Discord refuses to create the raid
	// channels, usually because the owner bot lacks Manage Channels.
	ErrChannelPermission = errors.New("the owner bot cannot create the raid channels (Manage Channels permission)")
)

// ChannelOptions configures the dynamic raid channels.
type ChannelOptions struct {
	Enabled       bool
	Category      string // category name
	After         string // existing category (name or ID) the raid category goes right below; "" = Discord default (bottom)
	RaidLeadName  string // raid lead channel name
	SquadName     string // squad channel prefix, numbered from 1
	SquadCount    int    // wanted squads, capped by the enabled speaker bots
	Region        string // Discord voice region of every channel, "" = automatic
	StateFile     string // where the created channel IDs are remembered
	AccessRole    string // role (name or ID) that sees and joins the channels when restricted; "" = everyone
	BoardRole     string // role (name or ID) allowed to grant and revoke AccessRole
	AccessChannel string // text channel (name or ID) where the board grants access by message
	// HomeCategory keeps one permanent category (Category) holding the panel
	// channel and the raid voice channels: only the voice channels come and
	// go with each raid.
	HomeCategory bool
}

// RaidChannels are the channels created for one raid.
type RaidChannels struct {
	Category snowflake.ID   `json:"category"`
	RaidLead snowflake.ID   `json:"raid_lead"`
	Squads   []snowflake.ID `json:"squads"`
	// Waiting is the class sorting waiting channel, 0 when sorting is off.
	Waiting snowflake.ID `json:"waiting,omitempty"`
	Created time.Time    `json:"created"`
	// CallerRole is the raid lead role the channels were created for: channels
	// made for another role (or none) are never reused.
	CallerRole snowflake.ID `json:"caller_role,omitempty"`
	// ViewRole is the role that could see the channels (access role, 0 = everyone).
	ViewRole snowflake.ID `json:"view_role,omitempty"`
	// SharedCategory: Category is the permanent home category, never
	// deleted with the raid.
	SharedCategory bool `json:"shared_category,omitempty"`
}

func (r RaidChannels) all() []snowflake.ID {
	ids := make([]snowflake.ID, 0, len(r.Squads)+3)
	ids = append(ids, r.Squads...)
	if r.Waiting != 0 {
		ids = append(ids, r.Waiting)
	}
	if r.RaidLead != 0 {
		ids = append(ids, r.RaidLead)
	}
	if r.Category != 0 && !r.SharedCategory {
		ids = append(ids, r.Category) // last, once it is empty
	}
	return ids
}

type channelState struct {
	opts ChannelOptions
	keep atomic.Bool // set on shutdown: stopping raids keeps their channels
	op   sync.Mutex  // serialises create and delete REST sequences
	mu   sync.Mutex  // guards byGuild and the state file
	// byGuild holds the channels of each guild still present on Discord.
	byGuild map[snowflake.ID]RaidChannels
}

// ConfigureChannels enables dynamic raid channels and loads the channels left
// by a previous run, so they are reused or deleted rather than forgotten.
func (m *Service) ConfigureChannels(opts ChannelOptions) {
	opts.SquadCount = max(opts.SquadCount, 1)
	m.channels.opts = opts
	m.channels.byGuild = loadChannelState(opts.StateFile)
	if !opts.Enabled {
		return
	}
	slog.Info("dynamic channels enabled",
		slog.String("accessRole", opts.AccessRole),
		slog.String("boardRole", opts.BoardRole),
		slog.String("category", opts.Category),
		slog.Int("squads", opts.SquadCount),
		slog.String("region", opts.Region),
		slog.Int("leftovers", len(m.channels.byGuild)),
	)
	if opts.AccessRole == "" {
		slog.Info("no ACCESS_ROLE: the channels are open to everyone")
	}
}

// DynamicChannels reports whether raids create their own channels.
func (m *Service) DynamicChannels() bool { return m.channels.opts.Enabled }

// ChannelCategory returns the name of the category created for each raid.
func (m *Service) ChannelCategory() string { return m.channels.opts.Category }

// RaidChannelsOf returns the channels currently created for guildID.
func (m *Service) RaidChannelsOf(guildID snowflake.ID) (RaidChannels, bool) {
	m.channels.mu.Lock()
	defer m.channels.mu.Unlock()
	rc, ok := m.channels.byGuild[guildID]
	return rc, ok
}

// resolveRole finds a role of guildID by ID or by name (case-insensitive).
func (m *Service) resolveRole(guildID snowflake.ID, want string) snowflake.ID {
	want = strings.TrimSpace(want)
	if want == "" {
		return 0
	}
	if id, err := snowflake.Parse(want); err == nil {
		if _, ok := m.ownerClient.Caches.Role(guildID, id); ok {
			return id
		}
	}
	for _, r := range slices.Collect(m.ownerClient.Caches.Roles(guildID)) {
		if strings.EqualFold(strings.TrimSpace(r.Name), want) {
			return r.ID
		}
	}
	return 0
}

// prepareRaidChannels gives guildID a raid lead channel and one channel per
// squad, and binds the owner bot and the squad bots to them. Channels left by
// a previous raid are reused when they are all still there with the right
// count, so members already sitting in them are not kicked; otherwise they are
// deleted and created again. Must run under the starting guard.
func (m *Service) prepareRaidChannels(ctx context.Context, guildID snowflake.ID) (RaidChannels, error) {
	c := &m.channels
	c.op.Lock()
	defer c.op.Unlock()

	// Errors on an active raid before touching anything: its channels are in
	// the state too.
	speakers, err := m.speakersAfterSeeding(guildID)
	if err != nil {
		return RaidChannels{}, err
	}
	speakers = pickSquadSpeakers(speakers, m.squadLimit(guildID))
	if len(speakers) == 0 {
		return RaidChannels{}, ErrNoBoundSpeakers
	}

	// The role that sees and joins the channels: the vetted access role when
	// configured and the mode is restricted, else everyone.
	viewName := m.viewRoleName(guildID)
	var viewRole snowflake.ID
	if viewName != "" {
		if viewRole = m.resolveRole(guildID, viewName); viewRole == 0 {
			return RaidChannels{}, fmt.Errorf("%w: %q", ErrAccessRoleMissing, viewName)
		}
	}

	// Only the raid lead role may join the raid lead channel: without it the
	// channel would be open to everyone, so refuse to start.
	callerRole, ok := m.callerRole(guildID)
	if !ok {
		return RaidChannels{}, fmt.Errorf("%w: %q", ErrRaidLeadRoleMissing, m.rvr.opts.RaidLeadRole)
	}

	rc, reused := m.reusableChannels(guildID, len(speakers), callerRole, viewRole)
	if !reused {
		if old, ok := m.RaidChannelsOf(guildID); ok {
			m.deleteRaidChannels(ctx, guildID, old, "leftover")
		}
		rc, err = m.createRaidChannels(ctx, guildID, viewRole, callerRole, speakers)
		if err != nil {
			return RaidChannels{}, err
		}
	}

	m.BindChannel(guildID, m.ownerBotID, rc.RaidLead)
	picked := make(map[snowflake.ID]bool, len(speakers))
	for i, sp := range speakers {
		m.BindChannel(guildID, sp.ID, rc.Squads[i])
		picked[sp.ID] = true
	}
	// Speakers left out must not try to join a channel of an earlier raid.
	all, _ := m.snapshotSpeakers(guildID)
	for _, sp := range all {
		if !picked[sp.ID] {
			m.UnbindChannel(guildID, sp.ID)
		}
	}
	diag.For(guildID).Event("raid_channels", map[string]any{
		"reused":   reused,
		"squads":   len(rc.Squads),
		"category": rc.Category.String(),
	})
	slog.Info("raid channels ready",
		slog.String("guildID", guildID.String()),
		slog.Bool("reused", reused),
		slog.Int("squads", len(rc.Squads)),
		slog.String("categoryID", rc.Category.String()),
		slog.String("raidLeadID", rc.RaidLead.String()),
		slog.String("raidLeadRoleID", callerRole.String()),
	)
	return rc, nil
}

// squadLimit is how many squads this raid creates: the panel's choice when
// one is saved, else the configured maximum.
func (m *Service) squadLimit(guildID snowflake.ID) int {
	m.squadChoice.mu.Lock()
	n, ok := m.squadChoicesLocked()[guildID]
	m.squadChoice.mu.Unlock()
	if ok && n >= 1 {
		return min(n, max(m.channels.opts.SquadCount, 1))
	}
	return m.channels.opts.SquadCount
}

// reusableChannels returns the remembered channels of guildID when all of them
// still exist and they hold exactly squads squad channels.
func (m *Service) reusableChannels(guildID snowflake.ID, squads int, callerRole, viewRole snowflake.ID) (RaidChannels, bool) {
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok || len(rc.Squads) != squads || rc.RaidLead == 0 || rc.CallerRole != callerRole || rc.ViewRole != viewRole ||
		(rc.Waiting != 0) != m.ClassSortEnabled() || rc.SharedCategory != m.channels.opts.HomeCategory {
		return RaidChannels{}, false
	}
	for _, id := range rc.all() {
		if _, exists := m.ownerClient.Caches.Channel(id); !exists {
			return RaidChannels{}, false
		}
	}
	return rc, true
}

func (m *Service) createRaidChannels(ctx context.Context, guildID, viewRole, callerRole snowflake.ID, speakers []guild.Speaker) (rc RaidChannels, err error) {
	c := &m.channels
	squadRole := m.resolveSquadRole(guildID)
	var boardRole snowflake.ID
	if c.opts.BoardRole != "" {
		if boardRole = m.resolveRole(guildID, c.opts.BoardRole); boardRole == 0 {
			slog.Warn("board role not found: the board cannot moderate the raid channels",
				slog.String("guildID", guildID.String()), slog.String("role", c.opts.BoardRole))
		}
	}
	bots := make([]snowflake.ID, 0, len(speakers)+1)
	bots = append(bots, m.ownerBotID)
	for _, sp := range speakers {
		bots = append(bots, sp.ID)
	}

	// Discord only lets a bot grant, in a permission overwrite, permissions it
	// holds itself in the guild: one missing permission makes every channel
	// creation fail. Leave out what the bot cannot grant and say so.
	grant := m.grantablePermissions(guildID)
	if missing := raidBotNeeds &^ grant; missing != 0 {
		slog.Warn("owner bot lacks guild permissions: raid channels are created without them",
			slog.String("guildID", guildID.String()),
			slog.String("missing", missing.String()),
			slog.String("botPermissions", grant.String()),
		)
		diag.For(guildID).Event("owner_permissions_missing", map[string]any{"missing": missing.String()})
	}
	overwrites := func(caller, squad snowflake.ID, kind channelKind) []discord.PermissionOverwrite {
		ow := raidOverwrites(guildID, viewRole, caller, squad, bots, kind)
		return maskOverwrites(append(ow, boardOverwrites(boardRole, kind)...), grant)
	}

	rc.Created = time.Now().UTC()
	rc.CallerRole = callerRole
	rc.ViewRole = viewRole
	// Remember every channel as soon as it exists, so a failure half-way or a
	// crash never leaves untracked channels behind.
	defer func() {
		if err != nil {
			m.deleteRaidChannels(context.WithoutCancel(ctx), guildID, rc, "failed start")
			rc = RaidChannels{}
		}
	}()
	opts := []rest.RequestOpt{rest.WithCtx(ctx), rest.WithReason("Voice Relay session start")}

	if c.opts.HomeCategory {
		// The permanent category: the voice channels carry their own
		// overwrites, so nothing on the category needs to change per raid.
		home, herr := m.EnsureHomeCategory(ctx, guildID)
		if herr != nil {
			return rc, channelError("home category", herr)
		}
		rc.Category, rc.SharedCategory = home, true
	} else {
		category, cerr := m.ownerClient.Rest.CreateGuildChannel(guildID, discord.GuildCategoryChannelCreate{
			Name:                 c.opts.Category,
			PermissionOverwrites: overwrites(0, 0, channelCategory),
		}, opts...)
		if cerr != nil {
			return rc, channelError("create category", cerr)
		}
		m.ownerClient.Caches.AddChannel(category)
		rc.Category = category.ID()
		m.rememberChannels(guildID, rc)
		m.placeCategory(ctx, guildID, rc.Category)
	}

	create := func(name string, kind channelKind) (snowflake.ID, error) {
		ch, err := m.ownerClient.Rest.CreateGuildChannel(guildID, discord.GuildVoiceChannelCreate{
			Name:                 name,
			ParentID:             rc.Category,
			RTCRegion:            c.opts.Region,
			PermissionOverwrites: overwrites(callerRole, squadRole, kind),
		}, opts...)
		if err != nil {
			return 0, channelError("create "+name, err)
		}
		m.ownerClient.Caches.AddChannel(ch)
		return ch.ID(), nil
	}
	if rc.RaidLead, err = create(c.opts.RaidLeadName, channelRaidLead); err != nil {
		return rc, err
	}
	m.rememberChannels(guildID, rc)
	for i := range speakers {
		id, err := create(fmt.Sprintf("%s %d", c.opts.SquadName, i+1), channelSquad)
		if err != nil {
			return rc, err
		}
		rc.Squads = append(rc.Squads, id)
		m.rememberChannels(guildID, rc)
	}
	if m.ClassSortEnabled() {
		if rc.Waiting, err = create(m.sort.opts.Waiting, channelWaiting); err != nil {
			return rc, err
		}
		m.rememberChannels(guildID, rc)
	}
	// Servers with onboarding hide channels members did not opt into.
	// Only open channels qualify: Discord refuses a default channel
	// @everyone cannot see, and a restricted channel is shown anyway to
	// those who can.
	if viewRole == 0 {
		ids := append([]snowflake.ID{rc.RaidLead, rc.Waiting}, rc.Squads...)
		m.EnsureOnboardingDefaults(ctx, guildID, ids...)
	}
	return rc, nil
}

// placeCategory moves the raid category right below the configured one. A
// failure only leaves the category at the bottom of the list, so it is logged
// and the start goes on.
func (m *Service) placeCategory(ctx context.Context, guildID, categoryID snowflake.ID) {
	after := strings.TrimSpace(m.channels.opts.After)
	if after == "" {
		return
	}
	var cats []discord.GuildChannel
	for _, ch := range slices.Collect(m.ownerClient.Caches.ChannelsForGuild(guildID)) {
		if ch.Type() == discord.ChannelTypeGuildCategory {
			cats = append(cats, ch)
		}
	}
	anchor := findCategory(cats, after)
	if anchor == 0 {
		slog.Warn("RVR_CATEGORY_AFTER not found, raid category left at the bottom",
			slog.String("guildID", guildID.String()), slog.String("after", after))
		return
	}
	order := orderCategories(cats, anchor, categoryID)
	updates := make([]discord.GuildChannelPositionUpdate, 0, len(order))
	for i, id := range order {
		pos := i
		updates = append(updates, discord.GuildChannelPositionUpdate{ID: id, Position: omit.New(&pos)})
	}
	if err := m.ownerClient.Rest.UpdateChannelPositions(guildID, updates,
		rest.WithCtx(ctx), rest.WithReason("Voice Relay session start")); err != nil {
		slog.Warn("failed to place the raid category", slog.String("guildID", guildID.String()), slog.Any("err", channelError("move category", err)))
		return
	}
	slog.Info("raid category placed", slog.String("guildID", guildID.String()), slog.String("after", after))
}

// findCategory returns the category matching want: an ID, an exact name
// (case-insensitive), or else the first name containing want.
func findCategory(cats []discord.GuildChannel, want string) snowflake.ID {
	if id, err := snowflake.Parse(want); err == nil {
		for _, c := range cats {
			if c.ID() == id {
				return id
			}
		}
	}
	lw := strings.ToLower(want)
	for _, c := range cats {
		if strings.EqualFold(strings.TrimSpace(c.Name()), want) {
			return c.ID()
		}
	}
	for _, c := range cats {
		if strings.Contains(strings.ToLower(c.Name()), lw) {
			return c.ID()
		}
	}
	return 0
}

// orderCategories returns the categories in display order with moved placed
// right after anchor.
func orderCategories(cats []discord.GuildChannel, anchor, moved snowflake.ID) []snowflake.ID {
	sorted := slices.Clone(cats)
	slices.SortFunc(sorted, func(a, b discord.GuildChannel) int {
		return cmp.Or(cmp.Compare(a.Position(), b.Position()), cmp.Compare(a.ID(), b.ID()))
	})
	out := make([]snowflake.ID, 0, len(sorted)+1)
	for _, c := range sorted {
		if c.ID() == moved {
			continue
		}
		out = append(out, c.ID())
		if c.ID() == anchor {
			out = append(out, moved)
		}
	}
	return out
}

func channelError(what string, err error) error {
	var restErr *rest.Error
	if errors.As(err, &restErr) {
		slog.Warn("discord refused a raid channel request",
			slog.String("what", what),
			slog.Int("code", int(restErr.Code)),
			slog.String("message", restErr.Message),
			slog.String("details", string(restErr.Errors)),
		)
		if restErr.Code == rest.JSONErrorCodeLackPermissionsToPerformAction || restErr.Code == rest.JSONErrorCodeMissingAccess {
			return fmt.Errorf("%s: %w (Discord %d: %s)", what, ErrChannelPermission, restErr.Code, restErr.Message)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// raidBotNeeds are the guild permissions the owner bot needs to create the
// raid channels with every overwrite they carry.
const raidBotNeeds = discord.PermissionViewChannel | discord.PermissionManageChannels |
	discord.PermissionConnect | discord.PermissionSpeak | discord.PermissionUseVAD |
	discord.PermissionPrioritySpeaker | discord.PermissionMuteMembers | discord.PermissionMoveMembers

// squadLeadAllow is what the squad lead role gets in the squad channels:
// Priority Speaker for the priority push-to-talk key, and moderation of their
// own squad (server mute, disconnect or move a member). Not Deafen Members:
// the bots use server deafen themselves (see speaker_deafen.go).
const squadLeadAllow = discord.PermissionPrioritySpeaker | discord.PermissionMuteMembers | discord.PermissionMoveMembers

// grantablePermissions returns the owner bot's guild permissions, i.e. what it
// may put in a permission overwrite. Unknown (not cached yet) means all: the
// request is then tried as is.
func (m *Service) grantablePermissions(guildID snowflake.ID) discord.Permissions {
	self, ok := m.ownerClient.Caches.SelfMember(guildID)
	if !ok {
		return discord.PermissionsAll
	}
	perms := m.ownerClient.Caches.MemberPermissions(self)
	if perms.Has(discord.PermissionAdministrator) {
		return discord.PermissionsAll
	}
	return perms
}

// maskOverwrites keeps in each overwrite only the permissions in grant, and
// drops overwrites left empty.
func maskOverwrites(ow []discord.PermissionOverwrite, grant discord.Permissions) []discord.PermissionOverwrite {
	out := make([]discord.PermissionOverwrite, 0, len(ow))
	for _, o := range ow {
		switch v := o.(type) {
		case discord.RolePermissionOverwrite:
			v.Allow &= grant
			v.Deny &= grant
			if v.Allow|v.Deny != 0 {
				out = append(out, v)
			}
		case discord.MemberPermissionOverwrite:
			v.Allow &= grant
			v.Deny &= grant
			if v.Allow|v.Deny != 0 {
				out = append(out, v)
			}
		default:
			out = append(out, o)
		}
	}
	return out
}

type channelKind int

const (
	channelCategory channelKind = iota
	channelRaidLead
	channelSquad
	channelWaiting // class sorting waiting room: same access as the squads
)

const voiceMember = discord.PermissionViewChannel | discord.PermissionConnect |
	discord.PermissionSpeak | discord.PermissionUseVAD

// raidOverwrites returns the permission overwrites of one raid channel:
//   - the view (access) role sees every channel and joins the listener
//     channels; @everyone sees nothing when a view role is set;
//   - only the Raid Lead role joins the raid lead channel (Connect without
//     View, so the speaker role alone reveals nothing more);
//   - the squad lead role gets Priority Speaker in the squads, which Discord
//     requires for the priority push-to-talk key;
//   - the bots get everything they need explicitly.
func raidOverwrites(guildID, viewRole, callerRole, squadRole snowflake.ID, bots []snowflake.ID, kind channelKind) []discord.PermissionOverwrite {
	var ow []discord.PermissionOverwrite
	everyoneDeny := discord.Permissions(0)
	if viewRole != 0 {
		everyoneDeny |= discord.PermissionViewChannel
		if kind != channelCategory {
			everyoneDeny |= discord.PermissionConnect
		}
	}
	if kind == channelRaidLead && callerRole != 0 {
		everyoneDeny |= discord.PermissionConnect
	}
	if everyoneDeny != 0 {
		ow = append(ow, discord.RolePermissionOverwrite{RoleID: guildID, Deny: everyoneDeny})
	}
	if viewRole != 0 {
		allow := voiceMember
		if kind == channelRaidLead && callerRole != 0 {
			allow = discord.PermissionViewChannel
		}
		ow = append(ow, discord.RolePermissionOverwrite{RoleID: viewRole, Allow: allow})
	}
	switch kind {
	case channelRaidLead:
		if callerRole != 0 {
			ow = append(ow, discord.RolePermissionOverwrite{RoleID: callerRole,
				Allow: discord.PermissionConnect | discord.PermissionSpeak | discord.PermissionUseVAD})
		}
	case channelSquad:
		if squadRole != 0 {
			ow = append(ow, discord.RolePermissionOverwrite{RoleID: squadRole, Allow: squadLeadAllow})
		}
	}
	// The bots get the same permissions on the category as on its channels:
	// Discord checks the overwrites of a new channel against what the bot
	// holds in the parent category, so a category that only let the bots view
	// made every child channel fail with Missing Permissions.
	botAllow := voiceMember | discord.PermissionPrioritySpeaker
	for _, id := range bots {
		ow = append(ow, discord.MemberPermissionOverwrite{UserID: id, Allow: botAllow})
	}
	return ow
}

// releaseRaidChannelsAsync deletes the channels of the raid that just stopped
// in the background. It deletes exactly that set: a raid started again in the
// meantime has new channels, which are left alone.
func (m *Service) releaseRaidChannelsAsync(guildID snowflake.ID) {
	if !m.channels.opts.Enabled || m.channels.keep.Load() {
		return
	}
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		m.channels.op.Lock()
		defer m.channels.op.Unlock()
		m.deleteRaidChannels(ctx, guildID, rc, "raid stopped")
	}()
}

// ReleaseRaidChannels deletes guildID's raid channels when no raid uses them,
// for a /stop arriving after the raid already ended (idle stop, restart).
// Returns whether channels were deleted.
func (m *Service) ReleaseRaidChannels(ctx context.Context, guildID snowflake.ID) bool {
	if !m.channels.opts.Enabled || m.HasActiveSession(guildID) {
		return false
	}
	m.channels.op.Lock()
	defer m.channels.op.Unlock()
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok || m.HasActiveSession(guildID) {
		return false
	}
	m.deleteRaidChannels(ctx, guildID, rc, "manual stop")
	return true
}

// deleteRaidChannels deletes rc (squads, raid lead, then category). Members in
// the channels are disconnected by Discord. Channels already gone count as
// deleted; the others stay remembered for the next attempt. Caller holds op.
func (m *Service) deleteRaidChannels(ctx context.Context, guildID snowflake.ID, rc RaidChannels, reason string) {
	var left RaidChannels
	left.Category, left.RaidLead, left.Waiting, left.Created = rc.Category, rc.RaidLead, rc.Waiting, rc.Created
	failed := map[snowflake.ID]bool{}
	for _, id := range rc.all() {
		err := m.ownerClient.Rest.DeleteChannel(id, rest.WithCtx(ctx), rest.WithReason("Voice Relay session end"))
		var restErr *rest.Error
		if err != nil && !(errors.As(err, &restErr) && restErr.Code == rest.JSONErrorCodeUnknownChannel) {
			failed[id] = true
			slog.Warn("failed to delete raid channel",
				slog.String("guildID", guildID.String()),
				slog.String("channelID", id.String()),
				slog.Any("err", err))
			continue
		}
		m.ownerClient.Caches.RemoveChannel(id)
	}
	for _, id := range rc.Squads {
		if failed[id] {
			left.Squads = append(left.Squads, id)
		}
	}
	if !failed[left.RaidLead] {
		left.RaidLead = 0
	}
	if !failed[left.Waiting] {
		left.Waiting = 0
	}
	if !failed[left.Category] {
		left.Category = 0
	}

	m.channels.mu.Lock()
	cur, ok := m.channels.byGuild[guildID]
	if ok && cur.Category == rc.Category {
		if len(failed) == 0 {
			delete(m.channels.byGuild, guildID)
		} else {
			m.channels.byGuild[guildID] = left
		}
		m.saveChannelStateLocked()
	}
	m.channels.mu.Unlock()

	slog.Info("raid channels deleted",
		slog.String("guildID", guildID.String()),
		slog.String("reason", reason),
		slog.Int("deleted", len(rc.all())-len(failed)),
		slog.Int("failed", len(failed)),
	)
}

func (m *Service) rememberChannels(guildID snowflake.ID, rc RaidChannels) {
	m.channels.mu.Lock()
	defer m.channels.mu.Unlock()
	if m.channels.byGuild == nil {
		m.channels.byGuild = make(map[snowflake.ID]RaidChannels)
	}
	rc.Squads = slices.Clone(rc.Squads)
	m.channels.byGuild[guildID] = rc
	m.saveChannelStateLocked()
}

func (m *Service) saveChannelStateLocked() {
	path := m.channels.opts.StateFile
	if path == "" {
		return
	}
	if err := writeChannelState(path, m.channels.byGuild); err != nil {
		slog.Warn("failed to save raid channel state", slog.String("path", path), slog.Any("err", err))
	}
}

func writeChannelState(path string, byGuild map[snowflake.ID]RaidChannels) error {
	data, err := json.MarshalIndent(byGuild, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadChannelState(path string) map[snowflake.ID]RaidChannels {
	byGuild := make(map[snowflake.ID]RaidChannels)
	if path == "" {
		return byGuild
	}
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- operator-configured state file
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read raid channel state", slog.String("path", path), slog.Any("err", err))
		}
		return byGuild
	}
	if err := json.Unmarshal(data, &byGuild); err != nil {
		slog.Warn("invalid raid channel state, ignored", slog.String("path", path), slog.Any("err", err))
		return make(map[snowflake.ID]RaidChannels)
	}
	return byGuild
}

var trailingNumber = regexp.MustCompile(`(\d+)\s*$`)

// pickSquadSpeakers keeps the enabled speakers, in natural name order
// ("Speaker-2" before "Speaker-10"), at most n of them.
func pickSquadSpeakers(speakers []guild.Speaker, n int) []guild.Speaker {
	enabled := slices.DeleteFunc(slices.Clone(speakers), func(s guild.Speaker) bool { return !s.Enabled })
	slices.SortFunc(enabled, func(a, b guild.Speaker) int {
		pa, na := splitNumber(a.Username)
		pb, nb := splitNumber(b.Username)
		return cmp.Or(
			strings.Compare(strings.ToLower(pa), strings.ToLower(pb)),
			cmp.Compare(na, nb),
			cmp.Compare(a.ID, b.ID),
		)
	})
	if len(enabled) > n {
		enabled = enabled[:n]
	}
	return enabled
}

func splitNumber(name string) (string, int) {
	loc := trailingNumber.FindStringSubmatchIndex(name)
	if loc == nil {
		return name, -1
	}
	n, err := strconv.Atoi(name[loc[2]:loc[3]])
	if err != nil {
		return name, -1
	}
	return name[:loc[0]], n
}

// boardModeration is what the board role gets in every voice channel of the
// session: server mute, disconnect and move members. Seeing and joining still
// comes from the access role.
const boardModeration = discord.PermissionMuteMembers | discord.PermissionMoveMembers

func boardOverwrites(boardRole snowflake.ID, kind channelKind) []discord.PermissionOverwrite {
	if boardRole == 0 || kind == channelCategory {
		return nil
	}
	return []discord.PermissionOverwrite{discord.RolePermissionOverwrite{RoleID: boardRole, Allow: boardModeration}}
}
