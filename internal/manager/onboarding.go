package manager

import (
	"context"
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
	ob, err := m.ownerClient.Rest.GetGuildOnboarding(guildID, rest.WithCtx(ctx))
	if err != nil {
		slog.Debug("onboarding: not readable", slog.String("guildID", guildID.String()), slog.Any("err", err))
		return
	}
	if ob == nil || !ob.Enabled {
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
		return
	}
	// Only the default channels are sent: prompts, mode and the switch stay
	// as the server set them.
	if _, err := m.ownerClient.Rest.UpdateGuildOnboarding(guildID, discord.GuildOnboardingUpdate{DefaultChannelIDs: &defaults},
		rest.WithCtx(ctx), rest.WithReason("Voice Relay: show its channels to members")); err != nil {
		journal.Record(journal.Event{GuildID: guildID, Level: journal.Warning, Kind: "onboarding_refused",
			Detail: "could not add the Voice Relay channels to the onboarding default channels: " + err.Error()})
		return
	}
	journal.Record(journal.Event{GuildID: guildID, Level: journal.Info, Kind: "onboarding_defaults",
		Detail: fmt.Sprintf("%d channel(s) added to the onboarding default channels", len(added))})
}
