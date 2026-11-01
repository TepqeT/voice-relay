package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// UninstallReport tells what Uninstall did.
type UninstallReport struct {
	RaidStopped     bool
	ChannelsDeleted bool
	Left            []string // relay bots (Hérauts) that left the server
	Failed          []string // relay bots that could not leave
	OwnerLeft       bool
}

// Uninstall removes the tool from guildID: stops the raid, deletes the raid
// channels, makes every relay bot leave, then the owner bot. Roles are kept:
// they may hold assignments the server wants to keep. Running it again on a
// server the owner bot already left is an error.
func (m *Service) Uninstall(ctx context.Context, guildID snowflake.ID) (UninstallReport, error) {
	var rep UninstallReport
	if !m.OwnerInGuild(guildID) {
		return rep, fmt.Errorf("the owner bot is not in guild %s", guildID)
	}

	switch err := m.StopVoiceRaid(ctx, guildID); {
	case err == nil:
		rep.RaidStopped = true
	case !errors.Is(err, ErrNoActiveSession):
		slog.Warn("uninstall: raid stop failed", slog.String("guildID", guildID.String()), slog.Any("err", err))
	}
	// Synchronous: the channels must go while the owner bot can still delete
	// them. Waits for the deletion a stop just started.
	rep.ChannelsDeleted = m.ReleaseRaidChannels(ctx, guildID)
	if m.HomeCategoryEnabled() {
		m.RemoveHomeCategory(ctx, guildID)
	}

	for _, id := range m.poolSvc.GetIDs() {
		name, present := m.MemberPresent(guildID, id)
		if !present {
			continue
		}
		if name == "" {
			name = id.String()
		}
		client, ok := m.poolSvc.GetClientByID(id)
		if !ok {
			rep.Failed = append(rep.Failed, name)
			continue
		}
		if err := client.Rest.LeaveGuild(guildID, rest.WithCtx(ctx)); err != nil {
			slog.Warn("uninstall: relay bot could not leave", slog.String("guildID", guildID.String()), slog.String("bot", name), slog.Any("err", err))
			rep.Failed = append(rep.Failed, name)
			continue
		}
		m.RemoveSpeaker(guildID, id)
		rep.Left = append(rep.Left, name)
	}

	if err := m.ownerClient.Rest.LeaveGuild(guildID, rest.WithCtx(ctx)); err != nil {
		return rep, fmt.Errorf("owner bot could not leave guild %s: %w", guildID, err)
	}
	rep.OwnerLeft = true
	slog.Info("uninstall: tool removed from guild", slog.String("guildID", guildID.String()),
		slog.Bool("raidStopped", rep.RaidStopped), slog.Bool("channelsDeleted", rep.ChannelsDeleted),
		slog.Int("left", len(rep.Left)), slog.Int("failed", len(rep.Failed)))
	return rep, nil
}
