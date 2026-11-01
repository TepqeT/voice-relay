package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Squad lead appointment: raid leads name and dismiss the squad leads of
// their faction from Discord (right click on a member > Apps), without any
// Discord permission of their own. Naming a squad lead also grants the RvR
// access role, since a squad lead must see the squad channels.

var (
	// ErrSquadLeadsDisabled: no squad lead role or no raid lead role configured.
	ErrSquadLeadsDisabled = errors.New("squad lead management is not configured")
	// ErrSquadLeadRoleMissing: SQUAD_LEAD_ROLE matches no guild role.
	ErrSquadLeadRoleMissing = errors.New("squad lead role not found")
)

// SquadLeadResult tells the bot layer what happened.
type SquadLeadResult struct {
	RoleID       snowflake.ID
	Changed      bool // false when the member already was in the asked state
	AccessAdded  bool // naming also granted the RvR access role
	AccessRoleID snowflake.ID
}

// SquadLeadsManageable reports whether raid leads can name squad leads.
func (m *Service) SquadLeadsManageable() bool {
	return m.channels.opts.Enabled && m.rvr.opts.SquadLeadRole != "" && m.rvr.opts.RaidLeadRole != ""
}

// CanManageSquadLeads reports whether a member may name or dismiss squad
// leads: server managers always, otherwise the raid lead role together with
// the faction role, so the raid leads of the other faction cannot.
func (m *Service) CanManageSquadLeads(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool {
	if !m.SquadLeadsManageable() {
		return false
	}
	if perms.Has(discord.PermissionAdministrator) || perms.Has(discord.PermissionManageGuild) {
		return true
	}
	caller, ok := m.callerRole(guildID)
	if !ok || !slices.Contains(roleIDs, caller) {
		return false
	}
	if f := m.channels.opts.FactionRole; f != "" {
		faction := m.resolveRole(guildID, f)
		return faction != 0 && slices.Contains(roleIDs, faction)
	}
	return true
}

// SetSquadLead names (grant=true) or dismisses targetID as squad lead. Naming
// also grants the RvR access role when access management is configured;
// dismissing leaves access as it is.
func (m *Service) SetSquadLead(ctx context.Context, guildID, actorID, targetID snowflake.ID, targetRoles []snowflake.ID, targetIsBot, grant bool) (SquadLeadResult, error) {
	if !m.SquadLeadsManageable() {
		return SquadLeadResult{}, ErrSquadLeadsDisabled
	}
	role := m.resolveSquadRole(guildID)
	if role == 0 {
		return SquadLeadResult{}, fmt.Errorf("%w: %q", ErrSquadLeadRoleMissing, m.rvr.opts.SquadLeadRole)
	}
	res := SquadLeadResult{RoleID: role}
	if targetIsBot {
		return res, ErrAccessTargetBot
	}
	faction := m.channels.opts.Faction
	reason := fmt.Sprintf("RvR %s: squad lead %s by %s", faction, map[bool]string{true: "named", false: "dismissed"}[grant], actorID)
	opts := []rest.RequestOpt{rest.WithCtx(ctx), rest.WithReason(reason)}

	has := slices.Contains(targetRoles, role)
	switch {
	case grant && !has:
		if err := m.ownerClient.Rest.AddMemberRole(guildID, targetID, role, opts...); err != nil {
			return res, accessError(err)
		}
		res.Changed = true
	case !grant && has:
		if err := m.ownerClient.Rest.RemoveMemberRole(guildID, targetID, role, opts...); err != nil {
			return res, accessError(err)
		}
		res.Changed = true
	}

	if grant && m.AccessEnabled() {
		if access := m.resolveRole(guildID, m.channels.opts.AccessRole); access != 0 {
			res.AccessRoleID = access
			if !slices.Contains(targetRoles, access) {
				if err := m.ownerClient.Rest.AddMemberRole(guildID, targetID, access, opts...); err != nil {
					return res, accessError(err)
				}
				res.AccessAdded = true
			}
		}
	}

	slog.Info("squad lead changed",
		slog.String("guildID", guildID.String()),
		slog.String("faction", faction),
		slog.Bool("named", grant),
		slog.Bool("changed", res.Changed),
		slog.Bool("accessAdded", res.AccessAdded),
		slog.String("by", actorID.String()),
		slog.String("member", targetID.String()),
	)
	return res, nil
}
