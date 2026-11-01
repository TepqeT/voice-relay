package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// RvRService is the RvR surface of the manager used by the bot layer.
// Kept apart from ManagerService so existing test doubles stay valid.
type RvRService interface {
	PlayOutro(guildID snowflake.ID)
	// SquadLeadsEnabled reports whether the guild has a squad lead role, in
	// which case raids start in star topology (squad leads reach the raid leads).
	SquadLeadsEnabled(guildID snowflake.ID) bool
	// DynamicChannels reports whether each raid creates its own channels,
	// deleted when it stops.
	DynamicChannels() bool
	ChannelCategory() string
	RaidChannelsOf(guildID snowflake.ID) (manager.RaidChannels, bool)
	// ReleaseRaidChannels deletes channels left without a running raid.
	ReleaseRaidChannels(ctx context.Context, guildID snowflake.ID) bool
}

func (h *CommandHandlers) dynamicChannels() bool {
	return h.rvr != nil && h.rvr.DynamicChannels()
}

// raidChannelsLine describes the channels created for the raid that started.
func (h *CommandHandlers) raidChannelsLine(guildID snowflake.ID, loc *i18n.Localizer) string {
	if !h.dynamicChannels() {
		return ""
	}
	rc, ok := h.rvr.RaidChannelsOf(guildID)
	if !ok {
		return ""
	}
	return "\n" + loc.T("raid.channels_created",
		"Category", h.rvr.ChannelCategory(),
		"RaidLead", fmt.Sprintf("<#%s>", rc.RaidLead),
		"Squads", len(rc.Squads))
}

// SetRvR enables the end-of-raid sound and squad lead calls.
func (h *CommandHandlers) SetRvR(r RvRService) { h.rvr = r }

// reportChannels remembers, per guild, the text channel where the running
// raid was started, so its quality report lands where the raid leads are.
type reportChannels struct {
	mu sync.Mutex
	m  map[snowflake.ID]snowflake.ID
}

func (r *reportChannels) set(guildID, channelID snowflake.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = make(map[snowflake.ID]snowflake.ID)
	}
	r.m[guildID] = channelID
}

func (r *reportChannels) get(guildID snowflake.ID) snowflake.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[guildID]
}

// ReportChannel returns the channel where guildID's last raid was started, or 0.
func (h *CommandHandlers) ReportChannel(guildID snowflake.ID) snowflake.ID {
	return h.reports.get(guildID)
}

// rememberReportChannel records the channel a raid was started from.
func (h *CommandHandlers) rememberReportChannel(guildID snowflake.ID, ch discord.InteractionChannel) {
	if ch.MessageChannel == nil {
		return
	}
	h.reports.set(guildID, ch.ID())
}

func logFollowUpError(err error) {
	slog.Warn("failed to send follow-up message", slog.Any("err", err))
}
