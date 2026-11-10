package manager

import (
	"context"
	"reflect"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// roleRest records role creations; "Refusé" fails with 50013.
type roleRest struct {
	stubRest
	created  []string
	next     snowflake.ID
	reloaded int
}

func (r *roleRest) CreateRole(guildID snowflake.ID, c discord.RoleCreate, _ ...rest.RequestOpt) (*discord.Role, error) {
	if c.Name == "Refusé" {
		return nil, &rest.Error{Code: 50013, Message: "Missing Permissions"}
	}
	r.next++
	r.created = append(r.created, c.Name)
	return &discord.Role{ID: 5000 + r.next, GuildID: guildID, Name: c.Name, Position: 1}, nil
}

// GetRoles answers the reload after a creation: the created roles end up
// right above @everyone, below the bot's role.
func (r *roleRest) GetRoles(guildID snowflake.ID, _ ...rest.RequestOpt) ([]discord.Role, error) {
	r.reloaded++
	return nil, nil
}

func setupService(t *testing.T) (*Service, *roleRest) {
	t.Helper()
	const guildID snowflake.ID = 10
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.CacheGuild{Guild: discord.Guild{ID: guildID, Name: "Test"}})
	caches.AddRole(discord.Role{ID: 900, GuildID: guildID, Name: "Raid Lead", Position: 2})
	caches.AddRole(discord.Role{ID: 901, GuildID: guildID, Name: "Admin", Position: 9})
	caches.AddRole(discord.Role{ID: 902, GuildID: guildID, Name: "Bot", Position: 5})
	rr := &roleRest{}
	m := &Service{ownerClient: &bot.Client{Caches: caches, Rest: rr}, ownerBotID: 1}
	caches.AddMember(discord.Member{GuildID: guildID, User: discord.User{ID: 1, Bot: true}, RoleIDs: []snowflake.ID{902}})
	caches.SetSelfUser(discord.OAuth2User{User: discord.User{ID: 1, Bot: true}})
	m.rvr.opts.RaidLeadRole = "raid lead"
	m.rvr.opts.SquadLeadRole = "Squad Lead"
	m.channels.opts.AccessRole = "Admin"
	m.channels.opts.BoardRole = "123456789012345678"
	m.sort.opts.ClassRoles = []string{"Healer", "Refusé", "admin"} // admin again: listed once
	return m, rr
}

func TestRoleSetupStatus(t *testing.T) {
	m, _ := setupService(t)
	got := m.RoleSetupStatus(10)
	if len(got) != 6 {
		t.Fatalf("roles = %+v, want 6 (access deduplicated)", got)
	}
	byName := map[string]RoleSetup{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if r := byName["raid lead"]; r.ID != 900 || r.AboveBot {
		t.Errorf("raid lead = %+v", r)
	}
	if r := byName["Admin"]; r.ID != 901 || !r.AboveBot {
		t.Errorf("Admin must be flagged above the bot: %+v", r)
	}
	if r := byName["123456789012345678"]; r.ID != 0 || !r.ByID {
		t.Errorf("unknown ID = %+v", r)
	}
}

// A role created next to the bot's role shares its position: Discord ranks the
// older role (the bot's, lower ID) higher, so the new role is manageable.
func TestRoleSetupStatusEqualPosition(t *testing.T) {
	m, _ := setupService(t)
	caches := m.ownerClient.Caches
	caches.AddRole(discord.Role{ID: 950, GuildID: 10, Name: "Squad Lead", Position: 5}) // newer than Bot (902)
	caches.AddRole(discord.Role{ID: 800, GuildID: 10, Name: "Healer", Position: 5})     // older than Bot
	byName := map[string]RoleSetup{}
	for _, r := range m.RoleSetupStatus(10) {
		byName[r.Name] = r
	}
	if r := byName["Squad Lead"]; r.ID != 950 || r.AboveBot {
		t.Errorf("newer role at the bot's position must stay below it: %+v", r)
	}
	if r := byName["Healer"]; r.ID != 800 || !r.AboveBot {
		t.Errorf("older role at the bot's position ranks above it: %+v", r)
	}
}

func TestRoleAbove(t *testing.T) {
	for _, c := range []struct {
		a, b discord.Role
		want bool
	}{
		{discord.Role{ID: 2, Position: 3}, discord.Role{ID: 1, Position: 2}, true},
		{discord.Role{ID: 1, Position: 2}, discord.Role{ID: 2, Position: 3}, false},
		{discord.Role{ID: 1, Position: 1}, discord.Role{ID: 2, Position: 1}, true},
		{discord.Role{ID: 2, Position: 1}, discord.Role{ID: 1, Position: 1}, false},
	} {
		if got := roleAbove(c.a, c.b); got != c.want {
			t.Errorf("roleAbove(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCreateMissingRoles(t *testing.T) {
	m, rr := setupService(t)
	rep, err := m.CreateMissingRoles(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Squad Lead", "Healer"}; !reflect.DeepEqual(rr.created, want) || !reflect.DeepEqual(rep.Created, want) {
		t.Fatalf("created %v / %v, want %v", rr.created, rep.Created, want)
	}
	if want := []string{"123456789012345678", "Refusé"}; !reflect.DeepEqual(rep.Failed, want) || !rep.PermissionDenied {
		t.Fatalf("failed = %v, denied = %v", rep.Failed, rep.PermissionDenied)
	}
	if rr.reloaded != 1 {
		t.Fatalf("roles reloaded %d times after creation, want 1", rr.reloaded)
	}
	// Second run: what was created is now found, nothing is created twice.
	rr.created = nil
	rep, _ = m.CreateMissingRoles(context.Background(), 10)
	if len(rr.created) != 0 || len(rep.Created) != 0 {
		t.Fatalf("second run created %v", rr.created)
	}
	if _, err := m.CreateMissingRoles(context.Background(), 99); err == nil {
		t.Fatal("a guild without the bot must be refused")
	}
}
