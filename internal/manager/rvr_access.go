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

// RvR access: when ACCESS_ROLE is set, only members holding it see and join
// the raid channels. Members of the RvR board (BOARD_ROLE, together with the
// faction role) grant and revoke it from Discord with two user commands
// (right click on a member > Apps), without any Discord permission of their
// own: the owner bot changes that single role and nothing else.

var (
	// ErrAccessDisabled: ACCESS_ROLE or BOARD_ROLE is not configured.
	ErrAccessDisabled = errors.New("rvr access management is not configured")
	// ErrAccessNotBoard: the member using the command is not on the board of
	// this faction.
	ErrAccessNotBoard = errors.New("only the RvR board of this faction can do this")
	// ErrAccessRoleMissing: ACCESS_ROLE or BOARD_ROLE matches no guild role.
	ErrAccessRoleMissing = errors.New("rvr access role not found")
	// ErrAccessTargetBot: the target is a bot.
	ErrAccessTargetBot = errors.New("bots do not need rvr access")
	// ErrAccessWrongFaction: the target lacks the faction role.
	ErrAccessWrongFaction = errors.New("the member does not have the faction role")
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

// AccessEnabled reports whether the board can manage RvR access.
func (m *Service) AccessEnabled() bool {
	o := m.channels.opts
	return o.Enabled && o.AccessRole != "" && o.BoardRole != ""
}

// AccessFaction is the faction name shown in the command names.
func (m *Service) AccessFaction() string { return m.channels.opts.Faction }

// CanManageAccess reports whether a member with roleIDs and perms may grant
// or revoke access: server managers always, otherwise the board role together
// with the faction role, so the board of the other faction cannot.
func (m *Service) CanManageAccess(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool {
	if !m.AccessEnabled() {
		return false
	}
	if perms.Has(discord.PermissionAdministrator) || perms.Has(discord.PermissionManageGuild) {
		return true
	}
	board := m.resolveRole(guildID, m.channels.opts.BoardRole)
	if board == 0 || !slices.Contains(roleIDs, board) {
		return false
	}
	if f := m.channels.opts.FactionRole; f != "" {
		faction := m.resolveRole(guildID, f)
		return faction != 0 && slices.Contains(roleIDs, faction)
	}
	return true
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
	// The faction role of the target is not checked: the board vets who gets
	// access, and the access role is the only gate.
	has := slices.Contains(targetRoles, role)

	reason := fmt.Sprintf("RvR %s: %s by %s", o.Faction, map[bool]string{true: "granted", false: "revoked"}[grant], actorID)
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
		slog.String("faction", o.Faction),
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
