package bot

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// Access requests: a member presses "Request access" on the panel, the board
// gets a message with Approve and Deny, one click grants the access role.
// Replaces right clicks and pasted usernames for the common case.

const accessReqID = "/access/req"

// accessRequestTTL lets a member ask again when a request was left unanswered.
const accessRequestTTL = 12 * time.Hour

// accessRequester is implemented by the manager. Asserted so the existing
// test doubles stay valid.
type accessRequester interface {
	AccessRoleID(guildID snowflake.ID) snowflake.ID
	BoardRoleID(guildID snowflake.ID) snowflake.ID
}

// accessRequests remembers pending requests, one per member and server.
type accessRequests struct {
	mu sync.Mutex
	m  map[[2]snowflake.ID]time.Time
}

// begin records a request; false when one is already pending.
func (r *accessRequests) begin(guildID, userID snowflake.ID, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = make(map[[2]snowflake.ID]time.Time)
	}
	k := [2]snowflake.ID{guildID, userID}
	if at, ok := r.m[k]; ok && now.Sub(at) < accessRequestTTL {
		return false
	}
	r.m[k] = now
	return true
}

func (r *accessRequests) end(guildID, userID snowflake.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, [2]snowflake.ID{guildID, userID})
}

// handleAccessRequest posts the request for the board in the channel of the
// panel, in the server's language, pinging the board role.
func (h *CommandHandlers) handleAccessRequest(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	a, ok := h.access()
	req, ok2 := h.rvr.(accessRequester)
	if !ok || !ok2 {
		return e.CreateMessage(ephemeral(loc.T("access.req.unavailable")))
	}
	member := e.Member()
	if member == nil || member.User.Bot {
		return e.CreateMessage(ephemeral(loc.T("access.req.unavailable")))
	}
	role := req.AccessRoleID(guildID)
	if role == 0 {
		return e.CreateMessage(ephemeral(loc.T("access.role_missing", "Err", "ACCESS_ROLE")))
	}
	if slices.Contains(member.RoleIDs, role) {
		return e.CreateMessage(ephemeral(loc.T("access.req.already")))
	}
	if a.CanManageAccess(guildID, member.RoleIDs, member.Permissions) {
		// The board does not need to ask itself.
		return e.CreateMessage(ephemeral(loc.T("access.req.self")))
	}
	if !h.accessReqs.begin(guildID, member.User.ID, time.Now()) {
		return e.CreateMessage(ephemeral(loc.T("access.req.pending")))
	}

	board := req.BoardRoleID(guildID)
	gloc := h.bundle.For(h.manager.GetLocale(guildID), "")
	boardMention := ""
	mentions := &discord.AllowedMentions{}
	if board != 0 {
		boardMention = fmt.Sprintf("<@&%s>", board)
		mentions.Roles = []snowflake.ID{board}
	}
	userID := member.User.ID.String()
	_, err := e.Client().Rest.CreateMessage(e.Channel().ID(), discord.MessageCreate{
		Content: gloc.T("access.req.message", "Board", boardMention, "Member", "<@"+userID+">"),
		Components: []discord.LayoutComponent{discord.NewActionRow(
			discord.NewSuccessButton(gloc.T("access.req.approve"), accessReqID+"/yes/"+userID),
			discord.NewDangerButton(gloc.T("access.req.deny"), accessReqID+"/no/"+userID),
		)},
		AllowedMentions: mentions,
	})
	if err != nil {
		h.accessReqs.end(guildID, member.User.ID)
		return e.CreateMessage(ephemeral(loc.T("access.failed", "Err", err.Error())))
	}
	return e.CreateMessage(ephemeral(loc.T("access.req.sent")))
}

// handleAccessDecision is a board member pressing Approve or Deny. The request
// message is replaced by the outcome, without buttons.
func (h *CommandHandlers) handleAccessDecision(guildID snowflake.ID, loc *i18n.Localizer, data discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	a, ok := h.access()
	if !ok {
		return e.CreateMessage(ephemeral(loc.T("access.req.unavailable")))
	}
	actor := e.Member()
	if actor == nil || !a.CanManageAccess(guildID, actor.RoleIDs, actor.Permissions) {
		return e.CreateMessage(ephemeral(loc.T("access.denied")))
	}
	target, err := snowflake.Parse(e.Vars["userID"])
	if err != nil {
		return e.CreateMessage(ephemeral(loc.T("access.req.unavailable")))
	}
	approve := e.Vars["decision"] == "yes"
	gloc := h.bundle.For(h.manager.GetLocale(guildID), "")
	member, actorMention := fmt.Sprintf("<@%s>", target), fmt.Sprintf("<@%s>", actor.User.ID)

	var content string
	if approve {
		if err := e.DeferUpdateMessage(); err != nil {
			return err
		}
		res, err := a.SetRvRAccess(e.Ctx, guildID, actor.User.ID, target, nil, false, true)
		switch {
		case errors.Is(err, manager.ErrAccessRoleMissing):
			content = gloc.T("access.role_missing", "Err", err.Error())
		case errors.Is(err, manager.ErrAccessPermission):
			content = gloc.T("access.no_permission") + "\n`" + err.Error() + "`"
		case err != nil:
			content = gloc.T("access.failed", "Err", err.Error())
		default:
			content = gloc.T("access.req.approved", "Member", member, "Actor", actorMention, "Role", fmt.Sprintf("<@&%s>", res.RoleID))
		}
		if err == nil {
			h.accessReqs.end(guildID, target)
		}
		empty := []discord.LayoutComponent{}
		if err != nil {
			empty = nil // keep the buttons so the board can retry
		}
		_, uerr := e.UpdateInteractionResponse(discord.MessageUpdate{Content: &content, Components: componentsOrKeep(empty), AllowedMentions: &discord.AllowedMentions{}})
		return uerr
	}
	h.accessReqs.end(guildID, target)
	content = gloc.T("access.req.denied", "Member", member, "Actor", actorMention)
	empty := []discord.LayoutComponent{}
	return e.UpdateMessage(discord.MessageUpdate{Content: &content, Components: &empty, AllowedMentions: &discord.AllowedMentions{}})
}

// componentsOrKeep returns nil (leave the message's buttons as they are) for
// a nil slice, else a pointer replacing them.
func componentsOrKeep(c []discord.LayoutComponent) *[]discord.LayoutComponent {
	if c == nil {
		return nil
	}
	return &c
}
