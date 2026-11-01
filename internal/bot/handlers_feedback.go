package bot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// Squad lead return: the speakers open, from the panel, a private set of
// buttons (one per listener channel plus cut all / restore all) that cuts or
// restores the voice of that channel's squad lead in the speaker channel.

const (
	panelFeedbackID = "/panel/feedback"
	feedbackID      = "/panel/fb"
)

// feedbackControl is implemented by the manager. Asserted so the existing
// test doubles stay valid.
type feedbackControl interface {
	ListenerFeeds(guildID snowflake.ID) []manager.ListenerFeed
	SetListenerMuted(guildID, channelID snowflake.ID, muted bool) bool
	CanControlFeedback(guildID snowflake.ID, roleIDs []snowflake.ID, isManager bool) bool
}

func (h *CommandHandlers) feedback() (feedbackControl, bool) {
	f, ok := h.rvr.(feedbackControl)
	return f, ok
}

// feedbackAvailable reports whether the panel offers the return controls:
// a running session with squad leads and listener channels.
func (h *CommandHandlers) feedbackAvailable(guildID snowflake.ID) bool {
	f, ok := h.feedback()
	if !ok || h.rvr == nil || !h.rvr.SquadLeadsEnabled(guildID) {
		return false
	}
	return len(f.ListenerFeeds(guildID)) > 0
}

func (h *CommandHandlers) canControlFeedback(guildID snowflake.ID, f feedbackControl, member *discord.ResolvedMember) bool {
	if member == nil {
		return false
	}
	isManager := member.Permissions.Has(discord.PermissionAdministrator) || member.Permissions.Has(discord.PermissionManageGuild)
	return f.CanControlFeedback(guildID, member.RoleIDs, isManager)
}

// handlePanelFeedback opens the return controls, for the asker only.
func (h *CommandHandlers) handlePanelFeedback(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	f, ok := h.feedback()
	if !ok || !h.feedbackAvailable(guildID) {
		return e.CreateMessage(ephemeral(loc.T("feedback.none")))
	}
	if !h.canControlFeedback(guildID, f, e.Member()) {
		return e.CreateMessage(ephemeral(loc.T("feedback.denied")))
	}
	msg := h.buildFeedbackControls(guildID, loc, f)
	msg.Flags = discord.MessageFlagEphemeral
	return e.CreateMessage(msg)
}

// handleFeedbackToggle cuts or restores one channel ("/panel/fb/{channelID}")
// or all of them ("/panel/fb/all/cut", "/panel/fb/all/on"), then redraws the
// controls and the panel.
func (h *CommandHandlers) handleFeedbackToggle(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	f, ok := h.feedback()
	if !ok {
		return e.CreateMessage(ephemeral(loc.T("feedback.none")))
	}
	if !h.canControlFeedback(guildID, f, e.Member()) {
		return e.CreateMessage(ephemeral(loc.T("feedback.denied")))
	}
	target := e.Vars["target"]
	switch {
	case target == "all":
		f.SetListenerMuted(guildID, 0, e.Vars["state"] == "cut")
	default:
		ch, err := snowflake.Parse(target)
		if err != nil {
			return e.CreateMessage(ephemeral(loc.T("feedback.none")))
		}
		muted := false
		for _, feed := range f.ListenerFeeds(guildID) {
			if feed.ChannelID == ch {
				muted = feed.Muted
			}
		}
		if !f.SetListenerMuted(guildID, ch, !muted) {
			return e.CreateMessage(ephemeral(loc.T("feedback.none")))
		}
	}
	h.notifyPanel(guildID)
	msg := h.buildFeedbackControls(guildID, loc, f)
	return e.UpdateMessage(discord.MessageUpdate{Content: &msg.Content, Embeds: &msg.Embeds, Components: &msg.Components})
}

// feedbackLabel is the button text of a listener channel.
func feedbackLabel(loc *i18n.Localizer, f manager.ListenerFeed, i int) string {
	name := f.Name
	if name == "" {
		name = fmt.Sprintf("%d", i+1)
	}
	if f.Muted {
		return loc.T("feedback.btn.cut", "Channel", name)
	}
	return loc.T("feedback.btn.on", "Channel", name)
}

// buildFeedbackControls is the private message: one button per listener
// channel (green: return on, red: cut), then cut all and restore all.
func (h *CommandHandlers) buildFeedbackControls(guildID snowflake.ID, loc *i18n.Localizer, f feedbackControl) discord.MessageCreate {
	feeds := f.ListenerFeeds(guildID)
	var rows []discord.LayoutComponent
	var row []discord.InteractiveComponent
	for i, feed := range feeds {
		if len(rows) == 4 { // the last row is for cut all / restore all
			break
		}
		id := feedbackID + "/" + feed.ChannelID.String() + "/toggle"
		b := discord.NewSuccessButton(feedbackLabel(loc, feed, i), id)
		if feed.Muted {
			b = discord.NewDangerButton(feedbackLabel(loc, feed, i), id)
		}
		row = append(row, b)
		if len(row) == 5 {
			rows = append(rows, discord.NewActionRow(row...))
			row = nil
		}
	}
	if len(row) > 0 && len(rows) < 4 {
		rows = append(rows, discord.NewActionRow(row...))
	}
	rows = append(rows, discord.NewActionRow(
		discord.NewDangerButton(loc.T("feedback.btn.cut_all"), feedbackID+"/all/cut"),
		discord.NewSuccessButton(loc.T("feedback.btn.on_all"), feedbackID+"/all/on"),
	))
	return discord.MessageCreate{
		Embeds: []discord.Embed{{
			Title:       loc.T("feedback.title"),
			Description: loc.T("feedback.desc"),
			Color:       panelColorIdle,
		}},
		Components:      rows,
		AllowedMentions: &discord.AllowedMentions{},
	}
}

// feedbackSummary is the panel line about the returns, "" when all are on.
func feedbackSummary(loc *i18n.Localizer, feeds []manager.ListenerFeed) string {
	var cut []string
	for i, f := range feeds {
		if f.Muted {
			name := f.Name
			if name == "" {
				name = fmt.Sprintf("%d", i+1)
			}
			cut = append(cut, name)
		}
	}
	switch {
	case len(cut) == 0:
		return loc.T("feedback.summary.on")
	case len(cut) == len(feeds):
		return loc.T("feedback.summary.all_cut")
	}
	return loc.T("feedback.summary.cut", "Channels", strings.Join(cut, ", "))
}
