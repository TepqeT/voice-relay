package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/store"
)

type homeRest struct {
	stubRest
	t       *testing.T
	next    snowflake.ID
	created []string
	deleted []snowflake.ID
	reperm  []snowflake.ID // categories whose overwrites were replaced
}

func (r *homeRest) UpdateChannel(id snowflake.ID, _ discord.ChannelUpdate, _ ...rest.RequestOpt) (discord.Channel, error) {
	r.reperm = append(r.reperm, id)
	return nil, nil
}

func categoryChannel(t *testing.T, id, guildID snowflake.ID, name string) discord.GuildCategoryChannel {
	t.Helper()
	var ch discord.GuildCategoryChannel
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":"%d","guild_id":"%d","name":%q,"type":4}`, id, guildID, name)), &ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

func (r *homeRest) CreateGuildChannel(guildID snowflake.ID, c discord.GuildChannelCreate, _ ...rest.RequestOpt) (discord.GuildChannel, error) {
	cc := c.(discord.GuildCategoryChannelCreate)
	r.created = append(r.created, cc.Name)
	r.next++
	return categoryChannel(r.t, 800+r.next, guildID, cc.Name), nil
}
func (r *homeRest) DeleteChannel(id snowflake.ID, _ ...rest.RequestOpt) error {
	r.deleted = append(r.deleted, id)
	return nil
}

func homeService(t *testing.T, dir string) (*Service, *homeRest, cache.Caches) {
	t.Helper()
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.Guild{ID: 10})
	hr := &homeRest{t: t}
	m := NewService(store.NewInMemoryStore(), emptyPool{}, &bot.Client{Caches: caches, Rest: hr}, 1, config.TestConfig{}, nil)
	m.channels.opts = ChannelOptions{Enabled: true, HomeCategory: true, Category: "RvR", StateFile: filepath.Join(dir, "rvr-channels.json")}
	return m, hr, caches
}

func TestHomeCategoryCreatedOnceAndRemovedOnUninstall(t *testing.T) {
	dir := t.TempDir()
	m, hr, _ := homeService(t, dir)
	ctx := context.Background()
	id, err := m.EnsureHomeCategory(ctx, 10)
	if err != nil || len(hr.created) != 1 || hr.created[0] != "RvR" {
		t.Fatalf("id %d err %v created %v", id, err, hr.created)
	}
	if again, _ := m.EnsureHomeCategory(ctx, 10); again != id || len(hr.created) != 1 {
		t.Fatal("the home category must be created once")
	}
	// Restart: remembered.
	m2, hr2, caches2 := homeService(t, dir)
	caches2.AddChannel(categoryChannel(t, id, 10, "RvR"))
	if got, _ := m2.EnsureHomeCategory(ctx, 10); got != id || len(hr2.created) != 0 {
		t.Fatalf("after restart got %d, created %v", got, hr2.created)
	}
	m2.EnsureHomeCategory(ctx, 10)
	if len(hr2.reperm) != 1 || hr2.reperm[0] != id {
		t.Fatalf("overwrites refreshed on %v, want once on %d", hr2.reperm, id)
	}
	// A raid's channels never include the shared category.
	rc := RaidChannels{Category: id, RaidLead: 51, Squads: []snowflake.ID{52}, SharedCategory: true}
	for _, c := range rc.all() {
		if c == id {
			t.Fatal("the home category must not be deleted with the raid")
		}
	}
	m2.RemoveHomeCategory(ctx, 10)
	if len(hr2.deleted) != 1 || hr2.deleted[0] != id {
		t.Fatalf("uninstall deleted %v", hr2.deleted)
	}
}

func TestHomeCategoryReusesExistingOne(t *testing.T) {
	m, hr, caches := homeService(t, t.TempDir())
	caches.AddChannel(categoryChannel(t, 555, 10, "rvr"))
	if id, _ := m.EnsureHomeCategory(context.Background(), 10); id != 555 || len(hr.created) != 0 {
		t.Fatalf("id %d created %v: the server's category must be reused", id, hr.created)
	}
	m.RemoveHomeCategory(context.Background(), 10)
	if len(hr.deleted) != 0 {
		t.Fatal("a category the server made must never be deleted")
	}
}
