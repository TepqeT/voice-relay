package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
)

// EnsureOnboardingDefaults adds ids (the permanent category and the panel
// channel) to the server's onboarding default channels. On a server with
// onboarding on, members only see the channels they opted into: a channel
// the bot creates afterwards stays hidden in "Browse channels" otherwise.
// Done once per guild and channel set per run, whatever the outcome, so the
// panel refreshes cost no extra call. Servers without onboarding are left
// alone.
func (m *Service) EnsureOnboardingDefaults(ctx context.Context, guildID snowflake.ID, ids ...snowflake.ID) {
	ids = slices.DeleteFunc(slices.Clone(ids), func(id snowflake.ID) bool { return id == 0 })
	if len(ids) == 0 {
		return
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	key := guildID.String() + ":" + strings.Join(parts, ",")
	if _, done := m.onboarded.LoadOrStore(key, true); done {
		return
	}
	g := guildID.String()
	// Raw request with only the fields used: the full onboarding object
	// (prompts, emojis) is neither decoded nor sent back, so nothing the
	// server configured can be lost or rejected on the way.
	var ob onboardingDefaults
	if err := m.ownerClient.Rest.Do(rest.GetGuildOnboarding.Compile(nil, guildID), nil, &ob, rest.WithCtx(ctx)); err != nil {
		journal.Record(journal.Event{GuildID: guildID, Level: journal.Warning, Kind: "onboarding_refused",
			Detail: "could not read the onboarding settings: " + err.Error()})
		return
	}
	if !ob.Enabled {
		slog.Info("onboarding: off on this server, nothing to do", slog.String("guildID", g))
		return
	}
	// A category is not a channel members opt into: Discord answers
	// UNKNOWN_CHANNEL. Its channels are added one by one instead.
	ids = slices.DeleteFunc(ids, func(id snowflake.ID) bool {
		ch, ok := m.ownerClient.Caches.Channel(id)
		return ok && ch.Type() == discord.ChannelTypeGuildCategory
	})
	defaults := slices.Clone(ob.DefaultChannelIDs)
	var added []snowflake.ID
	for _, id := range ids {
		if !slices.Contains(defaults, id) {
			defaults = append(defaults, id)
			added = append(added, id)
		}
	}
	if len(added) == 0 {
		slog.Info("onboarding: channels already in the default channels", slog.String("guildID", g), slog.Int("channels", len(ids)))
		return
	}
	err := m.putOnboardingDefaults(ctx, guildID, defaults)
	if isUnknownChannel(err) || isEveryoneAccess(err) {
		// Discord checks the whole list, including the server's own
		// entries: a channel deleted since, or one @everyone can no longer
		// see, makes it refuse ours too. Drop those and try once more.
		var dropped []string
		kept := slices.DeleteFunc(slices.Clone(defaults), func(id snowflake.ID) bool {
			if slices.Contains(added, id) {
				return false
			}
			ch, ok := m.ownerClient.Caches.Channel(id)
			switch {
			case !ok || ch.GuildID() != guildID:
				dropped = append(dropped, id.String()+" (deleted)")
				return true
			case !m.everyoneCanView(guildID, ch):
				dropped = append(dropped, ch.Name()+" (hidden from @everyone)")
				return true
			}
			return false
		})
		if len(dropped) > 0 {
			slog.Info("onboarding: dropping invalid default channels", slog.String("guildID", g), slog.Any("dropped", dropped))
			err = m.putOnboardingDefaults(ctx, guildID, kept)
		}
	}
	if isEveryoneAccess(err) {
		// Then it is one of ours: name it.
		var hidden []string
		for _, id := range added {
			if ch, ok := m.ownerClient.Caches.Channel(id); ok && !m.everyoneCanView(guildID, ch) {
				hidden = append(hidden, ch.Name())
			}
		}
		slog.Warn("onboarding: Voice Relay channels hidden from @everyone", slog.String("guildID", g), slog.Any("channels", hidden))
	}
	if err != nil {
		journal.Record(journal.Event{GuildID: guildID, Level: journal.Warning, Kind: "onboarding_refused",
			Detail: "could not add the Voice Relay channels to the onboarding default channels: " + err.Error()})
		return
	}
	journal.Record(journal.Event{GuildID: guildID, Level: journal.Info, Kind: "onboarding_defaults",
		Detail: fmt.Sprintf("%d channel(s) added to the onboarding default channels", len(added))})
}

// onboardingDefaults is the part of GET and PUT /guilds/{id}/onboarding the
// bot reads and writes.
type onboardingDefaults struct {
	Enabled           bool           `json:"enabled,omitempty"`
	DefaultChannelIDs []snowflake.ID `json:"default_channel_ids"`
}

// putOnboardingDefaults sends only the default channels: prompts, mode and
// the switch stay as the server set them.
func (m *Service) putOnboardingDefaults(ctx context.Context, guildID snowflake.ID, ids []snowflake.ID) error {
	return m.ownerClient.Rest.Do(rest.UpdateGuildOnboarding.Compile(nil, guildID), onboardingDefaults{DefaultChannelIDs: ids}, nil,
		rest.WithCtx(ctx), rest.WithReason("Voice Relay: show its channels to members"))
}

// isUnknownChannel reports Discord's form error for a channel it does not
// know in default_channel_ids.
func isUnknownChannel(err error) bool {
	return errorMentions(err, "UNKNOWN_CHANNEL")
}

// isEveryoneAccess reports Discord's refusal of a default channel @everyone
// cannot see.
func isEveryoneAccess(err error) bool {
	return errorMentions(err, "DEFAULT_CHANNEL_REQUIRES_EVERYONE_ACCESS")
}

func errorMentions(err error, code string) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	var re *rest.Error
	if errors.As(err, &re) {
		text += string(re.Errors) + string(re.RsBody)
	}
	return strings.Contains(text, code)
}

// everyoneCanView reports whether @everyone sees ch: the role's server
// permissions, then the channel's overwrite for it. Unknown roles count as
// visible, so the server's list is never trimmed on a guess.
func (m *Service) everyoneCanView(guildID snowflake.ID, ch discord.GuildChannel) bool {
	role, ok := m.ownerClient.Caches.Role(guildID, guildID)
	if !ok {
		return true
	}
	p := role.Permissions
	if p.Has(discord.PermissionAdministrator) {
		return true
	}
	if ow, ok := ch.PermissionOverwrites().Role(guildID); ok {
		p = (p &^ ow.Deny) | ow.Allow
	}
	return p.Has(discord.PermissionViewChannel)
}

// SyncOnboardingDefaults keeps the Voice Relay channels right in the
// onboarding default channels: added when they are public, removed when they
// are private to the access role. Discord refuses a private default channel,
// and a leftover one would make every later edit of the server's onboarding
// fail.
func (m *Service) SyncOnboardingDefaults(ctx context.Context, guildID snowflake.ID, ids ...snowflake.ID) {
	if m.AccessRoleID(guildID) == 0 {
		m.EnsureOnboardingDefaults(ctx, guildID, ids...)
		return
	}
	m.RemoveOnboardingDefaults(ctx, guildID, ids...)
}

// RemoveOnboardingDefaults takes ids out of the onboarding default channels,
// once per guild and channel set per run.
func (m *Service) RemoveOnboardingDefaults(ctx context.Context, guildID snowflake.ID, ids ...snowflake.ID) {
	ids = slices.DeleteFunc(slices.Clone(ids), func(id snowflake.ID) bool { return id == 0 })
	if len(ids) == 0 {
		return
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	if _, done := m.onboarded.LoadOrStore("remove:"+guildID.String()+":"+strings.Join(parts, ","), true); done {
		return
	}
	var ob onboardingDefaults
	if err := m.ownerClient.Rest.Do(rest.GetGuildOnboarding.Compile(nil, guildID), nil, &ob, rest.WithCtx(ctx)); err != nil || !ob.Enabled {
		return
	}
	kept := slices.DeleteFunc(slices.Clone(ob.DefaultChannelIDs), func(id snowflake.ID) bool { return slices.Contains(ids, id) })
	if len(kept) == len(ob.DefaultChannelIDs) {
		return
	}
	if err := m.putOnboardingDefaults(ctx, guildID, kept); err != nil {
		journal.Record(journal.Event{GuildID: guildID, Level: journal.Warning, Kind: "onboarding_refused",
			Detail: "could not take the Voice Relay channels out of the onboarding default channels: " + err.Error()})
		return
	}
	slog.Info("onboarding: Voice Relay channels taken out of the default channels", slog.String("guildID", guildID.String()),
		slog.Int("removed", len(ob.DefaultChannelIDs)-len(kept)))
}
