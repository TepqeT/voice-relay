package bot

import (
	"fmt"
	"strconv"
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
	panelHelpID     = "/panel/help"
	panelSquadsID   = "/panel/squads"
)

// squadChooser is implemented by the manager: how many squads the next raid
// creates, chosen on the panel within the relay bots available.
type squadChooser interface {
	SquadCapacity(guildID snowflake.ID) int
	SquadTarget(guildID snowflake.ID) int
	SetSquadTarget(guildID snowflake.ID, n int)
}

// maxSelectOptions is Discord's limit of options in one select menu.
const maxSelectOptions = 25

// panelStatusSource is implemented by the manager: the live raid state the
// panel shows. Asserted so RvRService test doubles stay valid.
type panelStatusSource interface {
	PanelStatus(guildID snowflake.ID) manager.PanelStatus
}

// homeCategoryInfo is implemented by the manager when the panel and the raid
// channels share one permanent category.
type homeCategoryInfo interface{ HomeCategoryEnabled() bool }

// squadKeyInfo is implemented by the manager: whether squad leads are
// relayed whenever they talk or only with the priority key.
type squadKeyInfo interface{ SquadLeadKeyAlways() bool }

// squadKeyText picks the text matching the squad lead key mode: key for the
// priority key, key+"_always" when squad leads are always relayed.
func (h *CommandHandlers) squadKeyText(key string) string {
	if k, ok := h.rvr.(squadKeyInfo); ok && k.SquadLeadKeyAlways() {
		return key + "_always"
	}
	return key
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
	r.ButtonComponent(panelHelpID, h.withGuildButton(h.handlePanelHelp))
	r.ButtonComponent(panelFeedbackID, h.withGuildButton(h.handlePanelFeedback))
	r.ButtonComponent(feedbackID+"/{target}/{state}", h.withGuildButton(h.handleFeedbackToggle))
	r.SelectMenuComponent(panelSquadsID, h.withGuildSelectMenu(h.handlePanelSquads))
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
	if running && h.feedbackAvailable(guildID) {
		raid = append(raid, discord.NewPrimaryButton(loc.T("panel.btn.feedback"), panelFeedbackID))
	}
	raid = append(raid, discord.NewSecondaryButton(loc.T("panel.btn.help"), panelHelpID))
	rows := []discord.LayoutComponent{discord.NewActionRow(raid...)}

	// Row 2: the players. With an access role every member holds it
	// (member_role.go): no request to make, no mode to switch, the panel is
	// for the Speakers only.
	var players []discord.InteractiveComponent
	if sortOn {
		players = append(players, discord.NewSecondaryButton(loc.T("panel.btn.sort"), panelSortID))
	}
	if len(players) > 0 {
		rows = append(rows, discord.NewActionRow(players...))
	}

	// Row 3: how many squads the next raid creates.
	squads, squadsOn := h.squadChoice(guildID)
	if squadsOn {
		capacity := squads.SquadCapacity(guildID)
		target := squads.SquadTarget(guildID)
		var opts []discord.StringSelectMenuOption
		for n := 1; n <= min(capacity, maxSelectOptions); n++ {
			label := loc.T("panel.squads.option", "N", n)
			if n == 1 {
				label = loc.T("panel.squads.option_one")
			}
			opts = append(opts, discord.NewStringSelectMenuOption(label, strconv.Itoa(n)).WithDefault(n == target))
		}
		rows = append(rows, discord.NewActionRow(
			discord.NewStringSelectMenu(panelSquadsID, loc.T("panel.squads.placeholder", "N", target, "Max", capacity), opts...),
		))
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
	if running && h.feedbackAvailable(guildID) {
		f, _ := h.feedback()
		embed.Fields = append(embed.Fields, discord.EmbedField{Name: loc.T("feedback.title"), Value: feedbackSummary(loc, f.ListenerFeeds(guildID))})
	}
	if squadsOn && !running {
		embed.Fields = append(embed.Fields, discord.EmbedField{Name: loc.T("panel.field.squads"),
			Value: loc.T("panel.squads.value", "N", squads.SquadTarget(guildID), "Max", squads.SquadCapacity(guildID))})
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
		help = append(help, loc.T(h.squadKeyText("panel.help.squads")))
	}
	if sortOn {
		help = append(help, loc.T("panel.help.sort", "Waiting", sorter.ClassSortWaiting()))
	}
	if h.dynamicChannels() {
		if hc, ok := h.rvr.(homeCategoryInfo); ok && hc.HomeCategoryEnabled() {
			help = append(help, loc.T("panel.help.channels_home"))
		} else {
			help = append(help, loc.T("panel.help.channels", "Category", h.rvr.ChannelCategory()))
		}
	}
	embed.Fields = append(embed.Fields, discord.EmbedField{Name: loc.T("panel.field.help"), Value: strings.Join(help, "\n")})

	return discord.MessageCreate{
		Embeds:          []discord.Embed{embed},
		Components:      rows,
		AllowedMentions: &discord.AllowedMentions{},
	}
}

// squadChoice reports whether the panel offers the squad count: raid
// channels created per raid and at least two relay bots to choose from.
func (h *CommandHandlers) squadChoice(guildID snowflake.ID) (squadChooser, bool) {
	c, ok := h.rvr.(squadChooser)
	if !ok || !h.dynamicChannels() || c.SquadCapacity(guildID) < 2 {
		return nil, false
	}
	return c, true
}

// handlePanelSquads saves the squad count picked on the panel. Reserved to
// those who start raids.
func (h *CommandHandlers) handlePanelSquads(guildID snowflake.ID, loc *i18n.Localizer, data discord.SelectMenuInteractionData, e *handler.ComponentEvent) error {
	c, ok := h.squadChoice(guildID)
	sel, ok2 := data.(discord.StringSelectMenuInteractionData)
	if !ok || !ok2 || len(sel.Values) == 0 {
		return e.CreateMessage(ephemeral(loc.T("err.unexpected_data_type")))
	}
	if !h.isManagerAuthorized(guildID, e.Member()) {
		return e.CreateMessage(ephemeral(loc.T("err.need_manager")))
	}
	n, err := strconv.Atoi(sel.Values[0])
	if err != nil || n < 1 || n > c.SquadCapacity(guildID) {
		return e.CreateMessage(ephemeral(loc.T("err.unexpected_data_type")))
	}
	c.SetSquadTarget(guildID, n)
	h.notifyPanel(guildID)
	msg := loc.T("panel.squads.set", "N", n)
	if h.manager.HasActiveSession(guildID) {
		msg = loc.T("panel.squads.set_running", "N", n)
	}
	return e.CreateMessage(ephemeral(msg))
}

// handlePanelHelp answers the Help button with a guide to every button of
// this panel, visible only to the person who asked (Discord lets them
// dismiss it), so the channel stays clean.
func (h *CommandHandlers) handlePanelHelp(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	return e.CreateMessage(h.buildPanelHelp(guildID, loc))
}

func (h *CommandHandlers) buildPanelHelp(guildID snowflake.ID, loc *i18n.Localizer) discord.MessageCreate {
	_, sortOn := h.classSort()
	fields := []discord.EmbedField{
		{Name: loc.T("panel.btn.start") + " / " + loc.T("panel.btn.stop"), Value: loc.T("panel.faq.startstop")},
		{Name: loc.T("panel.btn.status"), Value: loc.T("panel.faq.status")},
	}
	if sortOn {
		fields = append(fields, discord.EmbedField{Name: loc.T("panel.btn.sort"), Value: loc.T("panel.faq.sort")})
	}
	if _, ok := h.squadChoice(guildID); ok {
		fields = append(fields, discord.EmbedField{Name: loc.T("panel.field.squads"), Value: loc.T("panel.faq.squad_count")})
	}
	if h.rvr != nil && h.rvr.SquadLeadsEnabled(guildID) {
		fields = append(fields, discord.EmbedField{Name: loc.T("panel.faq.squads_title"), Value: loc.T(h.squadKeyText("panel.faq.squads"))})
	}
	fields = append(fields, discord.EmbedField{Name: loc.T("panel.faq.roles_title"), Value: loc.T("panel.faq.roles")})
	return discord.MessageCreate{
		Embeds:          []discord.Embed{{Title: loc.T("panel.faq.title"), Color: panelColorIdle, Fields: fields}},
		Flags:           discord.MessageFlagEphemeral,
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
