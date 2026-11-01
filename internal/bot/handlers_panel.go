package bot

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/i18n"
)

// Control panel: a message with buttons, so raid leads start and stop the
// relay without typing commands or choosing a mode. Custom IDs carry no
// state, so the buttons keep working after a bot restart.
const (
	panelStartID  = "/panel/start"
	panelStopID   = "/panel/stop"
	panelStatusID = "/panel/status"
)

// registerPanel attaches the panel command and button routes.
func (h *CommandHandlers) registerPanel(r handler.Router) {
	r.SlashCommand("/panel", h.withManager(h.handlePanel))
	r.ButtonComponent(panelStartID, h.withManagerButton(h.handlePanelStart))
	r.ButtonComponent(panelStopID, h.withManagerButton(h.handlePanelStop))
	r.ButtonComponent(panelStatusID, h.withGuildButton(h.handlePanelStatus))
}

// withManagerButton requires Manage Server permission or the manager role,
// like the slash commands the buttons replace.
func (h *CommandHandlers) withManagerButton(fn guildButtonHandler) func(discord.ButtonInteractionData, *handler.ComponentEvent) error {
	return h.withGuildButton(func(guildID snowflake.ID, loc *i18n.Localizer, data discord.ButtonInteractionData, e *handler.ComponentEvent) error {
		if !h.isManagerAuthorized(guildID, e.Member()) {
			return e.CreateMessage(ephemeral(loc.T("err.need_manager")))
		}
		return fn(guildID, loc, data, e)
	})
}

// handlePanel posts the control panel in the current channel, visible to all.
func (h *CommandHandlers) handlePanel(guildID snowflake.ID, loc *i18n.Localizer, _ discord.SlashCommandInteractionData, e *handler.CommandEvent) error {
	return e.CreateMessage(h.buildPanel(guildID, loc))
}

func (h *CommandHandlers) buildPanel(guildID snowflake.ID, loc *i18n.Localizer) discord.MessageCreate {
	buttons := []discord.InteractiveComponent{
		discord.NewSuccessButton(loc.T("panel.btn.start"), panelStartID),
		discord.NewDangerButton(loc.T("panel.btn.stop"), panelStopID),
		discord.NewSecondaryButton(loc.T("panel.btn.status"), panelStatusID),
	}
	sorter, sortOn := h.classSort()
	if sortOn {
		buttons = append(buttons, discord.NewPrimaryButton(loc.T("panel.btn.sort"), panelSortID))
	}
	rows := []discord.LayoutComponent{discord.NewActionRow(buttons...)}
	content := loc.T("panel.content")
	if h.dynamicChannels() {
		content += "\n" + loc.T("panel.content_channels", "Category", h.rvr.ChannelCategory())
	}
	if h.rvr != nil && h.rvr.SquadLeadsEnabled(guildID) {
		content += "\n" + loc.T("panel.content_squads")
	}
	if sortOn {
		content += "\n" + loc.T("panel.content_sort", "Waiting", sorter.ClassSortWaiting())
	}
	return discord.MessageCreate{
		Content:         content,
		Components:      rows,
		AllowedMentions: &discord.AllowedMentions{},
	}
}

func (h *CommandHandlers) handlePanelStart(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	if err := e.DeferCreateMessage(true); err != nil {
		return err
	}
	h.rememberReportChannel(guildID, e.Channel())
	h.startHostRaid(e.Ctx, guildID, loc, h.panelMode(guildID), func(msg string) { h.componentFollowUp(e, msg) })
	return nil
}

// squadBroadcaster is implemented by the manager when squad leads may also
// be relayed to the other squads. Asserted so RvRService test doubles stay valid.
type squadBroadcaster interface {
	SquadLeadsToSquads() bool
}

// panelMode picks the raid mode for the Start button. With a squad lead role:
// star topology (raid leads to every squad, squad leads to the raid leads
// only), or mix-minus when SQUAD_LEADS_TO_SQUADS is set (squad leads heard in
// the other squads too). One-caller otherwise. All of them mix several raid
// leads.
func (h *CommandHandlers) panelMode(guildID snowflake.ID) guild.RaidMode {
	if h.rvr != nil && h.rvr.SquadLeadsEnabled(guildID) {
		if b, ok := h.rvr.(squadBroadcaster); ok && b.SquadLeadsToSquads() {
			return guild.RaidModeGuildCaller
		}
		return guild.RaidModeOneManyGuildCaller
	}
	return guild.RaidModeOneCaller
}

func (h *CommandHandlers) handlePanelStop(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	if err := e.DeferCreateMessage(true); err != nil {
		return err
	}
	h.stopRaid(e.Ctx, guildID, loc, func(msg string) { h.componentFollowUp(e, msg) })
	return nil
}

func (h *CommandHandlers) handlePanelStatus(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	status := h.manager.GetStatus(guildID)
	return e.CreateMessage(ephemeral(status.Render(loc)))
}

// componentFollowUp sends an ephemeral follow-up after a deferred button response.
func (h *CommandHandlers) componentFollowUp(e *handler.ComponentEvent, content string) {
	if _, err := e.CreateFollowupMessage(ephemeral(content)); err != nil {
		logFollowUpError(err)
	}
}
