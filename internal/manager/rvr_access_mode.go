package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Access mode: with an access role configured, the leaders choose from the
// panel whether the raid channels are restricted to members holding it
// (default, against spying) or open to everyone. Saved per guild in
// access-mode.json next to the channel state.

type accessModes struct {
	mu     sync.Mutex
	loaded bool
	open   map[snowflake.ID]bool
}

func (m *Service) accessModeFile() string {
	if m.channels.opts.StateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.channels.opts.StateFile), "access-mode.json")
}

func (m *Service) loadAccessModesLocked() {
	a := &m.accessMode
	if a.loaded {
		return
	}
	a.loaded = true
	a.open = map[snowflake.ID]bool{}
	if f := m.accessModeFile(); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			if err := json.Unmarshal(b, &a.open); err != nil {
				slog.Warn("access mode: unreadable state, everything restricted", slog.String("file", f), slog.Any("err", err))
				a.open = map[snowflake.ID]bool{}
			}
		}
	}
}

// AccessOpen reports whether guildID's raid channels are open to everyone
// although an access role is configured.
func (m *Service) AccessOpen(guildID snowflake.ID) bool {
	m.accessMode.mu.Lock()
	defer m.accessMode.mu.Unlock()
	m.loadAccessModesLocked()
	return m.accessMode.open[guildID]
}

// AccessModeAvailable reports whether there is an access role to switch.
func (m *Service) AccessModeAvailable() bool { return m.AccessEnabled() }

// SetAccessOpen opens (open=true) or restricts guildID's raid channels. The
// channels of a running raid are changed at once and, when restricting, the
// people in them without access are disconnected (they can then ask for it);
// the next raids follow the saved choice. Returns how many were disconnected.
func (m *Service) SetAccessOpen(ctx context.Context, guildID snowflake.ID, open bool) (int, error) {
	if !m.AccessEnabled() {
		return 0, ErrAccessDisabled
	}
	m.accessMode.mu.Lock()
	m.loadAccessModesLocked()
	if open {
		m.accessMode.open[guildID] = true
	} else {
		delete(m.accessMode.open, guildID)
	}
	snapshot := make(map[snowflake.ID]bool, len(m.accessMode.open))
	for k, v := range m.accessMode.open {
		snapshot[k] = v
	}
	m.accessMode.mu.Unlock()
	if f := m.accessModeFile(); f != "" {
		if b, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
			if err := os.WriteFile(f, b, 0o600); err != nil {
				slog.Warn("access mode: not saved", slog.String("file", f), slog.Any("err", err))
			}
		}
	}
	slog.Info("access mode changed", slog.String("guildID", guildID.String()), slog.Bool("open", open))
	if err := m.applyAccessMode(ctx, guildID, open); err != nil {
		return 0, err
	}
	if open {
		return 0, nil
	}
	return m.disconnectWithoutAccess(ctx, guildID), nil
}

// disconnectWithoutAccess moves out of the raid voice channels everyone who
// lacks the view role, except bots and those who manage access (leaders,
// server managers).
func (m *Service) disconnectWithoutAccess(ctx context.Context, guildID snowflake.ID) int {
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok {
		return 0
	}
	name := m.viewRoleName(guildID)
	if name == "" {
		return 0
	}
	view := m.resolveRole(guildID, name)
	if view == 0 {
		return 0
	}
	voice := slices.Clone(rc.Squads)
	voice = append(voice, rc.RaidLead, rc.Waiting)
	caches := m.ownerClient.Caches
	var targets []snowflake.ID
	for vs := range caches.VoiceStates(guildID) {
		if vs.ChannelID == nil || *vs.ChannelID == 0 || !slices.Contains(voice, *vs.ChannelID) {
			continue
		}
		mem, ok := caches.Member(guildID, vs.UserID)
		if !ok || m.IsBot(mem.User) || slices.Contains(mem.RoleIDs, view) {
			continue
		}
		if m.CanManageAccess(guildID, mem.RoleIDs, caches.MemberPermissions(mem)) {
			continue
		}
		targets = append(targets, vs.UserID)
	}
	opts := []rest.RequestOpt{rest.WithCtx(ctx), rest.WithReason("raid restricted: no access")}
	n := 0
	for _, id := range targets {
		if m.disconnectFromRaid(ctx, guildID, id, opts) {
			n++
		}
	}
	if n > 0 {
		slog.Info("raid restricted: members without access disconnected", slog.String("guildID", guildID.String()), slog.Int("count", n))
	}
	return n
}

// viewRoleName is the role that sees the raid channels of guildID: the access
// role while restricted, else the faction role (often none: everyone).
func (m *Service) viewRoleName(guildID snowflake.ID) string {
	o := m.channels.opts
	if o.AccessRole != "" && !m.AccessOpen(guildID) {
		return o.AccessRole
	}
	return o.FactionRole
}

// applyAccessMode rewrites the @everyone and access role overwrites of the
// existing raid channels.
func (m *Service) applyAccessMode(ctx context.Context, guildID snowflake.ID, open bool) error {
	m.channels.op.Lock()
	defer m.channels.op.Unlock()
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok {
		return nil
	}
	var view snowflake.ID
	if name := m.viewRoleName(guildID); name != "" {
		if view = m.resolveRole(guildID, name); view == 0 {
			return fmt.Errorf("%w: %q", ErrAccessRoleMissing, name)
		}
	}
	callerRole, _ := m.callerRole(guildID)
	grant := m.grantablePermissions(guildID)
	opts := []rest.RequestOpt{rest.WithCtx(ctx), rest.WithReason(map[bool]string{true: "raid opened to everyone", false: "raid restricted"}[open])}

	kinds := map[snowflake.ID]channelKind{rc.RaidLead: channelRaidLead, rc.Waiting: channelWaiting}
	if !rc.SharedCategory {
		kinds[rc.Category] = channelCategory // the home category stays open: it holds the panel
	}
	for _, id := range rc.Squads {
		kinds[id] = channelSquad
	}
	var firstErr error
	for id, kind := range kinds {
		if id == 0 {
			continue
		}
		ow := maskOverwrites(raidOverwrites(guildID, view, callerRole, 0, nil, kind), grant)
		everyone, viewAllow := discord.Permissions(0), discord.Permissions(0)
		for _, o := range ow {
			if r, ok := o.(discord.RolePermissionOverwrite); ok {
				switch r.RoleID {
				case guildID:
					everyone = r.Deny
				case view:
					viewAllow = r.Allow
				}
			}
		}
		var err error
		if everyone != 0 {
			zero := discord.Permissions(0)
			err = m.ownerClient.Rest.UpdatePermissionOverwrite(id, guildID, discord.RolePermissionOverwriteUpdate{Allow: &zero, Deny: &everyone}, opts...)
		} else {
			err = m.ownerClient.Rest.DeletePermissionOverwrite(id, guildID, opts...)
			if isUnknownOverwrite(err) {
				err = nil
			}
		}
		if err == nil && view != 0 && view != guildID && viewAllow != 0 {
			zero := discord.Permissions(0)
			err = m.ownerClient.Rest.UpdatePermissionOverwrite(id, view, discord.RolePermissionOverwriteUpdate{Allow: &viewAllow, Deny: &zero}, opts...)
		}
		if err != nil && firstErr == nil {
			firstErr = accessError(err)
		}
	}
	rc.ViewRole = view
	rc.Squads = slices.Clone(rc.Squads)
	m.rememberChannels(guildID, rc)
	return firstErr
}

// isUnknownOverwrite reports Discord's answer for an overwrite already gone.
func isUnknownOverwrite(err error) bool {
	var re *rest.Error
	return err != nil && errors.As(err, &re) && (re.Code == 10009 || (re.Response != nil && re.Response.StatusCode == 404))
}
