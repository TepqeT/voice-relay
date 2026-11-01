package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// AccessService is the RvR access surface of the manager: the board grants
// and revokes the role that opens the raid channels. Kept apart from
// RvRService so its test doubles stay valid.
type AccessService interface {
	AccessEnabled() bool
	AccessFaction() string
	CanManageAccess(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool
	SetRvRAccess(ctx context.Context, guildID, actorID, targetID snowflake.ID, targetRoles []snowflake.ID, targetIsBot, grant bool) (manager.AccessResult, error)
}

func (h *CommandHandlers) access() (AccessService, bool) {
	a, ok := h.rvr.(AccessService)
	if !ok || !a.AccessEnabled() {
		return nil, false
	}
	return a, true
}

// accessCommandNames are the two user commands (right click on a member >
// Apps). The faction is in the name so both factions' bots can offer them in
// the same server without confusion. Discord caps the name at 32 characters.
func accessCommandNames(label, faction string) (grant, revoke string) {
	grant = fmt.Sprintf("%s %s : autoriser", label, faction)
	revoke = fmt.Sprintf("%s %s : retirer", label, faction)
	if strings.TrimSpace(faction) == "" || len([]rune(grant)) > 32 {
		grant, revoke = shortCommandName(label, "autoriser"), shortCommandName(label, "retirer")
	}
	return grant, revoke
}

// shortCommandName is the fallback "<label> : <action>", the label cut so the
// name fits Discord's 32 characters.
func shortCommandName(label, action string) string {
	suffix := " : " + action
	return i18n.TruncateRunes(label, 32-len([]rune(suffix))) + suffix
}

// eventLabel is the event name in command names (EVENT_LABEL, default RvR).
func (h *CommandHandlers) eventLabel() string {
	if h.bundle == nil {
		return i18n.DefaultEventLabel
	}
	return h.bundle.EventLabel()
}

// AccessCommands returns the user commands to register, none when access
// management is not configured.
func (h *CommandHandlers) AccessCommands() []discord.ApplicationCommandCreate {
	a, ok := h.access()
	if !ok {
		return nil
	}
	grant, revoke := accessCommandNames(h.eventLabel(), a.AccessFaction())
	return []discord.ApplicationCommandCreate{
		discord.UserCommandCreate{Name: grant},
		discord.UserCommandCreate{Name: revoke},
	}
}

func (h *CommandHandlers) registerAccess(r handler.Router) {
	a, ok := h.access()
	if !ok {
		return
	}
	grant, revoke := accessCommandNames(h.eventLabel(), a.AccessFaction())
	r.UserCommand("/"+grant, h.handleAccess(a, true))
	r.UserCommand("/"+revoke, h.handleAccess(a, false))
	r.ButtonComponent("/access/pick/{mode}/{userID}", h.handleAccessPick(a))
	r.ButtonComponent("/access/skip", h.handleAccessSkip(a))
	slog.Info("rvr access commands enabled", slog.String("grant", grant), slog.String("revoke", revoke))
}

func (h *CommandHandlers) handleAccess(a AccessService, grant bool) func(discord.UserCommandInteractionData, *handler.CommandEvent) error {
	return func(data discord.UserCommandInteractionData, e *handler.CommandEvent) error {
		loc := h.bundle.For("", e.Locale())
		guildID, errMsg := requireGuild(loc, e.GuildID())
		if errMsg != nil {
			return e.CreateMessage(*errMsg)
		}
		loc = h.loc(guildID, e.Locale())
		actor := e.Member()
		if actor == nil || !a.CanManageAccess(guildID, actor.RoleIDs, actor.Permissions) {
			return e.CreateMessage(ephemeral(loc.T("access.denied")))
		}
		target := data.TargetUser()
		var targetRoles []snowflake.ID
		if m, ok := data.Resolved.Members[target.ID]; ok {
			targetRoles = m.RoleIDs
		}
		if err := e.DeferCreateMessage(true); err != nil {
			return err
		}

		res, err := a.SetRvRAccess(e.Ctx, guildID, actor.User.ID, target.ID, targetRoles, target.Bot, grant)
		mention := fmt.Sprintf("<@%s>", target.ID)
		role := fmt.Sprintf("<@&%s>", res.RoleID)
		var msg string
		switch {
		case errors.Is(err, manager.ErrAccessTargetBot):
			msg = loc.T("access.bot")
		case errors.Is(err, manager.ErrAccessWrongFaction):
			msg = loc.T("access.wrong_faction", "Member", mention)
		case errors.Is(err, manager.ErrAccessRoleMissing):
			msg = loc.T("access.role_missing", "Err", err.Error())
		case errors.Is(err, manager.ErrAccessPermission):
			msg = loc.T("access.no_permission") + "\n`" + err.Error() + "`"
		case err != nil:
			msg = loc.T("access.failed", "Err", err.Error())
		case grant && res.Changed:
			msg = loc.T("access.granted", "Member", mention, "Role", role)
		case grant:
			msg = loc.T("access.already", "Member", mention, "Role", role)
		case res.Disconnected:
			msg = loc.T("access.revoked_kicked", "Member", mention, "Role", role)
		case res.Changed:
			msg = loc.T("access.revoked", "Member", mention, "Role", role)
		default:
			msg = loc.T("access.not_on", "Member", mention, "Role", role)
		}
		_, uerr := e.UpdateInteractionResponse(discord.MessageUpdate{
			Content:         &msg,
			AllowedMentions: &discord.AllowedMentions{},
		})
		return uerr
	}
}
