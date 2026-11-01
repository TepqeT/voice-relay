package manager

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/guild"
)

func TestPickSquadSpeakersNaturalOrder(t *testing.T) {
	in := []guild.Speaker{
		{ID: 10, Username: "Asmos-Speaker-10", Enabled: true},
		{ID: 2, Username: "Asmos-Speaker-2", Enabled: true},
		{ID: 3, Username: "Asmos-Speaker-3", Enabled: false},
		{ID: 1, Username: "Asmos-Speaker-1", Enabled: true},
	}
	got := pickSquadSpeakers(in, 10)
	want := []snowflake.ID{1, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("got %d speakers, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d = %s, want %s", i, got[i].Username, id)
		}
	}
	if n := len(pickSquadSpeakers(in, 2)); n != 2 {
		t.Fatalf("capped to %d, want 2", n)
	}
	if in[0].ID != 10 {
		t.Fatal("input slice must not be reordered")
	}
}

func overwriteFor(ow []discord.PermissionOverwrite, id snowflake.ID) (allow, deny discord.Permissions, ok bool) {
	for _, o := range ow {
		switch v := o.(type) {
		case discord.RolePermissionOverwrite:
			if v.RoleID == id {
				return v.Allow, v.Deny, true
			}
		case discord.MemberPermissionOverwrite:
			if v.UserID == id {
				return v.Allow, v.Deny, true
			}
		}
	}
	return 0, 0, false
}

func TestRaidOverwrites(t *testing.T) {
	const (
		guildID snowflake.ID = 100
		faction snowflake.ID = 200
		caller  snowflake.ID = 300
		squad   snowflake.ID = 400
		ownerID snowflake.ID = 1
		botID   snowflake.ID = 2
	)
	bots := []snowflake.ID{ownerID, botID}

	// Squad channel: faction joins, everyone else sees nothing, squad leads
	// get priority speaker, the caller role gets nothing extra.
	ow := raidOverwrites(guildID, faction, caller, squad, bots, channelSquad)
	if _, deny, _ := overwriteFor(ow, guildID); !deny.Has(discord.PermissionViewChannel | discord.PermissionConnect) {
		t.Fatalf("@everyone deny = %v", deny)
	}
	if allow, _, _ := overwriteFor(ow, faction); !allow.Has(voiceMember) {
		t.Fatalf("faction allow = %v", allow)
	}
	if allow, _, _ := overwriteFor(ow, squad); allow != squadLeadAllow || !allow.Has(discord.PermissionPrioritySpeaker|discord.PermissionMuteMembers|discord.PermissionMoveMembers) || allow.Has(discord.PermissionDeafenMembers) {
		t.Fatalf("squad lead allow = %v", allow)
	}
	if _, _, ok := overwriteFor(ow, caller); ok {
		t.Fatal("caller role must not be set on squad channels")
	}
	if allow, _, _ := overwriteFor(ow, botID); !allow.Has(voiceMember | discord.PermissionPrioritySpeaker) {
		t.Fatalf("bot allow = %v", allow)
	}

	// Raid lead channel: the faction only sees it, the caller role joins it
	// without View, so a role shared by both factions reveals nothing.
	ow = raidOverwrites(guildID, faction, caller, squad, bots, channelRaidLead)
	if allow, _, _ := overwriteFor(ow, faction); allow != discord.PermissionViewChannel {
		t.Fatalf("faction allow on raid lead = %v", allow)
	}
	allow, _, _ := overwriteFor(ow, caller)
	if !allow.Has(discord.PermissionConnect|discord.PermissionSpeak) || allow.Has(discord.PermissionViewChannel) {
		t.Fatalf("caller allow on raid lead = %v", allow)
	}
	if _, _, ok := overwriteFor(ow, squad); ok {
		t.Fatal("squad role must not be set on the raid lead channel")
	}

	// No faction role: visible to everyone, only the raid lead channel
	// restricts Connect.
	ow = raidOverwrites(guildID, 0, caller, 0, bots, channelSquad)
	if _, _, ok := overwriteFor(ow, guildID); ok {
		t.Fatal("no faction role: squads must stay open")
	}
	ow = raidOverwrites(guildID, 0, caller, 0, bots, channelRaidLead)
	if _, deny, _ := overwriteFor(ow, guildID); deny != discord.PermissionConnect {
		t.Fatalf("no faction role: raid lead @everyone deny = %v", deny)
	}
}

func TestChannelStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "rvr-channels.json")
	in := map[snowflake.ID]RaidChannels{
		42: {Category: 1, RaidLead: 2, Squads: []snowflake.ID{3, 4}, CallerRole: 7},
	}
	if err := writeChannelState(path, in); err != nil {
		t.Fatal(err)
	}
	out := loadChannelState(path)
	rc, ok := out[42]
	if !ok || rc.Category != 1 || rc.RaidLead != 2 || len(rc.Squads) != 2 || rc.Squads[1] != 4 || rc.CallerRole != 7 {
		t.Fatalf("round trip = %+v", out)
	}
	if got := rc.all(); len(got) != 4 || got[len(got)-1] != 1 {
		t.Fatalf("all() = %v, category must be last", got)
	}
	if len(loadChannelState(filepath.Join(t.TempDir(), "missing.json"))) != 0 {
		t.Fatal("missing state must load empty")
	}
}

func TestMaskOverwrites(t *testing.T) {
	ow := raidOverwrites(100, 200, 300, 400, []snowflake.ID{1}, channelSquad)
	grant := discord.PermissionViewChannel | discord.PermissionConnect | discord.PermissionSpeak | discord.PermissionUseVAD
	got := maskOverwrites(ow, grant)
	if _, _, ok := overwriteFor(got, 400); ok {
		t.Fatal("squad lead overwrite only held permissions the bot cannot grant: it must be dropped")
	}
	allow, _, _ := overwriteFor(got, 1)
	if allow.Has(discord.PermissionPrioritySpeaker) || !allow.Has(discord.PermissionConnect) {
		t.Fatalf("bot allow = %v", allow)
	}
	if _, deny, _ := overwriteFor(got, 100); !deny.Has(discord.PermissionViewChannel) {
		t.Fatalf("@everyone deny lost: %v", deny)
	}
}

func TestOrderCategories(t *testing.T) {
	mk := func(id snowflake.ID, name string, pos int) discord.GuildChannel {
		var c discord.GuildCategoryChannel
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":"%d","type":4,"name":%q,"position":%d}`, id, name, pos)), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	all := []discord.GuildChannel{
		mk(1, "Aion 2 Global", 0),
		mk(2, "Aion 2 Communauté Fr", 1),
		mk(3, "Aion 2 faction Asmodienne", 2),
		mk(4, "Aion 2 faction Elyséenne", 3),
		mk(9, "RvR Asmodiens", 3), // just created, same position as the next one
	}
	if got := findCategory(all, "aion 2 faction asmodienne"); got != 3 {
		t.Fatalf("findCategory exact = %d", got)
	}
	if got := findCategory(all, "Elyséenne"); got != 4 {
		t.Fatalf("findCategory contains = %d", got)
	}
	got := orderCategories(all, 3, 9)
	want := []snowflake.ID{1, 2, 3, 9, 4}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestRaidOverwritesCategoryLetsBotsGrantChildPermissions(t *testing.T) {
	ow := raidOverwrites(100, 200, 300, 400, []snowflake.ID{1}, channelCategory)
	allow, _, _ := overwriteFor(ow, 1)
	if !allow.Has(voiceMember | discord.PermissionPrioritySpeaker) {
		t.Fatalf("bot allow on category = %v: child channels would be refused", allow)
	}
	if _, deny, _ := overwriteFor(ow, 100); deny != discord.PermissionViewChannel {
		t.Fatalf("@everyone deny on category = %v, want View only", deny)
	}
}

func TestBoardModeratesRaidVoiceChannels(t *testing.T) {
	for _, kind := range []channelKind{channelSquad, channelRaidLead} {
		allow, _, ok := overwriteFor(boardOverwrites(500, kind), 500)
		if !ok || allow != boardModeration || allow.Has(discord.PermissionViewChannel) || allow.Has(discord.PermissionDeafenMembers) {
			t.Fatalf("kind %v: board allow = %v (ok %v)", kind, allow, ok)
		}
	}
	if ow := boardOverwrites(500, channelCategory); len(ow) != 0 {
		t.Fatalf("category must not carry the board overwrite: %v", ow)
	}
	if ow := boardOverwrites(0, channelSquad); len(ow) != 0 {
		t.Fatalf("no board role: %v", ow)
	}
}
