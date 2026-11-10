package manager

import (
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestPanelStatusIdleAndCounts(t *testing.T) {
	const guildID, ch snowflake.ID = 10, 70
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.CacheGuild{Guild: discord.Guild{ID: guildID}})
	caches.AddRole(discord.Role{ID: 900, GuildID: guildID, Name: "Raid Access"})
	caches.AddRole(discord.Role{ID: 901, GuildID: guildID, Name: "Raid Board"})
	in := ch
	for _, u := range []struct {
		id  snowflake.ID
		bot bool
	}{{21, false}, {22, false}, {23, true}} {
		caches.AddMember(discord.Member{GuildID: guildID, User: discord.User{ID: u.id, Bot: u.bot}})
		caches.AddVoiceState(discord.VoiceState{GuildID: guildID, UserID: u.id, ChannelID: &in})
	}
	m := &Service{ownerClient: &bot.Client{Caches: caches}}

	if st := m.PanelStatus(guildID); st.Running || len(st.Squads) != 0 {
		t.Fatalf("no raid: %+v", st)
	}
	if n := m.humansIn(guildID, ch); n != 2 {
		t.Fatalf("humans = %d, want 2 (bot excluded)", n)
	}

	if m.AccessRoleID(guildID) != 0 || m.BoardRoleID(guildID) != 0 {
		t.Fatal("access off: no role")
	}
	m.channels.opts.Enabled = true
	m.channels.opts.AccessRole, m.channels.opts.BoardRole = "raid access", "Raid Board"
	if m.AccessRoleID(guildID) != 900 || m.BoardRoleID(guildID) != 901 {
		t.Fatalf("roles = %d %d", m.AccessRoleID(guildID), m.BoardRoleID(guildID))
	}
}
