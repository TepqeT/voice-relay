package bot

import (
	"errors"
	"testing"

	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

const (
	cacheTestGuild   snowflake.ID = 100
	cacheTestChannel snowflake.ID = 200
)

func newOwnerCaches() cache.Caches {
	var caches cache.Caches
	caches = cache.New(ownerCacheOpts(&caches)...)
	return caches
}

func testMember(id snowflake.ID, isBot bool) discord.Member {
	return discord.Member{
		GuildID: cacheTestGuild,
		User:    discord.User{ID: id, Bot: isBot},
		RoleIDs: []snowflake.ID{1},
	}
}

func joinVoice(caches cache.Caches, userID snowflake.ID) {
	ch := cacheTestChannel
	caches.AddVoiceState(discord.VoiceState{GuildID: cacheTestGuild, UserID: userID, ChannelID: &ch})
}

func isCached(caches cache.Caches, userID snowflake.ID) bool {
	_, ok := caches.Member(cacheTestGuild, userID)
	return ok
}

func TestOwnerCacheKeepsOnlyVoiceMembersAndBots(t *testing.T) {
	caches := newOwnerCaches()

	caches.AddMember(testMember(1, false))
	if isCached(caches, 1) {
		t.Error("member outside voice was cached")
	}

	caches.AddMember(testMember(2, true))
	if !isCached(caches, 2) {
		t.Error("bot outside voice was not cached")
	}

	joinVoice(caches, 3)
	caches.AddMember(testMember(3, false))
	if !isCached(caches, 3) {
		t.Error("member in voice was not cached")
	}
}

// disgo's GUILD_CREATE handler adds members before voice states.
func TestCacheVoiceMembersRestoresMembersAlreadyInVoiceAtGuildCreate(t *testing.T) {
	caches := newOwnerCaches()
	inVoice, idle := testMember(1, false), testMember(2, false)

	caches.AddMember(inVoice)
	caches.AddMember(idle)
	joinVoice(caches, inVoice.User.ID)

	fetched := 0
	cacheVoiceMembers(caches, cacheTestGuild, func(guildID, userID snowflake.ID, _ ...rest.RequestOpt) (*discord.Member, error) {
		fetched++
		for _, m := range []discord.Member{inVoice, idle} {
			if m.User.ID == userID && guildID == cacheTestGuild {
				m.GuildID = 0 // the REST member carries no guild_id in its body
				return &m, nil
			}
		}
		return nil, errors.New("unknown member")
	})

	if !isCached(caches, inVoice.User.ID) {
		t.Error("member in voice at GUILD_CREATE is missing from the cache")
	}
	if isCached(caches, idle.User.ID) {
		t.Error("member outside voice at GUILD_CREATE was cached")
	}
	if fetched != 1 {
		t.Errorf("fetched %d members, want 1 (only the one in voice)", fetched)
	}
	// Already cached: nothing fetched again.
	cacheVoiceMembers(caches, cacheTestGuild, func(snowflake.ID, snowflake.ID, ...rest.RequestOpt) (*discord.Member, error) {
		t.Error("member fetched although cached")
		return nil, nil
	})
}

func TestEvictVoiceLeaverDropsHumansButKeepsBots(t *testing.T) {
	caches := newOwnerCaches()
	human, bot := testMember(1, false), testMember(2, true)
	for _, m := range []discord.Member{human, bot} {
		joinVoice(caches, m.User.ID)
		caches.AddMember(m)
	}

	for _, m := range []discord.Member{human, bot} {
		caches.RemoveVoiceState(cacheTestGuild, m.User.ID)
		evictVoiceLeaver(caches, m.GuildID, m)
	}

	if isCached(caches, human.User.ID) {
		t.Error("member stayed cached after leaving voice")
	}
	if !isCached(caches, bot.User.ID) {
		t.Error("bot was evicted after leaving voice")
	}
}

func TestOwnerCacheStoresNoMessages(t *testing.T) {
	caches := newOwnerCaches()

	caches.AddMessage(discord.Message{ID: 1, ChannelID: cacheTestChannel})

	if _, ok := caches.Message(cacheTestChannel, 1); ok {
		t.Error("owner cache stored a message")
	}
}

// A member joining voice after startup arrives without guild_id in the
// VOICE_STATE_UPDATE payload; it must still be cached so the squad channel
// counts as having listeners.
func TestCacheVoiceStateMemberWithoutGuildID(t *testing.T) {
	caches := newOwnerCaches()
	ch := cacheTestChannel
	vs := discord.VoiceState{GuildID: cacheTestGuild, UserID: 7, ChannelID: &ch}
	m := discord.Member{User: discord.User{ID: 7}} // no GuildID, like the gateway payload

	// What disgo itself does: voice state first, then the member as received.
	caches.AddVoiceState(vs)
	caches.AddMember(m)
	if isCached(caches, 7) {
		t.Skip("disgo now fills the member guild; the workaround is no longer needed")
	}

	cacheVoiceStateMember(caches, vs, m)
	if !isCached(caches, 7) {
		t.Fatal("member who joined voice is missing from the cache")
	}

	caches.RemoveVoiceState(cacheTestGuild, 7)
	evictVoiceLeaver(caches, cacheTestGuild, m)
	if isCached(caches, 7) {
		t.Fatal("member who left voice is still cached")
	}
}

func TestCacheVoiceStateMemberIgnoresLeave(t *testing.T) {
	caches := newOwnerCaches()
	cacheVoiceStateMember(caches, discord.VoiceState{GuildID: cacheTestGuild, UserID: 8}, discord.Member{User: discord.User{ID: 8}})
	if isCached(caches, 8) {
		t.Fatal("a leave event must not cache the member")
	}
}
