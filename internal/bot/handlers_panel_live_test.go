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
	st manager.PanelStatus
}

func (f liveRvR) PanelStatus(snowflake.ID) manager.PanelStatus { return f.st }
func (f liveRvR) AccessEnabled() bool                          { return true }
func (f liveRvR) AccessFaction() string                        { return "" }
func (f liveRvR) CanManageAccess(snowflake.ID, []snowflake.ID, discord.Permissions) bool {
	return false
}
func (f liveRvR) SetRvRAccess(context.Context, snowflake.ID, snowflake.ID, snowflake.ID, []snowflake.ID, bool, bool) (manager.AccessResult, error) {
	return manager.AccessResult{}, nil
}

func panelButtons(t *testing.T, msg discord.MessageCreate) []string {
	t.Helper()
	row, ok := msg.Components[0].(discord.ActionRowComponent)
	if !ok {
		t.Fatalf("first row %#v", msg.Components[0])
	}
	var ids []string
	for _, c := range row.Components {
		ids = append(ids, c.(discord.ButtonComponent).CustomID)
	}
	return ids
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
	if got := strings.Join(panelButtons(t, msg), " "); got != panelStartID+" "+panelStatusID+" "+panelAccessID {
		t.Fatalf("idle buttons = %s, want Start, Status, Request access", got)
	}
	if !strings.Contains(msg.Content, "No raid running") {
		t.Fatalf("idle content %q", msg.Content)
	}

	since := time.Unix(1793550000, 0)
	h.SetRvR(liveRvR{st: manager.PanelStatus{
		Running:  true,
		Since:    since,
		RaidLead: manager.ChannelCount{ChannelID: 7, Members: 2},
		Squads:   []manager.ChannelCount{{ChannelID: 8, Members: 5}, {ChannelID: 9, Members: 0}},
	}})
	msg = h.buildPanel(1, loc)
	if got := strings.Join(panelButtons(t, msg), " "); got != panelStopID+" "+panelStatusID+" "+panelAccessID {
		t.Fatalf("running buttons = %s, want Stop, Status, Request access", got)
	}
	for _, want := range []string{"Raid running", "<t:1793550000:R>", "<#7> **2**", "<#8> **5**", "<#9> **0**"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("running content lacks %q:\n%s", want, msg.Content)
		}
	}
}

func TestPanelInFrench(t *testing.T) {
	bundle, _ := i18n.NewBundle()
	h := &CommandHandlers{bundle: bundle}
	h.SetRvR(liveRvR{})
	msg := h.buildPanel(1, bundle.For("fr", ""))
	if !strings.Contains(msg.Content, "Aucun raid en cours") {
		t.Fatalf("French panel %q", msg.Content)
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
