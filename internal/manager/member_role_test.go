package manager

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/store"
)

const (
	mrGuild  snowflake.ID = 10
	mrAccess snowflake.ID = 77
)

type memberRoleRest struct {
	stubRest
	members []discord.Member
	added   []snowflake.ID
}

func (r *memberRoleRest) GetMembers(_ snowflake.ID, limit int, after snowflake.ID, _ ...rest.RequestOpt) ([]discord.Member, error) {
	var out []discord.Member
	for _, m := range r.members {
		if m.User.ID > after && len(out) < limit {
			out = append(out, m)
		}
	}
	return out, nil
}

func (r *memberRoleRest) AddMemberRole(_, userID, roleID snowflake.ID, _ ...rest.RequestOpt) error {
	if roleID != mrAccess {
		panic("wrong role")
	}
	r.added = append(r.added, userID)
	return nil
}

func memberRoleService(r *memberRoleRest) *Service {
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.CacheGuild{Guild: discord.Guild{ID: mrGuild}})
	caches.AddRole(discord.Role{ID: mrAccess, GuildID: mrGuild, Name: "Listener Access"})
	m := NewService(store.NewInMemoryStore(), mapPool{}, &bot.Client{Caches: caches, Rest: r}, 1, config.TestConfig{}, nil)
	m.channels.opts = ChannelOptions{Enabled: true, AccessRole: "Listener Access"}
	return m
}

// Every human member without the role gets it, once per run.
func TestGrantAccessToAll(t *testing.T) {
	r := &memberRoleRest{members: []discord.Member{
		{User: discord.User{ID: 1}},
		{User: discord.User{ID: 2}, RoleIDs: []snowflake.ID{mrAccess}},
		{User: discord.User{ID: 3, Bot: true}},
		{User: discord.User{ID: 4}},
	}}
	m := memberRoleService(r)
	m.GrantAccessToAll(context.Background(), mrGuild)
	m.GrantAccessToAll(context.Background(), mrGuild)
	slices.Sort(r.added)
	if !reflect.DeepEqual(r.added, []snowflake.ID{1, 4}) {
		t.Fatalf("role given to %v, want 1 and 4 once", r.added)
	}
}

// With the access role, the home category is private to it.
func TestHomeCategoryPrivateToAccessRole(t *testing.T) {
	m := memberRoleService(&memberRoleRest{})
	var everyoneDenied, roleAllowed bool
	for _, o := range m.homeOverwrites(mrGuild) {
		if r, ok := o.(discord.RolePermissionOverwrite); ok {
			everyoneDenied = everyoneDenied || (r.RoleID == mrGuild && r.Deny.Has(discord.PermissionViewChannel))
			roleAllowed = roleAllowed || (r.RoleID == mrAccess && r.Allow.Has(discord.PermissionViewChannel))
		}
	}
	if !everyoneDenied || !roleAllowed {
		t.Fatalf("overwrites %+v: want @everyone denied and the access role allowed", m.homeOverwrites(mrGuild))
	}
}
