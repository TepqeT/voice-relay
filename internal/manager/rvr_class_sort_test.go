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

	"github.com/TepqeT/voice-relay/internal/classsort"
	"github.com/TepqeT/voice-relay/internal/guild"
)

func TestGroupPlayers(t *testing.T) {
	const (
		waiting snowflake.ID = 50
		squad1  snowflake.ID = 51
		clerc   snowflake.ID = 900
		templar snowflake.ID = 901
		lead    snowflake.ID = 902
		other   snowflake.ID = 903
	)
	classes := map[snowflake.ID]string{clerc: "Clerc", templar: "Templier"}
	got := groupPlayers([]memberVoice{
		{UserID: 3, ChannelID: waiting, Roles: []snowflake.ID{clerc, other}},
		{UserID: 1, ChannelID: waiting, Roles: []snowflake.ID{clerc, templar}},
		{UserID: 2, ChannelID: waiting, Roles: []snowflake.ID{templar, lead}},
		{UserID: 4, ChannelID: squad1},
	}, classes, lead)
	want := map[snowflake.ID][]classsort.Player{
		waiting: {
			{ID: "1", Classes: []string{"Clerc", "Templier"}},
			{ID: "2", Classes: []string{"Templier"}, SquadLead: true},
			{ID: "3", Classes: []string{"Clerc"}},
		},
		squad1: {{ID: "4"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestRaidOverwritesWaitingChannel(t *testing.T) {
	const (
		guildID snowflake.ID = 100
		faction snowflake.ID = 200
		caller  snowflake.ID = 300
		squad   snowflake.ID = 400
		ownerID snowflake.ID = 1
	)
	ow := raidOverwrites(guildID, faction, caller, squad, []snowflake.ID{ownerID}, channelWaiting)
	if _, deny, _ := overwriteFor(ow, guildID); !deny.Has(discord.PermissionViewChannel | discord.PermissionConnect) {
		t.Fatalf("@everyone deny = %v", deny)
	}
	if allow, _, _ := overwriteFor(ow, faction); !allow.Has(voiceMember) {
		t.Fatalf("access role allow = %v, want the squads' access", allow)
	}
	if _, _, ok := overwriteFor(ow, squad); ok {
		t.Fatal("squad leads get no moderation in the waiting channel")
	}
	if allow, _, _ := overwriteFor(ow, ownerID); !allow.Has(discord.PermissionViewChannel | discord.PermissionConnect) {
		t.Fatalf("owner bot allow = %v", allow)
	}
}

func TestRaidChannelsAllIncludesWaiting(t *testing.T) {
	rc := RaidChannels{Category: 1, RaidLead: 2, Squads: []snowflake.ID{3, 4}, Waiting: 5}
	if got, want := rc.all(), []snowflake.ID{3, 4, 5, 2, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all() = %v, want %v (category last)", got, want)
	}
}

func TestClassSortDisabledByDefault(t *testing.T) {
	m := &Service{}
	m.ConfigureClassSort(ClassSortOptions{ClassRoles: []string{"Clerc"}})
	if m.ClassSortEnabled() {
		t.Fatal("enabled without a waiting channel")
	}
	if m.CanSortPlayers(1, nil, discord.PermissionAdministrator) {
		t.Fatal("sorting allowed while disabled")
	}
	if _, err := m.SortPlayers(context.Background(), 1); err != ErrClassSortDisabled {
		t.Fatalf("err = %v, want ErrClassSortDisabled", err)
	}
	m.OnVoiceChannelJoin(1, 2) // must not panic nor schedule anything
	if len(m.sort.timers) != 0 {
		t.Fatal("timer scheduled while disabled")
	}
	m.ConfigureClassSort(ClassSortOptions{ClassRoles: []string{"Clerc"}, Waiting: "Attente"})
	if !m.ClassSortEnabled() || !m.CanSortPlayers(1, nil, discord.PermissionManageGuild) {
		t.Fatal("managers must be able to sort once configured")
	}
}

func TestSortPlayersMovesAndSurvivesPermissionErrors(t *testing.T) {
	const (
		guildID  snowflake.ID = 10
		waiting  snowflake.ID = 50
		squadA   snowflake.ID = 51
		squadB   snowflake.ID = 52
		clercID  snowflake.ID = 900
		leadRole snowflake.ID = 901
	)
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddRole(discord.Role{ID: clercID, GuildID: guildID, Name: "Clerc"})
	caches.AddRole(discord.Role{ID: leadRole, GuildID: guildID, Name: "Chef d'escouade"})
	join := func(user snowflake.ID, ch snowflake.ID, roles ...snowflake.ID) {
		caches.AddMember(discord.Member{GuildID: guildID, User: discord.User{ID: user}, RoleIDs: roles})
		c := ch
		caches.AddVoiceState(discord.VoiceState{GuildID: guildID, UserID: user, ChannelID: &c})
	}
	join(1, waiting, clercID)
	join(2, waiting, clercID)
	join(3, waiting)                    // no class
	join(4, waiting, clercID, leadRole) // squad lead
	caches.AddMember(discord.Member{GuildID: guildID, User: discord.User{ID: 99, Bot: true}})
	bw := waiting
	caches.AddVoiceState(discord.VoiceState{GuildID: guildID, UserID: 99, ChannelID: &bw})

	m := &Service{
		statuses:    map[snowflake.ID]*guild.Status{guildID: {GuildID: guildID, Session: &guild.Session{GuildID: guildID}}},
		ownerClient: &bot.Client{Caches: caches},
	}
	m.channels.opts.Enabled = true
	m.channels.byGuild = map[snowflake.ID]RaidChannels{guildID: {Squads: []snowflake.ID{squadA, squadB}, Waiting: waiting, RaidLead: 7}}
	m.rvr.opts.SquadLeadRole = "Chef d'escouade"
	var moved []snowflake.ID
	m.sort.move = func(_ context.Context, _, userID, channelID snowflake.ID) error {
		if userID == 2 {
			return &rest.Error{Code: 50013, Message: "Missing Permissions"}
		}
		moved = append(moved, userID, channelID)
		return nil
	}
	m.ConfigureClassSort(ClassSortOptions{ClassRoles: []string{"clerc"}, Waiting: "Attente"})

	rep, err := m.SortPlayers(context.Background(), guildID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moved != 1 || rep.Failed != 1 || !rep.PermissionDenied || rep.Squads != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if !reflect.DeepEqual(moved, []snowflake.ID{1, squadA}) {
		t.Fatalf("moved = %v", moved)
	}
	want := []SortWaiting{{UserID: 3, Reason: classsort.ReasonNoClass}, {UserID: 4, Reason: classsort.ReasonSquadLead}}
	if !reflect.DeepEqual(rep.Waiting, want) {
		t.Fatalf("waiting = %+v, want %+v", rep.Waiting, want)
	}
}
