package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

type fakeRvR struct{ squads, dynamic bool }

func (f fakeRvR) PlayOutro(snowflake.ID)              {}
func (f fakeRvR) SquadLeadsEnabled(snowflake.ID) bool { return f.squads }
func (f fakeRvR) DynamicChannels() bool               { return f.dynamic }
func (f fakeRvR) ChannelCategory() string             { return "RvR Asmodiens" }
func (f fakeRvR) RaidChannelsOf(snowflake.ID) (manager.RaidChannels, bool) {
	return manager.RaidChannels{RaidLead: 7, Squads: []snowflake.ID{8, 9}}, f.dynamic
}
func (f fakeRvR) ReleaseRaidChannels(context.Context, snowflake.ID) bool { return false }

func TestBuildPanel(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	h := &CommandHandlers{bundle: bundle}
	h.SetRvR(fakeRvR{squads: true})
	msg := h.buildPanel(1, bundle.For("fr", ""))

	if len(msg.Components) != 1 {
		t.Fatalf("rows = %d, want 1 (controls only)", len(msg.Components))
	}
	controls, ok := msg.Components[0].(discord.ActionRowComponent)
	if !ok || len(controls.Components) != 4 {
		t.Fatalf("first row %#v", msg.Components[0])
	}
	start := controls.Components[0].(discord.ButtonComponent)
	if start.CustomID != panelStartID || start.Label != "Lancer le raid" {
		t.Fatalf("start button %+v", start)
	}
	if !strings.Contains(panelText(msg), "Chefs d'escouade") {
		t.Fatalf("content %q", panelText(msg))
	}
}

func TestBuildPanelDynamicChannels(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	h := &CommandHandlers{bundle: bundle}
	h.SetRvR(fakeRvR{dynamic: true})
	loc := bundle.For("fr", "")
	if msg := h.buildPanel(1, loc); !strings.Contains(panelText(msg), "RvR Asmodiens") {
		t.Fatalf("panel must name the category: %q", panelText(msg))
	}
	line := h.raidChannelsLine(1, loc)
	if !strings.Contains(line, "<#7>") || !strings.Contains(line, "2 ") {
		t.Fatalf("channels line %q", line)
	}
}

func TestBuildPanelWithoutSquadLeads(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	h := &CommandHandlers{bundle: bundle}
	msg := h.buildPanel(1, bundle.For("en", ""))
	if len(msg.Components) != 1 || strings.Contains(panelText(msg), "Squad leads") {
		t.Fatalf("panel without squad leads: %d rows, %q", len(msg.Components), panelText(msg))
	}
}

func TestPanelMode(t *testing.T) {
	h := &CommandHandlers{}
	if h.panelMode(1) != guild.RaidModeOneCaller {
		t.Fatal("no rvr: one-caller expected")
	}
	h.SetRvR(fakeRvR{squads: true})
	if h.panelMode(1) != guild.RaidModeOneManyGuildCaller {
		t.Fatal("squad leads: star topology expected")
	}
}

type fakeBroadcastRvR struct{ fakeRvR }

func (fakeBroadcastRvR) SquadLeadsToSquads() bool { return true }

func TestPanelModeSquadLeadsToSquads(t *testing.T) {
	h := &CommandHandlers{}
	h.SetRvR(fakeRvR{squads: true})
	if got := h.panelMode(1); got != guild.RaidModeOneManyGuildCaller {
		t.Fatalf("squad leads to raid leads only: mode %s, want star", got)
	}
	h.SetRvR(fakeBroadcastRvR{fakeRvR{squads: true}})
	if got := h.panelMode(1); got != guild.RaidModeGuildCaller {
		t.Fatalf("squad leads heard in every squad: mode %s, want mix-minus guild caller", got)
	}
	h.SetRvR(fakeBroadcastRvR{fakeRvR{squads: false}})
	if got := h.panelMode(1); got != guild.RaidModeOneCaller {
		t.Fatalf("no squad lead role: mode %s, want one caller", got)
	}
}
