package bot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	ApproverRoleIDs(guildID snowflake.ID) []snowflake.ID
}

// accessModeSwitch is implemented by the manager: raid channels open to
// everyone or restricted to the access role, chosen from the panel.
type accessModeSwitch interface {
	AccessOpen(guildID snowflake.ID) bool
	SetAccessOpen(ctx context.Context, guildID snowflake.ID, open bool) (int, error)
}

func (h *CommandHandlers) accessMode() (accessModeSwitch, bool) {
	s, ok := h.rvr.(accessModeSwitch)
	return s, ok
}

// accessRoleMention is the access role as a mention, or its configured name.
func (h *CommandHandlers) accessRoleMention(guildID snowflake.ID) string {
	if r, ok := h.rvr.(accessRequester); ok {
		if id := r.AccessRoleID(guildID); id != 0 {
			return fmt.Sprintf("<@&%s>", id)
		}
	}
	return "the access role"
}

// handleAccessMode opens the raid channels to everyone or restricts them.
// Reserved to those who manage access (raid leads, squad leads, board,
// server managers).
func (h *CommandHandlers) handleAccessMode(open bool) guildButtonHandler {
	return func(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
		a, ok := h.access()
		mode, ok2 := h.accessMode()
		if !ok || !ok2 {
			return e.CreateMessage(ephemeral(loc.T("access.req.unavailable")))
		}
		actor := e.Member()
		if actor == nil || !a.CanManageAccess(guildID, actor.RoleIDs, actor.Permissions) {
			return e.CreateMessage(ephemeral(loc.T("access.denied")))
		}
		if err := e.DeferCreateMessage(true); err != nil {
			return err
		}
		kicked, err := mode.SetAccessOpen(e.Ctx, guildID, open)
		var msg string
		switch {
		case err != nil:
			msg = loc.T("access.mode.failed", "Err", err.Error())
		case open:
			msg = loc.T("access.mode.opened")
		case kicked > 0:
			msg = loc.T("access.mode.restricted_kicked", "Count", kicked)
		default:
			msg = loc.T("access.mode.restricted")
		}
		h.notifyPanel(guildID)
		h.componentFollowUp(e, msg)
		return nil
	}
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
	if mode, ok := h.accessMode(); ok && mode.AccessOpen(guildID) {
		return e.CreateMessage(ephemeral(loc.T("access.req.open")))
	}
	if a.CanManageAccess(guildID, member.RoleIDs, member.Permissions) {
		// The board does not need to ask itself.
		return e.CreateMessage(ephemeral(loc.T("access.req.self")))
	}
	if !h.accessReqs.begin(guildID, member.User.ID, time.Now()) {
		return e.CreateMessage(ephemeral(loc.T("access.req.pending")))
	}

	gloc := h.bundle.For(h.manager.GetLocale(guildID), "")
	mentions := &discord.AllowedMentions{}
	var leaders []string
	for _, id := range req.ApproverRoleIDs(guildID) {
		leaders = append(leaders, fmt.Sprintf("<@&%s>", id))
		mentions.Roles = append(mentions.Roles, id)
	}
	userID := member.User.ID.String()
	_, err := e.Client().Rest.CreateMessage(e.Channel().ID(), discord.MessageCreate{
		Content: gloc.T("access.req.message", "Leaders", strings.Join(leaders, " "), "Member", "<@"+userID+">"),
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
