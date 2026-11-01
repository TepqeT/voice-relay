package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/manager"
)

// AccessChannelService is the access channel surface of the manager.
type AccessChannelService interface {
	AccessService
	AccessChannelEnabled() bool
	IsAccessChannel(guildID, channelID snowflake.ID) bool
	MemberPermissions(member discord.Member) discord.Permissions
	FindMember(ctx context.Context, guildID, userID snowflake.ID) (discord.Member, bool)
	FindMembersByName(ctx context.Context, guildID snowflake.ID, name string) ([]discord.Member, error)
}

// maxAmbiguousPrompts caps the confirmation messages posted for one message,
// maxCandidates the members offered in each.
const (
	maxAmbiguousPrompts = 10
	maxCandidates       = 4
)

// AccessChannelEnabled reports whether the bot must read the access channel,
// i.e. whether the Message Content intent is needed.
func (h *CommandHandlers) AccessChannelEnabled() bool {
	a, ok := h.rvr.(AccessChannelService)
	return ok && a.AccessChannelEnabled()
}

// OnAccessMessage handles a message posted in the access channel by a board
// member: grants (or, starting with "retirer", revokes) the access role of
// every mentioned or pasted member, then answers with a summary.
func (h *CommandHandlers) OnAccessMessage(e *events.GuildMessageCreate) {
	a, ok := h.rvr.(AccessChannelService)
	if !ok || !a.AccessChannelEnabled() || e.Message.Author.Bot || !a.IsAccessChannel(e.GuildID, e.ChannelID) {
		return
	}
	go h.handleAccessMessage(a, e.Client().Rest, e.GuildID, e.ChannelID, e.Message)
}

func (h *CommandHandlers) handleAccessMessage(a AccessChannelService, r rest.Rest, guildID, channelID snowflake.ID, msg discord.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	loc := h.loc(guildID, "")
	reply := func(content string) {
		if _, err := r.CreateMessage(channelID, discord.MessageCreate{
			Content:          content,
			AllowedMentions:  &discord.AllowedMentions{},
			MessageReference: &discord.MessageReference{MessageID: &msg.ID, FailIfNotExists: false},
		}); err != nil {
			slog.Warn("access channel: reply failed", slog.String("channelID", channelID.String()), slog.Any("err", err))
		}
	}

	author, ok := a.FindMember(ctx, guildID, msg.Author.ID)
	if !ok || !a.CanManageAccess(guildID, author.RoleIDs, a.MemberPermissions(author)) {
		reply(loc.T("access.denied"))
		return
	}

	req := manager.ParseAccessMessage(msg.Content)
	if len(req.IDs) == 0 && len(req.Names) == 0 {
		reply(loc.T("access.batch.nothing"))
		return
	}
	_ = r.AddReaction(channelID, msg.ID, "⏳")

	type target struct {
		label  string
		member discord.Member
		found  bool
	}
	var targets []target
	for _, id := range req.IDs {
		mb, found := a.FindMember(ctx, guildID, id)
		targets = append(targets, target{label: fmt.Sprintf("<@%s>", id), member: mb, found: found})
	}
	type ambiguous struct {
		name       string
		candidates []discord.Member
	}
	var ambiguities []ambiguous
	var lookupFailed []string
	for _, name := range req.Names {
		found, err := a.FindMembersByName(ctx, guildID, name)
		switch {
		case err != nil:
			lookupFailed = append(lookupFailed, name)
		case len(found) == 1:
			targets = append(targets, target{label: fmt.Sprintf("<@%s>", found[0].User.ID), member: found[0], found: true})
		case len(found) > 1:
			ambiguities = append(ambiguities, ambiguous{name: name, candidates: found})
		default:
			targets = append(targets, target{label: name})
		}
	}
	truncated := len(targets) > manager.MaxAccessTargets
	if truncated {
		targets = targets[:manager.MaxAccessTargets]
	}

	var changed, kicked, already, notFound, failed []string
	for _, name := range lookupFailed {
		failed = append(failed, name+" (recherche)")
	}
	for _, t := range targets {
		if !t.found {
			notFound = append(notFound, t.label)
			continue
		}
		res, err := a.SetRvRAccess(ctx, guildID, author.User.ID, t.member.User.ID, t.member.RoleIDs, t.member.User.Bot, !req.Revoke)
		switch {
		case err != nil:
			failed = append(failed, t.label+" ("+shortErr(err)+")")
		case res.Changed:
			changed = append(changed, t.label)
		default:
			already = append(already, t.label)
		}
		if err == nil && res.Disconnected {
			kicked = append(kicked, t.label)
		}
	}

	var lines []string
	add := func(key string, list []string) {
		if len(list) > 0 {
			lines = append(lines, loc.T(key, "List", strings.Join(list, ", ")))
		}
	}
	if req.Revoke {
		add("access.batch.revoked", changed)
		add("access.batch.kicked", kicked)
	} else {
		add("access.batch.granted", changed)
	}
	add("access.batch.already", already)
	add("access.batch.not_found", notFound)
	add("access.batch.failed", failed)
	if truncated {
		lines = append(lines, loc.T("access.batch.truncated", "Max", manager.MaxAccessTargets))
	}
	if len(ambiguities) > 0 {
		names := make([]string, 0, len(ambiguities))
		for _, amb := range ambiguities {
			names = append(names, amb.name)
		}
		lines = append(lines, loc.T("access.batch.ambiguous", "List", strings.Join(names, ", ")))
	}
	if len(lines) > 0 {
		reply(truncateDiscord(strings.Join(lines, "\n")))
	}

	// Same display name for several members: the board picks with a button.
	mode := "g"
	if req.Revoke {
		mode = "r"
	}
	for i, amb := range ambiguities {
		if i == maxAmbiguousPrompts {
			break
		}
		var buttons []discord.InteractiveComponent
		var who []string
		for j, c := range amb.candidates {
			if j == maxCandidates {
				break
			}
			buttons = append(buttons, discord.NewPrimaryButton(
				truncateLabel(fmt.Sprintf("%s (%s)", manager.DisplayName(c), c.User.Username)),
				fmt.Sprintf("/access/pick/%s/%s", mode, c.User.ID)))
			who = append(who, fmt.Sprintf("<@%s> (%s)", c.User.ID, c.User.Username))
		}
		buttons = append(buttons, discord.NewSecondaryButton(loc.T("access.pick.none"), "/access/skip"))
		key := "access.pick.prompt_grant"
		if req.Revoke {
			key = "access.pick.prompt_revoke"
		}
		if _, err := r.CreateMessage(channelID, discord.MessageCreate{
			Content:          loc.T(key, "Name", amb.name, "Count", len(amb.candidates), "List", strings.Join(who, ", ")),
			AllowedMentions:  &discord.AllowedMentions{},
			MessageReference: &discord.MessageReference{MessageID: &msg.ID},
			Components:       []discord.LayoutComponent{discord.NewActionRow(buttons...)},
		}); err != nil {
			slog.Warn("access channel: confirmation prompt failed", slog.Any("err", err))
		}
	}

	_ = r.RemoveOwnReaction(channelID, msg.ID, "⏳")
	mark := "✅"
	if len(notFound)+len(failed)+len(ambiguities) > 0 {
		mark = "⚠️"
	}
	_ = r.AddReaction(channelID, msg.ID, mark)
	slog.Info("access channel message handled",
		slog.String("guildID", guildID.String()),
		slog.String("by", author.User.ID.String()),
		slog.Bool("revoke", req.Revoke),
		slog.Int("changed", len(changed)),
		slog.Int("already", len(already)),
		slog.Int("notFound", len(notFound)),
		slog.Int("failed", len(failed)),
		slog.Int("ambiguous", len(ambiguities)),
	)
}

func truncateLabel(s string) string {
	const limit = 80 // Discord button label limit
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

func shortErr(err error) string {
	switch {
	case errors.Is(err, manager.ErrAccessPermission):
		return "permission"
	case errors.Is(err, manager.ErrAccessTargetBot):
		return "bot"
	case errors.Is(err, manager.ErrAccessRoleMissing):
		return "rôle introuvable"
	}
	s := err.Error()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// truncateDiscord keeps a message under Discord's 2000 character limit.
func truncateDiscord(s string) string {
	const limit = 1990
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// handleAccessPick applies the board's choice among members sharing a name.
func (h *CommandHandlers) handleAccessPick(a AccessService) func(discord.ButtonInteractionData, *handler.ComponentEvent) error {
	return func(_ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
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
		grant := e.Vars["mode"] != "r"
		targetID, err := snowflake.Parse(e.Vars["userID"])
		if err != nil {
			return e.CreateMessage(ephemeral(loc.T("access.failed", "Err", err.Error())))
		}
		if err := e.DeferUpdateMessage(); err != nil {
			return err
		}
		var roles []snowflake.ID
		isBot := false
		if cs, ok := a.(AccessChannelService); ok {
			if mb, found := cs.FindMember(e.Ctx, guildID, targetID); found {
				roles, isBot = mb.RoleIDs, mb.User.Bot
			}
		}
		res, err := a.SetRvRAccess(e.Ctx, guildID, actor.User.ID, targetID, roles, isBot, grant)
		mention := fmt.Sprintf("<@%s>", targetID)
		role := fmt.Sprintf("<@&%s>", res.RoleID)
		var msg string
		switch {
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
		msg += " " + loc.T("access.pick.by", "Who", fmt.Sprintf("<@%s>", actor.User.ID))
		empty := []discord.LayoutComponent{}
		_, err = e.UpdateInteractionResponse(discord.MessageUpdate{
			Content:         &msg,
			Components:      &empty,
			AllowedMentions: &discord.AllowedMentions{},
		})
		return err
	}
}

// handleAccessSkip closes a confirmation prompt without changing anything.
func (h *CommandHandlers) handleAccessSkip(a AccessService) func(discord.ButtonInteractionData, *handler.ComponentEvent) error {
	return func(_ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
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
		msg := loc.T("access.pick.skipped", "Who", fmt.Sprintf("<@%s>", actor.User.ID))
		empty := []discord.LayoutComponent{}
		return e.UpdateMessage(discord.MessageUpdate{Content: &msg, Components: &empty, AllowedMentions: &discord.AllowedMentions{}})
	}
}
