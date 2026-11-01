package bot

import (
	"context"
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
func (f liveRvR) AccessEnabled() bool   { return true }
func (f liveRvR) AccessFaction() string { return "" }
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
	if got := strings.Join(panelButtonIDs(msg), " "); got != panelStartID+" "+panelStatusID+" "+panelHelpID+" "+panelAccessID+" "+panelOpenID {
		t.Fatalf("idle buttons = %s, want Start, Status | Request access, Open", got)
	}
	if len(msg.Components) != 2 {
		t.Fatalf("rows = %d, want raid row and players row", len(msg.Components))
	}
	text := panelText(msg)
	for _, want := range []string{"No raid running", "Restricted", "<@&902>"} {
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
	if got := strings.Join(panelButtonIDs(msg), " "); got != panelStopID+" "+panelStatusID+" "+panelHelpID+" "+panelAccessID+" "+panelOpenID {
		t.Fatalf("running buttons = %s, want Stop, Status | Request access, Open", got)
	}
	text = panelText(msg)
	for _, want := range []string{"Raid running", "<t:1793550000:R>", "<#7>  **2**", "<#8>  **5**", "<#9>  **0**"} {
		if !strings.Contains(text, want) {
			t.Fatalf("running panel lacks %q:\n%s", want, text)
		}
	}
	if msg.Embeds[0].Color != panelColorRunning {
		t.Error("running colour expected")
	}

	// Open to everyone: no request button, the switch offers to restrict.
	h.SetRvR(liveRvR{open: true})
	msg = h.buildPanel(1, loc)
	if got := strings.Join(panelButtonIDs(msg), " "); got != panelStartID+" "+panelStatusID+" "+panelHelpID+" "+panelRestrictID {
		t.Fatalf("open buttons = %s, want Start, Status | Restrict", got)
	}
	if !strings.Contains(panelText(msg), "Open: anyone") {
		t.Fatalf("open panel %q", panelText(msg))
	}
}

func TestPanelInFrench(t *testing.T) {
	bundle, _ := i18n.NewBundle()
	h := &CommandHandlers{bundle: bundle}
	h.SetRvR(liveRvR{})
	msg := h.buildPanel(1, bundle.For("fr", ""))
	if !strings.Contains(panelText(msg), "Aucun raid en cours") {
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
	for _, want := range []string{"What each button does", "Start raid / Stop raid", "Request access", "Open to everyone / Restrict access", "Who does what"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help lacks %q:\n%s", want, text)
		}
	}
}
