package bot

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// liveRvR reports a raid state and has access management on.
type liveRvR struct {
	fakeRvR
	st   manager.PanelStatus
	open bool
}

func (f liveRvR) PanelStatus(snowflake.ID) manager.PanelStatus { return f.st }
func (f liveRvR) AccessRoleID(snowflake.ID) snowflake.ID       { return 902 }
func (f liveRvR) ApproverRoleIDs(snowflake.ID) []snowflake.ID  { return []snowflake.ID{900, 901} }
func (f liveRvR) AccessOpen(snowflake.ID) bool                 { return f.open }
func (f liveRvR) SetAccessOpen(context.Context, snowflake.ID, bool) (int, error) {
	return 0, nil
}
func (f liveRvR) AccessEnabled() bool { return true }
func (f liveRvR) CanManageAccess(snowflake.ID, []snowflake.ID, discord.Permissions) bool {
	return false
}
func (f liveRvR) SetRvRAccess(context.Context, snowflake.ID, snowflake.ID, snowflake.ID, []snowflake.ID, bool, bool) (manager.AccessResult, error) {
	return manager.AccessResult{}, nil
}

func TestPanelShowsLiveState(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	loc := bundle.For("", "") // server without a pinned language: English
	h := &CommandHandlers{bundle: bundle}

	h.SetRvR(liveRvR{})
	msg := h.buildPanel(1, loc)
	if got := strings.Join(panelButtonIDs(msg), " "); got != panelStartID+" "+panelStatusID+" "+panelHelpID {
		t.Fatalf("idle buttons = %s, want Start, Status, Help only: every member holds the access role", got)
	}
	if len(msg.Components) != 1 {
		t.Fatalf("rows = %d, want the session row only", len(msg.Components))
	}
	text := panelText(msg)
	if strings.Contains(text, "Request access") {
		t.Fatalf("the panel must not tell members to request access:\n%s", text)
	}
	for _, want := range []string{"No session running"} {
		if !strings.Contains(text, want) {
			t.Fatalf("idle panel lacks %q:\n%s", want, text)
		}
	}
	if msg.Embeds[0].Color != panelColorIdle {
		t.Error("idle colour expected")
	}

	since := time.Unix(1793550000, 0)
	h.SetRvR(liveRvR{st: manager.PanelStatus{
		Running:  true,
		Since:    since,
		RaidLead: manager.ChannelCount{ChannelID: 7, Members: 2},
		Squads:   []manager.ChannelCount{{ChannelID: 8, Members: 5}, {ChannelID: 9, Members: 0}},
	}})
	msg = h.buildPanel(1, loc)
	if got := strings.Join(panelButtonIDs(msg), " "); got != panelStopID+" "+panelStatusID+" "+panelHelpID {
		t.Fatalf("running buttons = %s, want Stop, Status, Help", got)
	}
	text = panelText(msg)
	for _, want := range []string{"Session running", "<t:1793550000:R>", "<#7>  **2**", "<#8>  **5**", "<#9>  **0**"} {
		if !strings.Contains(text, want) {
			t.Fatalf("running panel lacks %q:\n%s", want, text)
		}
	}
	if msg.Embeds[0].Color != panelColorRunning {
		t.Error("running colour expected")
	}
}

func TestPanelInFrench(t *testing.T) {
	bundle, _ := i18n.NewBundle()
	h := &CommandHandlers{bundle: bundle}
	h.SetRvR(liveRvR{})
	msg := h.buildPanel(1, bundle.For("fr", ""))
	if !strings.Contains(panelText(msg), "Aucune session en cours") {
		t.Fatalf("French panel %q", panelText(msg))
	}
}

func TestAccessRequestsOnePerMember(t *testing.T) {
	var r accessRequests
	now := time.Now()
	if !r.begin(1, 42, now) {
		t.Fatal("first request refused")
	}
	if r.begin(1, 42, now.Add(time.Minute)) {
		t.Fatal("a second pending request must be refused")
	}
	if !r.begin(2, 42, now) {
		t.Fatal("another server is a separate request")
	}
	if !r.begin(1, 42, now.Add(accessRequestTTL+time.Second)) {
		t.Fatal("an unanswered request must expire")
	}
	r.end(1, 42)
	if !r.begin(1, 42, now) {
		t.Fatal("after a decision the member may ask again")
	}
}

func TestPanelHelpButton(t *testing.T) {
	bundle, _ := i18n.NewBundle()
	h := &CommandHandlers{bundle: bundle}
	h.SetRvR(liveRvR{})
	loc := bundle.For("", "")
	msg := h.buildPanel(1, loc)
	if !strings.Contains(strings.Join(panelButtonIDs(msg), " "), panelHelpID) {
		t.Fatal("the panel must offer a Help button")
	}
	help := h.buildPanelHelp(1, loc)
	if help.Flags&discord.MessageFlagEphemeral == 0 {
		t.Fatal("the help must only be visible to the person who asked")
	}
	text := panelText(help)
	if strings.Contains(text, "Request access") {
		t.Fatal("no access to request any more: the help must not mention it")
	}
	for _, want := range []string{"What each button does", "Start session / Stop session", "Who does what"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help lacks %q:\n%s", want, text)
		}
	}
}

type squadRvR struct {
	liveRvR
	capacity, target int
}

func (f squadRvR) DynamicChannels() bool            { return true }
func (f squadRvR) SquadCapacity(snowflake.ID) int   { return f.capacity }
func (f squadRvR) SquadTarget(snowflake.ID) int     { return f.target }
func (f squadRvR) SetSquadTarget(snowflake.ID, int) {}

func TestPanelSquadSelect(t *testing.T) {
	bundle, _ := i18n.NewBundle()
	h := &CommandHandlers{bundle: bundle}
	loc := bundle.For("", "")
	h.SetRvR(squadRvR{capacity: 11, target: 7})
	msg := h.buildPanel(1, loc)
	last := msg.Components[len(msg.Components)-1].(discord.ActionRowComponent)
	menu, ok := last.Components[0].(discord.StringSelectMenuComponent)
	if !ok || menu.CustomID != panelSquadsID || len(menu.Options) != 11 {
		t.Fatalf("squad select %#v", last.Components[0])
	}
	if !menu.Options[6].Default || menu.Options[0].Label != "1 listener channel" || menu.Options[10].Label != "11 listener channels" {
		t.Fatalf("options %+v", menu.Options)
	}
	if !strings.Contains(panelText(msg), "7 at the next start, 11 relay bots available") {
		t.Fatalf("panel %q", panelText(msg))
	}
	// One relay bot only: nothing to choose.
	h.SetRvR(squadRvR{capacity: 1, target: 1})
	for _, r := range h.buildPanel(1, loc).Components {
		for _, c := range r.(discord.ActionRowComponent).Components {
			if _, isMenu := c.(discord.StringSelectMenuComponent); isMenu {
				t.Fatal("no squad choice with a single relay bot")
			}
		}
	}
}

type feedbackRvR struct {
	liveRvR
	feeds []manager.ListenerFeed
}

func (f feedbackRvR) SquadLeadsEnabled(snowflake.ID) bool                    { return true }
func (f feedbackRvR) ListenerFeeds(snowflake.ID) []manager.ListenerFeed      { return f.feeds }
func (f feedbackRvR) SetListenerMuted(snowflake.ID, snowflake.ID, bool) bool { return true }
func (f feedbackRvR) CanControlFeedback(snowflake.ID, []snowflake.ID, bool) bool {
	return true
}
func (f feedbackRvR) SquadLeadKeyAlways() bool { return true }

func TestSquadLeadReturnControls(t *testing.T) {
	bundle, _ := i18n.NewBundle()
	loc := bundle.For("", "")
	h := &CommandHandlers{bundle: bundle}
	feeds := []manager.ListenerFeed{{ChannelID: 81, Name: "Listener 1"}, {ChannelID: 82, Name: "Listener 2", Muted: true}}
	for i := 3; i <= 11; i++ {
		feeds = append(feeds, manager.ListenerFeed{ChannelID: snowflake.ID(80 + i), Name: "Listener " + strconv.Itoa(i)})
	}
	h.SetRvR(feedbackRvR{liveRvR: liveRvR{st: manager.PanelStatus{Running: true}}, feeds: feeds})

	msg := h.buildPanel(1, loc)
	if !strings.Contains(strings.Join(panelButtonIDs(msg), " "), panelFeedbackID) {
		t.Fatal("a running session with squad leads must offer the return controls")
	}
	if !strings.Contains(panelText(msg), "Cut in: Listener 2") || !strings.Contains(panelText(msg), "heard in the speaker channel whenever they talk") {
		t.Fatalf("panel %q", panelText(msg))
	}

	ctl := h.buildFeedbackControls(1, loc, feedbackRvR{feeds: feeds})
	var labels []string
	for _, r := range ctl.Components {
		for _, c := range r.(discord.ActionRowComponent).Components {
			b := c.(discord.ButtonComponent)
			labels = append(labels, b.Label+"="+b.CustomID)
		}
	}
	all := strings.Join(labels, " | ")
	for _, want := range []string{"Listener 1: on=/panel/fb/81/toggle", "Listener 2: cut=/panel/fb/82/toggle", "Listener 11: on=/panel/fb/91/toggle", "Cut all=/panel/fb/all/cut", "Restore all=/panel/fb/all/on"} {
		if !strings.Contains(all, want) {
			t.Fatalf("controls lack %q: %s", want, all)
		}
	}
	if len(ctl.Components) > 5 {
		t.Fatalf("%d rows, Discord allows 5", len(ctl.Components))
	}

	// Stopped: no return controls.
	h.SetRvR(feedbackRvR{liveRvR: liveRvR{}, feeds: feeds})
	if strings.Contains(strings.Join(panelButtonIDs(h.buildPanel(1, loc)), " "), panelFeedbackID) {
		t.Fatal("no return controls without a running session")
	}
}
