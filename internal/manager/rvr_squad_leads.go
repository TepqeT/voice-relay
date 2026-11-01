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

// Squad lead appointment: speakers name and dismiss the squad leads from
// Discord (right click on a member > Apps), without any Discord permission of
// their own. Naming a squad lead also grants the access role, since a squad
// lead must see the listener channels.

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
	AccessAdded  bool // naming also granted the access role
	AccessRoleID snowflake.ID
}

// SquadLeadsManageable reports whether raid leads can name squad leads.
func (m *Service) SquadLeadsManageable() bool {
	return m.channels.opts.Enabled && m.rvr.opts.SquadLeadRole != "" && m.rvr.opts.RaidLeadRole != ""
}

// CanManageSquadLeads reports whether a member may name or dismiss squad
// leads: server managers always, otherwise the speaker role.
func (m *Service) CanManageSquadLeads(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool {
	if !m.SquadLeadsManageable() {
		return false
	}
	if perms.Has(discord.PermissionAdministrator) || perms.Has(discord.PermissionManageGuild) {
		return true
	}
	caller, ok := m.callerRole(guildID)
	return ok && slices.Contains(roleIDs, caller)
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
	reason := fmt.Sprintf("Voice Relay squad lead %s by %s", map[bool]string{true: "named", false: "dismissed"}[grant], actorID)
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
		slog.Bool("named", grant),
		slog.Bool("changed", res.Changed),
		slog.Bool("accessAdded", res.AccessAdded),
		slog.String("by", actorID.String()),
		slog.String("member", targetID.String()),
	)
	return res, nil
}
