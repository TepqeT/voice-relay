package bot

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/manager"
)

type fakeSiteManager struct {
	inGuild bool
	members map[snowflake.ID]string
	missing discord.Permissions
}

func (f fakeSiteManager) OwnerInGuild(snowflake.ID) bool { return f.inGuild }
func (f fakeSiteManager) MemberCanManageGuild(snowflake.ID, snowflake.ID) (bool, bool) {
	return true, true
}
func (f fakeSiteManager) MemberPresent(_ snowflake.ID, id snowflake.ID) (string, bool) {
	n, ok := f.members[id]
	return n, ok
}
func (f fakeSiteManager) MissingOwnerPermissions(snowflake.ID) discord.Permissions { return f.missing }
func (f fakeSiteManager) RoleSetupStatus(snowflake.ID) []manager.RoleSetup {
	return []manager.RoleSetup{{Purpose: "raid_lead", Name: "Raid Lead", ID: 5}, {Purpose: "class", Name: "Clerc"}}
}
func (f fakeSiteManager) CreateMissingRoles(context.Context, snowflake.ID) (manager.RoleSetupReport, error) {
	return manager.RoleSetupReport{Created: []string{"Clerc"}}, nil
}

// speakerToken builds a token whose first segment encodes id, like Discord's.
func speakerToken(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id)) + ".x.y"
}

func TestWebBackendGuildStatus(t *testing.T) {
	m := fakeSiteManager{inGuild: true, members: map[snowflake.ID]string{1001: "Héraut d'Elysea 1"}, missing: discord.PermissionMoveMembers}
	b := newWebBackend(m, 42, func() string { return "" }, []string{speakerToken("1001"), speakerToken("1002")})
	st := b.GuildStatus(777)
	if !st.BotPresent || !strings.Contains(st.BotInviteURL, "guild_id=777") || !strings.Contains(st.BotInviteURL, "disable_guild_select=true") {
		t.Fatalf("invite = %q", st.BotInviteURL)
	}
	if len(st.Heralds) != 2 || st.Heralds[0].Name != "Héraut d'Elysea 1" || !st.Heralds[0].Present || st.Heralds[1].Name != "Héraut 2" || st.Heralds[1].Present {
		t.Fatalf("heralds = %+v", st.Heralds)
	}
	if !strings.Contains(st.Heralds[1].InviteURL, "client_id=1002") || !strings.Contains(st.Heralds[1].InviteURL, "guild_id=777") {
		t.Fatalf("herald invite = %q", st.Heralds[1].InviteURL)
	}
	if len(st.MissingPermissions) != 1 || st.MissingPermissions[0] != "Déplacer des membres" {
		t.Fatalf("missing = %v", st.MissingPermissions)
	}
	if len(st.Roles) != 2 || !st.Roles[0].Exists || st.Roles[1].Exists {
		t.Fatalf("roles = %+v", st.Roles)
	}
	if b.BotName() != "le bot principal" {
		t.Fatalf("fallback name = %q", b.BotName())
	}
	if rep, _ := b.SetupRoles(context.Background(), 777); len(rep.Created) != 1 {
		t.Fatalf("setup = %+v", rep)
	}
}

func TestWebBackendBotAbsent(t *testing.T) {
	b := newWebBackend(fakeSiteManager{}, 42, func() string { return "Voix" }, []string{speakerToken("1001")})
	st := b.GuildStatus(777)
	if st.BotPresent || len(st.Roles) != 0 || st.Heralds[0].Present {
		t.Fatalf("status without bot = %+v", st)
	}
	if !siteOwnerPermissions.Has(discord.PermissionManageRoles | discord.PermissionMoveMembers | discord.PermissionMuteMembers | discord.PermissionDeafenMembers) {
		t.Fatal("site invite lacks raid permissions")
	}
}
