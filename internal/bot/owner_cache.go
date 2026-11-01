package bot

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
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
		bot.NewListenerFunc(func(e *events.GuildReady) { cacheVoiceMembers(e.Client().Caches, e.Guild) }),
		bot.NewListenerFunc(func(e *events.GuildAvailable) { cacheVoiceMembers(e.Client().Caches, e.Guild) }),
		bot.NewListenerFunc(func(e *events.GuildJoin) { cacheVoiceMembers(e.Client().Caches, e.Guild) }),
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
// UPSTREAM(disgo v0.19.3): the member object of that event has no guild_id, so
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

// cacheVoiceMembers re-adds the members of a GUILD_CREATE. disgo caches them
// before the guild's voice states, so the policy rejects everyone in voice on
// that first pass.
func cacheVoiceMembers(caches cache.Caches, g discord.GatewayGuild) {
	for _, m := range g.Members {
		m.GuildID = g.ID
		caches.AddMember(m)
	}
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
