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

// Access: when ACCESS_ROLE is set and the mode is restricted, only members
// holding it see and join the listener channels. Speakers, squad leads and
// the optional board role grant and revoke it from the panel or with two
// user commands (right click on a member > Apps), without any Discord
// permission of their own: the owner bot changes that single role.

var (
	// ErrAccessDisabled: ACCESS_ROLE or BOARD_ROLE is not configured.
	ErrAccessDisabled = errors.New("rvr access management is not configured")
	// ErrAccessNotBoard: the member using the command may not manage access.
	ErrAccessNotBoard = errors.New("only speakers, squad leads or the board can do this")
	// ErrAccessRoleMissing: ACCESS_ROLE or BOARD_ROLE matches no guild role.
	ErrAccessRoleMissing = errors.New("rvr access role not found")
	// ErrAccessTargetBot: the target is a bot.
	ErrAccessTargetBot = errors.New("bots do not need rvr access")
	// ErrAccessPermission: Discord refused the role change (role hierarchy or
	// missing Manage Roles).
	ErrAccessPermission = errors.New("the owner bot cannot change this role (Manage Roles, or its role is below the access role)")
)

// AccessResult tells the bot layer what happened.
type AccessResult struct {
	RoleID       snowflake.ID
	Changed      bool // false when the member already was in the asked state
	Disconnected bool // revoked while in a raid channel: kicked from voice
}

// AccessEnabled reports whether raid access is managed (an access role is
// configured with dynamic channels). The board role is optional: raid leads
// and squad leads approve players too.
func (m *Service) AccessEnabled() bool {
	o := m.channels.opts
	return o.Enabled && o.AccessRole != ""
}

// CanManageAccess reports whether a member with roleIDs and perms may grant
// or revoke access: server managers always, otherwise the speaker, squad lead
// or board role.
func (m *Service) CanManageAccess(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool {
	if !m.AccessEnabled() {
		return false
	}
	if perms.Has(discord.PermissionAdministrator) || perms.Has(discord.PermissionManageGuild) {
		return true
	}
	for _, id := range m.ApproverRoleIDs(guildID) {
		if slices.Contains(roleIDs, id) {
			return true
		}
	}
	return false
}

// ApproverRoleIDs are the roles that approve access requests (and are pinged
// by them): raid lead, squad lead, and the board role when one is set.
func (m *Service) ApproverRoleIDs(guildID snowflake.ID) []snowflake.ID {
	if !m.AccessEnabled() {
		return nil
	}
	var out []snowflake.ID
	if b := m.channels.opts.BoardRole; b != "" {
		if id := m.resolveRole(guildID, b); id != 0 {
			out = append(out, id)
		}
	}
	if id, ok := m.callerRole(guildID); ok && id != 0 && !slices.Contains(out, id) {
		out = append(out, id)
	}
	if name := m.rvr.opts.SquadLeadRole; name != "" {
		if id := m.resolveRole(guildID, name); id != 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// SetRvRAccess grants (grant=true) or revokes the access role of targetID.
// targetRoles are the target's current roles as Discord sent them with the
// command. A revoked member sitting in a raid channel is disconnected.
func (m *Service) SetRvRAccess(ctx context.Context, guildID, actorID, targetID snowflake.ID, targetRoles []snowflake.ID, targetIsBot, grant bool) (AccessResult, error) {
	if !m.AccessEnabled() {
		return AccessResult{}, ErrAccessDisabled
	}
	o := m.channels.opts
	role := m.resolveRole(guildID, o.AccessRole)
	if role == 0 {
		return AccessResult{}, fmt.Errorf("%w: %q", ErrAccessRoleMissing, o.AccessRole)
	}
	res := AccessResult{RoleID: role}
	if targetIsBot {
		return res, ErrAccessTargetBot
	}
	has := slices.Contains(targetRoles, role)

	reason := fmt.Sprintf("Voice Relay access %s by %s", map[bool]string{true: "granted", false: "revoked"}[grant], actorID)
	opts := []rest.RequestOpt{rest.WithCtx(ctx), rest.WithReason(reason)}
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
	if !grant {
		res.Disconnected = m.disconnectFromRaid(ctx, guildID, targetID, opts)
	}

	slog.Info("rvr access changed",
		slog.String("guildID", guildID.String()),
		slog.Bool("granted", grant),
		slog.Bool("changed", res.Changed),
		slog.Bool("disconnected", res.Disconnected),
		slog.String("by", actorID.String()),
		slog.String("member", targetID.String()),
	)
	return res, nil
}

// disconnectFromRaid kicks userID out of voice when they sit in one of the
// raid channels of guildID. Needs Move Members; a refusal is only logged.
func (m *Service) disconnectFromRaid(ctx context.Context, guildID, userID snowflake.ID, opts []rest.RequestOpt) bool {
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok {
		return false
	}
	vs, ok := m.ownerClient.Caches.VoiceState(guildID, userID)
	if !ok || vs.ChannelID == nil || !slices.Contains(rc.all(), *vs.ChannelID) {
		return false
	}
	// discord.MemberUpdate cannot send "channel_id": null (omitempty), which
	// is what disconnects a member.
	body := map[string]any{"channel_id": nil}
	if err := m.ownerClient.Rest.Do(rest.UpdateMember.Compile(nil, guildID, userID), body, nil, opts...); err != nil {
		slog.Warn("rvr access: could not disconnect the revoked member (Move Members?)",
			slog.String("guildID", guildID.String()),
			slog.String("member", userID.String()),
			slog.Any("err", err))
		return false
	}
	return true
}

func accessError(err error) error {
	var restErr *rest.Error
	if errors.As(err, &restErr) &&
		(restErr.Code == rest.JSONErrorCodeLackPermissionsToPerformAction || restErr.Code == rest.JSONErrorCodeMissingAccess) {
		return fmt.Errorf("%w (Discord %d: %s)", ErrAccessPermission, restErr.Code, restErr.Message)
	}
	return err
}
