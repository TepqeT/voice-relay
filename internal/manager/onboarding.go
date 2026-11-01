package manager

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

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
	// Only the default channels are sent: prompts, mode and the switch stay
	// as the server set them.
	body := onboardingDefaults{DefaultChannelIDs: defaults}
	if err := m.ownerClient.Rest.Do(rest.UpdateGuildOnboarding.Compile(nil, guildID), body, nil,
		rest.WithCtx(ctx), rest.WithReason("Voice Relay: show its channels to members")); err != nil {
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
