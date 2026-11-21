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

// The project is stopped: the tool leaves every server it is in.
func TestUninstallEverywhere(t *testing.T) {
	old := guildsSettle
	guildsSettle = time.Millisecond
	t.Cleanup(func() { guildsSettle = old })
	m := &uninstallManager{guilds: []manager.GuildRef{{ID: 7001, Name: "A"}, {ID: 7002, Name: "B"}}}
	b := &Bot{siteMgr: m}
	b.uninstallEverywhere(context.Background())
	if len(m.done) != 2 {
		t.Fatalf("uninstalled %v, want both servers", m.done)
	}
	if !hasKind(journal.ForGuild(7001, 5), "shutdown_uninstalled") {
		t.Error("uninstall not journaled")
	}
	if !ProjectStopped {
		t.Error("the project is stopped")
	}
}
