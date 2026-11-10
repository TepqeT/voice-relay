package manager

import (
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/config"
	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/store"
)

func feedbackService(t *testing.T) *Service {
	t.Helper()
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.CacheGuild{Guild: discord.Guild{ID: 10}})
	caches.AddRole(discord.Role{ID: 900, GuildID: 10, Name: "Speaker"})
	caches.AddMember(discord.Member{GuildID: 10, User: discord.User{ID: 5}, RoleIDs: []snowflake.ID{900}}) // speaker
	caches.AddMember(discord.Member{GuildID: 10, User: discord.User{ID: 6}})                               // no role
	st := store.NewInMemoryStore()
	pool := mapPool{ids: []snowflake.ID{21, 22}, clients: map[snowflake.ID]*bot.Client{}}
	m := NewService(st, pool, &bot.Client{Caches: caches}, 1, config.TestConfig{}, nil)
	m.rvr.opts.RaidLeadRole = "Speaker"
	st.BindChannel(10, 21, 101)
	st.BindChannel(10, 22, 102)
	gs := guild.NewStatus(10, 1)
	gs.Session = &guild.Session{GuildID: 10}
	m.statuses[10] = gs
	m.rememberChannels(10, RaidChannels{RaidLead: 100, Squads: []snowflake.ID{101, 102}})
	return m
}

func TestListenerReturnCut(t *testing.T) {
	m := feedbackService(t)
	allowAll := func(snowflake.ID) bool { return true }
	l1, l2 := m.feedbackAllow(10, 21, allowAll), m.feedbackAllow(10, 22, allowAll)

	feeds := m.ListenerFeeds(10)
	if len(feeds) != 2 || feeds[0].ChannelID != 101 || feeds[1].ChannelID != 102 || feeds[0].Muted {
		t.Fatalf("feeds = %+v", feeds)
	}
	if !m.SetListenerMuted(10, 102, true) || l2(7) || !l1(7) {
		t.Fatal("cutting Listener 2 must stop only its capture")
	}
	if !m.ListenerFeeds(10)[1].Muted {
		t.Fatal("the cut must show in the feeds")
	}
	m.SetListenerMuted(10, 0, true) // cut all
	if l1(7) || l2(7) {
		t.Fatal("cut all")
	}
	m.SetListenerMuted(10, 0, false)
	if !l1(7) || !l2(7) {
		t.Fatal("restore all")
	}
	m.SetListenerMuted(10, 101, true)
	m.resetFeedback(10) // new session
	if !l1(7) {
		t.Fatal("every return starts on with a new session")
	}
	if m.SetListenerMuted(10, 999, true) {
		t.Fatal("a channel outside the session cannot be cut")
	}
}

func TestSpeakerChannelRelaysOnlySpeakers(t *testing.T) {
	m := feedbackService(t)
	allow := m.speakerChannelAllow(10, func(snowflake.ID) bool { return true })
	if !allow(5) || allow(6) {
		t.Fatal("only the speaker role is relayed from the speaker channel")
	}
}

func TestOnlySpeakersCutReturns(t *testing.T) {
	m := feedbackService(t)
	if !m.CanControlFeedback(10, []snowflake.ID{900}, false) || m.CanControlFeedback(10, nil, false) || !m.CanControlFeedback(10, nil, true) {
		t.Fatal("speakers and server managers only")
	}
}
