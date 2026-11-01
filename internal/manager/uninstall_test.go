package manager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/store"
)

// leaveRest records the guilds a bot leaves; fail makes every leave refused.
type leaveRest struct {
	stubRest
	left []snowflake.ID
	fail bool
}

func (r *leaveRest) LeaveGuild(guildID snowflake.ID, _ ...rest.RequestOpt) error {
	if r.fail {
		return &rest.Error{Code: 50001, Message: "Missing Access"}
	}
	r.left = append(r.left, guildID)
	return nil
}

// mapPool is a PoolService over a fixed set of speaker clients.
type mapPool struct {
	ids     []snowflake.ID
	clients map[snowflake.ID]*bot.Client
}

func (p mapPool) GetClientByID(id snowflake.ID) (*bot.Client, bool) {
	c, ok := p.clients[id]
	return c, ok
}
func (p mapPool) GetIDs() []snowflake.ID                     { return p.ids }
func (mapPool) Reconnect(context.Context, snowflake.ID) bool { return false }
func (mapPool) Shutdown(context.Context)                     {}

type uninstallFixture struct {
	m                 *Service
	owner, h1, h2, h3 *leaveRest
}

// uninstallService: owner bot 1 in guild 10 with relay bots 21 and 22; relay
// bot 23 is in the pool but not in that server.
func uninstallService(t *testing.T) uninstallFixture {
	t.Helper()
	const guildID snowflake.ID = 10
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.Guild{ID: guildID, Name: "Test"})
	caches.AddMember(discord.Member{GuildID: guildID, User: discord.User{ID: 21, Username: "Voice Relay 1", Bot: true}})
	caches.AddMember(discord.Member{GuildID: guildID, User: discord.User{ID: 22, Username: "Voice Relay 2", Bot: true}})
	f := uninstallFixture{owner: &leaveRest{}, h1: &leaveRest{}, h2: &leaveRest{}, h3: &leaveRest{}}
	pool := mapPool{ids: []snowflake.ID{21, 22, 23}, clients: map[snowflake.ID]*bot.Client{
		21: {Rest: f.h1}, 22: {Rest: f.h2}, 23: {Rest: f.h3},
	}}
	f.m = NewService(store.NewInMemoryStore(), pool, &bot.Client{Caches: caches, Rest: f.owner}, 1, config.TestConfig{}, nil)
	return f
}

func TestUninstallLeavesEveryBot(t *testing.T) {
	f := uninstallService(t)
	rep, err := f.m.Uninstall(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OwnerLeft || !reflect.DeepEqual(f.owner.left, []snowflake.ID{10}) {
		t.Errorf("owner bot must leave: rep=%+v left=%v", rep, f.owner.left)
	}
	if want := []string{"Voice Relay 1", "Voice Relay 2"}; !reflect.DeepEqual(rep.Left, want) {
		t.Errorf("relay bots left = %v, want %v", rep.Left, want)
	}
	if len(f.h3.left) != 0 {
		t.Errorf("a relay bot absent from the server must not be asked to leave it: %v", f.h3.left)
	}
	if rep.RaidStopped || rep.ChannelsDeleted || len(rep.Failed) != 0 {
		t.Errorf("nothing else to do: %+v", rep)
	}
}

func TestUninstallRelayRefusalStillRemovesOwner(t *testing.T) {
	f := uninstallService(t)
	f.h2.fail = true
	rep, err := f.m.Uninstall(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Failed, []string{"Voice Relay 2"}) || !rep.OwnerLeft {
		t.Errorf("rep = %+v, want Voice Relay 2 failed and the owner gone", rep)
	}
}

func TestUninstallOwnerRefusal(t *testing.T) {
	f := uninstallService(t)
	f.owner.fail = true
	rep, err := f.m.Uninstall(context.Background(), 10)
	var re *rest.Error
	if !errors.As(err, &re) || rep.OwnerLeft {
		t.Fatalf("err = %v, rep = %+v: the owner refusal must be reported", err, rep)
	}
}

func TestUninstallUnknownGuild(t *testing.T) {
	f := uninstallService(t)
	if _, err := f.m.Uninstall(context.Background(), 99); err == nil {
		t.Fatal("a server without the owner bot must be refused")
	}
	if len(f.owner.left)+len(f.h1.left) != 0 {
		t.Error("nothing may leave anything")
	}
}
