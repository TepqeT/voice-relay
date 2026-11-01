package bot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// Control panel: a message with buttons, so raid leads start and stop the
// relay without typing commands or choosing a mode. Custom IDs carry no
// state, so the buttons keep working after a bot restart.
const (
	panelStartID    = "/panel/start"
	panelStopID     = "/panel/stop"
	panelStatusID   = "/panel/status"
	panelAccessID   = "/panel/access"
	panelOpenID     = "/panel/open"
	panelRestrictID = "/panel/restrict"
)

// panelStatusSource is implemented by the manager: the live raid state the
// panel shows. Asserted so RvRService test doubles stay valid.
type panelStatusSource interface {
	PanelStatus(guildID snowflake.ID) manager.PanelStatus
}

// OnPanelChange registers fn, called when a raid started or stopped so the
// panel on display is refreshed at once.
func (h *CommandHandlers) OnPanelChange(fn func(guildID snowflake.ID)) { h.panelChanged = fn }

func (h *CommandHandlers) notifyPanel(guildID snowflake.ID) {
	if h.panelChanged != nil {
		h.panelChanged(guildID)
	}
}

// registerPanel attaches the panel command and button routes.
func (h *CommandHandlers) registerPanel(r handler.Router) {
	r.SlashCommand("/panel", h.withManager(h.handlePanel))
	r.ButtonComponent(panelStartID, h.withManagerButton(h.handlePanelStart))
	r.ButtonComponent(panelStopID, h.withManagerButton(h.handlePanelStop))
	r.ButtonComponent(panelStatusID, h.withGuildButton(h.handlePanelStatus))
	r.ButtonComponent(panelAccessID, h.withGuildButton(h.handleAccessRequest))
	r.ButtonComponent(panelOpenID, h.withGuildButton(h.handleAccessMode(true)))
	r.ButtonComponent(panelRestrictID, h.withGuildButton(h.handleAccessMode(false)))
	r.ButtonComponent(accessReqID+"/{decision}/{userID}", h.withGuildButton(h.handleAccessDecision))
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

// Panel colours: Discord blurple when idle, green while a raid runs.
const (
	panelColorIdle    = 0x5865F2
	panelColorRunning = 0x2ECC71
)

// buildPanel lays the panel out as one embed (state, channels, access, how it
// works) and two rows of buttons: the raid itself, then the players.
func (h *CommandHandlers) buildPanel(guildID snowflake.ID, loc *i18n.Localizer) discord.MessageCreate {
	var live *manager.PanelStatus
	if src, ok := h.rvr.(panelStatusSource); ok {
		st := src.PanelStatus(guildID)
		live = &st
	}
	running := live != nil && live.Running
	_, accessOn := h.access()
	mode, modeOn := h.accessMode()
	open := modeOn && accessOn && mode.AccessOpen(guildID)
	sorter, sortOn := h.classSort()

	// Row 1: the raid. One Start or Stop button following the state; both
	// when the state is unknown.
	var raid []discord.InteractiveComponent
	if live == nil || !running {
		raid = append(raid, discord.NewSuccessButton(loc.T("panel.btn.start"), panelStartID))
	}
	if live == nil || running {
		raid = append(raid, discord.NewDangerButton(loc.T("panel.btn.stop"), panelStopID))
	}
	raid = append(raid, discord.NewSecondaryButton(loc.T("panel.btn.status"), panelStatusID))
	rows := []discord.LayoutComponent{discord.NewActionRow(raid...)}

	// Row 2: the players.
	var players []discord.InteractiveComponent
	if accessOn && !open {
		players = append(players, discord.NewPrimaryButton(loc.T("panel.btn.request"), panelAccessID))
	}
	if accessOn && modeOn {
		if open {
			players = append(players, discord.NewSecondaryButton(loc.T("panel.btn.restrict"), panelRestrictID))
		} else {
			players = append(players, discord.NewSecondaryButton(loc.T("panel.btn.open"), panelOpenID))
		}
	}
	if sortOn {
		players = append(players, discord.NewSecondaryButton(loc.T("panel.btn.sort"), panelSortID))
	}
	if len(players) > 0 {
		rows = append(rows, discord.NewActionRow(players...))
	}

	embed := discord.Embed{Title: loc.T("panel.title"), Color: panelColorIdle}
	if running {
		embed.Color = panelColorRunning
	}
	if live != nil {
		embed.Description = panelStateLine(loc, *live)
		if ch := panelChannelLines(*live); ch != "" {
			embed.Fields = append(embed.Fields, discord.EmbedField{Name: loc.T("panel.field.channels"), Value: ch})
		}
	}
	if accessOn {
		value := loc.T("panel.access.open")
		if !open {
			value = loc.T("panel.access.restricted", "Role", h.accessRoleMention(guildID))
		}
		embed.Fields = append(embed.Fields, discord.EmbedField{Name: loc.T("panel.field.access"), Value: value})
	}
	help := []string{loc.T("panel.help.start"), loc.T("panel.help.stop")}
	if h.rvr != nil && h.rvr.SquadLeadsEnabled(guildID) {
		help = append(help, loc.T("panel.help.squads"))
	}
	if sortOn {
		help = append(help, loc.T("panel.help.sort", "Waiting", sorter.ClassSortWaiting()))
	}
	if h.dynamicChannels() {
		help = append(help, loc.T("panel.help.channels", "Category", h.rvr.ChannelCategory()))
	}
	embed.Fields = append(embed.Fields, discord.EmbedField{Name: loc.T("panel.field.help"), Value: strings.Join(help, "\n")})

	return discord.MessageCreate{
		Embeds:          []discord.Embed{embed},
		Components:      rows,
		AllowedMentions: &discord.AllowedMentions{},
	}
}

// panelStateLine says whether a raid runs and since when (Discord renders
// the timestamp in each viewer's language and time zone).
func panelStateLine(loc *i18n.Localizer, st manager.PanelStatus) string {
	if !st.Running {
		return loc.T("panel.live.idle")
	}
	line := loc.T("panel.live.running")
	if !st.Since.IsZero() {
		line += ", " + loc.T("panel.live.since", "Since", fmt.Sprintf("<t:%d:R>", st.Since.Unix()))
	}
	return line
}

// panelChannelLines lists the raid channels with the people in each, one per
// line, "" when the raid has no channels.
func panelChannelLines(st manager.PanelStatus) string {
	var lines []string
	add := func(c manager.ChannelCount) {
		if c.ChannelID != 0 {
			lines = append(lines, fmt.Sprintf("<#%s>  **%d**", c.ChannelID, c.Members))
		}
	}
	add(st.RaidLead)
	for _, c := range st.Squads {
		add(c)
	}
	add(st.Waiting)
	return strings.Join(lines, "\n")
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
