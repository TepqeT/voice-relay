package bot

import (
	"context"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// uninstallManager records the servers uninstalled.
type uninstallManager struct {
	fakeSiteManager
	guilds []manager.GuildRef
	done   []snowflake.ID
}

func (u *uninstallManager) OwnerGuilds() []manager.GuildRef { return u.guilds }
func (u *uninstallManager) Uninstall(_ context.Context, g snowflake.ID) (manager.UninstallReport, error) {
	u.done = append(u.done, g)
	return manager.UninstallReport{OwnerLeft: true}, nil
}

// Voice Relay serves one server: it leaves every other one and keeps that one.
func TestLeaveOtherGuildsKeepsTheOnlyServer(t *testing.T) {
	old, oldGuild := guildsSettle, OnlyGuild
	guildsSettle, OnlyGuild = time.Millisecond, 9001
	t.Cleanup(func() { guildsSettle, OnlyGuild = old, oldGuild })
	m := &uninstallManager{guilds: []manager.GuildRef{{ID: 7001, Name: "A"}, {ID: OnlyGuild, Name: "Kept"}, {ID: 7002, Name: "B"}}}
	b := &Bot{siteMgr: m}
	b.leaveOtherGuilds(context.Background())
	if len(m.done) != 2 || m.done[0] != 7001 || m.done[1] != 7002 {
		t.Fatalf("uninstalled %v, want 7001 and 7002 only", m.done)
	}
	if !hasKind(journal.ForGuild(7001, 5), "single_server_left") {
		t.Error("leave not journaled")
	}

	// A join on the kept server is not undone.
	b.leaveAfterJoin(context.Background(), OnlyGuild)
	if len(m.done) != 2 {
		t.Fatalf("the kept server was uninstalled after a join: %v", m.done)
	}
	if !guildAllowed(OnlyGuild) || guildAllowed(7001) {
		t.Fatal("guildAllowed")
	}
}

// ONLY_GUILD missing or invalid: no server is left, every server is allowed.
func TestOnlyGuildUnsetLeavesNothing(t *testing.T) {
	if parseOnlyGuild("") != 0 || parseOnlyGuild("abc") != 0 || parseOnlyGuild(" 42 ") != 42 {
		t.Fatal("parseOnlyGuild")
	}
	oldSettle, oldGuild := guildsSettle, OnlyGuild
	guildsSettle, OnlyGuild = time.Millisecond, 0
	t.Cleanup(func() { guildsSettle, OnlyGuild = oldSettle, oldGuild })
	m := &uninstallManager{guilds: []manager.GuildRef{{ID: 7001, Name: "A"}}}
	b := &Bot{siteMgr: m}
	b.leaveOtherGuilds(context.Background())
	if len(m.done) != 0 || !guildAllowed(7001) {
		t.Fatalf("ONLY_GUILD unset: uninstalled %v", m.done)
	}
}

// The panel is kept only on the served server.
func TestPanelOnlyOnTheServedServer(t *testing.T) {
	k, fr, _ := newTestKeeper(t, "")
	k.allow = func(g snowflake.ID) bool { return g != panelGuild }
	k.Ensure(context.Background(), panelGuild)
	if len(fr.created) != 0 {
		t.Fatalf("panel created on a server Voice Relay no longer serves")
	}
}
