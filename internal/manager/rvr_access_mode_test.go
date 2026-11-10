package manager

import (
	"context"
	"path/filepath"
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

// overwriteRest records overwrite changes per channel.
type overwriteRest struct {
	stubRest
	set     map[[2]snowflake.ID]discord.RolePermissionOverwriteUpdate
	removed [][2]snowflake.ID
	kicks   int
}

func (r *overwriteRest) UpdatePermissionOverwrite(ch, id snowflake.ID, u discord.PermissionOverwriteUpdate, _ ...rest.RequestOpt) error {
	r.set[[2]snowflake.ID{ch, id}] = u.(discord.RolePermissionOverwriteUpdate)
	return nil
}
func (r *overwriteRest) Do(_ *rest.CompiledEndpoint, _, _ any, _ ...rest.RequestOpt) error {
	r.kicks++
	return nil
}
func (r *overwriteRest) DeletePermissionOverwrite(ch, id snowflake.ID, _ ...rest.RequestOpt) error {
	r.removed = append(r.removed, [2]snowflake.ID{ch, id})
	return nil
}

const (
	amGuild     snowflake.ID = 10
	amRaidLead  snowflake.ID = 900
	amSquadLead snowflake.ID = 901
	amAccess    snowflake.ID = 902
)

func accessModeService(t *testing.T) (*Service, *overwriteRest) {
	t.Helper()
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.CacheGuild{Guild: discord.Guild{ID: amGuild}})
	caches.AddRole(discord.Role{ID: amRaidLead, GuildID: amGuild, Name: "Raid Lead"})
	caches.AddRole(discord.Role{ID: amSquadLead, GuildID: amGuild, Name: "Squad Lead"})
	caches.AddRole(discord.Role{ID: amAccess, GuildID: amGuild, Name: "Raid Access"})
	rr := &overwriteRest{set: map[[2]snowflake.ID]discord.RolePermissionOverwriteUpdate{}}
	m := NewService(store.NewInMemoryStore(), emptyPool{}, &bot.Client{Caches: caches, Rest: rr}, 1, config.TestConfig{}, nil)
	m.rvr.opts.RaidLeadRole, m.rvr.opts.SquadLeadRole = "Raid Lead", "Squad Lead"
	m.channels.opts.Enabled = true
	m.channels.opts.AccessRole = "Raid Access"
	m.channels.opts.StateFile = filepath.Join(t.TempDir(), "rvr-channels.json")
	return m, rr
}

func TestLeadersApproveAccess(t *testing.T) {
	m, _ := accessModeService(t)
	if !m.AccessEnabled() {
		t.Fatal("an access role alone must enable access management (board optional)")
	}
	if got := m.ApproverRoleIDs(amGuild); !slices.Equal(got, []snowflake.ID{amRaidLead, amSquadLead}) {
		t.Fatalf("approvers = %v, want raid lead and squad lead", got)
	}
	for _, roles := range [][]snowflake.ID{{amRaidLead}, {amSquadLead}} {
		if !m.CanManageAccess(amGuild, roles, 0) {
			t.Errorf("roles %v must approve", roles)
		}
	}
	if m.CanManageAccess(amGuild, []snowflake.ID{amAccess}, 0) {
		t.Error("a plain player must not approve")
	}
}

// With an access role, the channels stay private to it whatever was saved:
// every member holds the role, and Discord always shows private channels
// to those who can see them, onboarding or not.
func TestAccessAlwaysRestrictedWithAccessRole(t *testing.T) {
	m, _ := accessModeService(t)
	if m.AccessOpen(amGuild) || m.viewRoleName(amGuild) != "Raid Access" {
		t.Fatal("restricted by default")
	}
	if _, err := m.SetAccessOpen(context.Background(), amGuild, true); err != nil {
		t.Fatal(err)
	}
	if m.AccessOpen(amGuild) || m.viewRoleName(amGuild) != "Raid Access" {
		t.Fatal("an old open choice must not make the channels public")
	}
}

func TestRestrictDisconnectsMembersWithoutAccess(t *testing.T) {
	m, rr := accessModeService(t)
	ctx := context.Background()
	m.rememberChannels(amGuild, RaidChannels{Category: 50, RaidLead: 51, Squads: []snowflake.ID{52}, SharedCategory: true})
	if _, err := m.SetAccessOpen(ctx, amGuild, true); err != nil {
		t.Fatal(err)
	}
	caches := m.ownerClient.Caches
	squad := snowflake.ID(52)
	elsewhere := snowflake.ID(99)
	for _, u := range []struct {
		id    snowflake.ID
		roles []snowflake.ID
		bot   bool
		ch    *snowflake.ID
	}{
		{21, nil, false, &squad},                         // no access: moved out
		{22, []snowflake.ID{amAccess}, false, &squad},    // has access: stays
		{23, []snowflake.ID{amSquadLead}, false, &squad}, // leader: stays
		{24, nil, true, &squad},                          // bot: stays
		{25, nil, false, &elsewhere},                     // not in the raid
	} {
		caches.AddMember(discord.Member{GuildID: amGuild, User: discord.User{ID: u.id, Bot: u.bot}, RoleIDs: u.roles})
		caches.AddVoiceState(discord.VoiceState{GuildID: amGuild, UserID: u.id, ChannelID: u.ch})
	}
	n, err := m.SetAccessOpen(ctx, amGuild, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || rr.kicks != 1 {
		t.Fatalf("disconnected %d (%d calls), want only the member without access", n, rr.kicks)
	}
	// The home category is never closed: it holds the panel channel.
	if _, ok := rr.set[[2]snowflake.ID{50, amGuild}]; ok {
		t.Error("home category overwrite changed")
	}
}
