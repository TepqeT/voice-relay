package bot

import (
	"log/slog"
	"slices"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// ownerCacheFlags are the caches the owner bot reads. Everything else disgo
// can cache (messages, emojis, stickers, threads, …) is held per guild for the
// life of the process, so it is left off to keep idle guilds cheap.
const ownerCacheFlags = cache.FlagGuilds |
	cache.FlagChannels |
	cache.FlagRoles |
	cache.FlagMembers |
	cache.FlagVoiceStates

// ownerCacheOpts keeps a member only while they are in voice, plus every bot.
// Role checks only ever concern users in a voice channel, and a guild's full
// member list would otherwise accumulate from member-update events.
//
// The policy needs the caches it is installed in, which do not exist until the
// client is built, so the caller sets *caches right after disgo.New.
func ownerCacheOpts(caches *cache.Caches) []cache.ConfigOpt {
	return []cache.ConfigOpt{
		cache.WithCaches(ownerCacheFlags),
		cache.WithMemberCachePolicy(func(m discord.Member) bool {
			if m.User.Bot || *caches == nil {
				return true
			}
			_, inVoice := (*caches).VoiceState(m.GuildID, m.User.ID)
			return inVoice
		}),
	}
}

// ownerCacheListeners keep the member cache consistent with ownerCacheOpts.
func ownerCacheListeners() []bot.EventListener {
	return []bot.EventListener{
		bot.NewListenerFunc(func(e *events.GuildReady) {
			go cacheVoiceMembers(e.Client().Caches, e.Guild.ID, e.Client().Rest.GetMember)
		}),
		bot.NewListenerFunc(func(e *events.GuildAvailable) {
			go cacheVoiceMembers(e.Client().Caches, e.Guild.ID, e.Client().Rest.GetMember)
		}),
		bot.NewListenerFunc(func(e *events.GuildJoin) {
			go cacheVoiceMembers(e.Client().Caches, e.Guild.ID, e.Client().Rest.GetMember)
		}),
		bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
			cacheVoiceStateMember(e.Client().Caches, e.VoiceState, e.Member)
		}),
		bot.NewListenerFunc(func(e *events.GuildVoiceLeave) {
			evictVoiceLeaver(e.Client().Caches, e.VoiceState.GuildID, e.Member)
		}),
	}
}

// cacheVoiceStateMember caches the member carried by a VOICE_STATE_UPDATE.
//
// UPSTREAM(disgo v0.20.0): the member object of that event has no guild_id, so
// disgo's own AddMember stores it under guild 0 and the policy above, which
// looks the voice state up by the member's guild, rejects it. The member of
// anyone joining voice after startup was then never cached: their squad
// channel counted as empty (no audio relayed) unless a role change happened
// to refresh them. These listeners run before the routing listeners, which
// are registered later, so the router sees the member.
func cacheVoiceStateMember(caches cache.Caches, vs discord.VoiceState, m discord.Member) {
	if vs.ChannelID == nil || m.User.ID == 0 {
		return
	}
	m.GuildID = vs.GuildID
	caches.AddMember(m)
}

// cacheVoiceMembers caches the members already in voice when a guild comes
// up. disgo's GUILD_CREATE handler adds the members before the voice states,
// so the policy rejects everyone in voice on that first pass; the guild events
// no longer carry the members (disgo v0.20.0), so the missing ones are fetched
// one by one. Runs off the event goroutine: fetch is a REST call.
//
// UPSTREAM(disgo v0.20.0): members cached before voice states on GUILD_CREATE.
type memberFetcher func(guildID, userID snowflake.ID, opts ...rest.RequestOpt) (*discord.Member, error)

func cacheVoiceMembers(caches cache.Caches, guildID snowflake.ID, fetch memberFetcher) {
	for _, userID := range voiceUserIDs(caches, guildID) {
		if _, ok := caches.Member(guildID, userID); ok {
			continue
		}
		m, err := fetch(guildID, userID)
		if err != nil || m == nil {
			slog.Debug("owner cache: member in voice not fetched", slog.String("guildID", guildID.String()), slog.String("userID", userID.String()), slog.Any("err", err))
			continue
		}
		m.GuildID = guildID
		caches.AddMember(*m)
	}
}

// voiceUserIDs copies the users in voice in guildID out of the cache.
//
// Ranging over caches.VoiceStates holds the voice-state read lock for the
// whole loop. AddMember re-reads that cache through the member policy, so a
// loop that adds members while ranging takes the read lock twice; as soon as
// a voice state update waits for the write lock in between, every reader and
// the writer block for good. That writer is the gateway read loop: the main
// bot then stopped reading the gateway, missed every heartbeat ACK and
// reconnected every three minutes (v66, 2026-11-10 to 2026-11-12). Copy
// first, release, then work.
func voiceUserIDs(caches cache.Caches, guildID snowflake.ID) []snowflake.ID {
	var ids []snowflake.ID
	for _, vs := range slices.Collect(caches.VoiceStates(guildID)) {
		if vs.ChannelID != nil {
			ids = append(ids, vs.UserID)
		}
	}
	return ids
}

func evictVoiceLeaver(caches cache.Caches, guildID snowflake.ID, m discord.Member) {
	if m.User.Bot {
		return
	}
	// The event member has no guild_id (see cacheVoiceStateMember).
	if guildID == 0 {
		guildID = m.GuildID
	}
	caches.RemoveMember(guildID, m.User.ID)
}
