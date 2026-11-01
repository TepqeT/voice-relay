package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/manager"
)

// SquadLeadService is the squad lead appointment surface of the manager.
type SquadLeadService interface {
	SquadLeadsManageable() bool
	AccessFaction() string
	CanManageSquadLeads(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool
	SetSquadLead(ctx context.Context, guildID, actorID, targetID snowflake.ID, targetRoles []snowflake.ID, targetIsBot, grant bool) (manager.SquadLeadResult, error)
}

func (h *CommandHandlers) squadLeads() (SquadLeadService, bool) {
	s, ok := h.rvr.(SquadLeadService)
	if !ok || !s.SquadLeadsManageable() {
		return nil, false
	}
	return s, true
}

// squadLeadCommandNames are the raid leads' two user commands. Discord caps a
// name at 32 characters and an app at 5 user commands (4 used with access).
func squadLeadCommandNames(label, faction string) (name, dismiss string) {
	name = fmt.Sprintf("%s %s : nommer chef", label, faction)
	dismiss = fmt.Sprintf("%s %s : retirer chef", label, faction)
	if len([]rune(dismiss)) > 32 {
		name, dismiss = shortCommandName(label, "nommer chef"), shortCommandName(label, "retirer chef")
	}
	return name, dismiss
}

// SquadLeadCommands returns the user commands to register, none when squad
// lead management is not configured.
func (h *CommandHandlers) SquadLeadCommands() []discord.ApplicationCommandCreate {
	s, ok := h.squadLeads()
	if !ok {
		return nil
	}
	name, dismiss := squadLeadCommandNames(h.eventLabel(), s.AccessFaction())
	return []discord.ApplicationCommandCreate{
		discord.UserCommandCreate{Name: name},
		discord.UserCommandCreate{Name: dismiss},
	}
}

func (h *CommandHandlers) registerSquadLeads(r handler.Router) {
	s, ok := h.squadLeads()
	if !ok {
		return
	}
	name, dismiss := squadLeadCommandNames(h.eventLabel(), s.AccessFaction())
	r.UserCommand("/"+name, h.handleSquadLead(s, true))
	r.UserCommand("/"+dismiss, h.handleSquadLead(s, false))
	slog.Info("squad lead commands enabled", slog.String("name", name), slog.String("dismiss", dismiss))
}

func (h *CommandHandlers) handleSquadLead(s SquadLeadService, grant bool) func(discord.UserCommandInteractionData, *handler.CommandEvent) error {
	return func(data discord.UserCommandInteractionData, e *handler.CommandEvent) error {
		loc := h.bundle.For("", e.Locale())
		guildID, errMsg := requireGuild(loc, e.GuildID())
		if errMsg != nil {
			return e.CreateMessage(*errMsg)
		}
		loc = h.loc(guildID, e.Locale())
		actor := e.Member()
		if actor == nil || !s.CanManageSquadLeads(guildID, actor.RoleIDs, actor.Permissions) {
			return e.CreateMessage(ephemeral(loc.T("squad.denied")))
		}
		target := data.TargetUser()
		var targetRoles []snowflake.ID
		if m, ok := data.Resolved.Members[target.ID]; ok {
			targetRoles = m.RoleIDs
		}
		if err := e.DeferCreateMessage(true); err != nil {
			return err
		}

		res, err := s.SetSquadLead(e.Ctx, guildID, actor.User.ID, target.ID, targetRoles, target.Bot, grant)
		mention := fmt.Sprintf("<@%s>", target.ID)
		role := fmt.Sprintf("<@&%s>", res.RoleID)
		var msg string
		switch {
		case errors.Is(err, manager.ErrAccessTargetBot):
			msg = loc.T("access.bot")
		case errors.Is(err, manager.ErrSquadLeadRoleMissing):
			msg = loc.T("squad.role_missing", "Err", err.Error())
		case errors.Is(err, manager.ErrAccessPermission):
			msg = loc.T("access.no_permission") + "\n`" + err.Error() + "`"
		case err != nil:
			msg = loc.T("access.failed", "Err", err.Error())
		case grant && res.Changed:
			msg = loc.T("squad.named", "Member", mention, "Role", role)
		case grant:
			msg = loc.T("squad.already", "Member", mention, "Role", role)
		case res.Changed:
			msg = loc.T("squad.dismissed", "Member", mention, "Role", role)
		default:
			msg = loc.T("squad.not_on", "Member", mention, "Role", role)
		}
		if err == nil && res.AccessAdded {
			msg += "\n" + loc.T("squad.access_added", "Role", fmt.Sprintf("<@&%s>", res.AccessRoleID))
		}
		_, uerr := e.UpdateInteractionResponse(discord.MessageUpdate{
			Content:         &msg,
			AllowedMentions: &discord.AllowedMentions{},
		})
		return uerr
	}
}
