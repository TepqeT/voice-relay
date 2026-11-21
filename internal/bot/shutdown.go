package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
)

// ProjectStopped: Voice Relay was discontinued on 2026-11-21. At start the
// tool uninstalls itself from every server (session stopped, channels and
// panel deleted, relay bots and main bot leave; roles are kept), leaves any
// server that adds it again, and the site shows "project stopped".
const ProjectStopped = true

// guildsSettle is how long the main bot waits after start for the servers it
// is in to reach its cache before it uninstalls from them.
var guildsSettle = 30 * time.Second

// uninstallEverywhere removes the tool from every server the main bot is in.
func (b *Bot) uninstallEverywhere(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(guildsSettle):
	}
	guilds := b.siteMgr.OwnerGuilds()
	slog.InfoContext(ctx, "project stopped: uninstalling from every server", slog.Int("servers", len(guilds)))
	for _, g := range guilds {
		b.uninstallGuild(ctx, g.ID, g.Name)
	}
}

// leaveAfterJoin uninstalls from a server that added the main bot after the
// project stopped (an old invite link).
func (b *Bot) leaveAfterJoin(ctx context.Context, guildID snowflake.ID) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second): // let the caches fill
	}
	b.uninstallGuild(ctx, guildID, "")
}

// uninstallGuild is the site's uninstall: panel first, while the bot can
// still delete it, then session, channels, relay bots and main bot.
func (b *Bot) uninstallGuild(ctx context.Context, guildID snowflake.ID, name string) {
	uctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if b.panel != nil {
		b.panel.Remove(uctx, guildID)
	}
	rep, err := b.siteMgr.Uninstall(uctx, guildID)
	if err != nil && !rep.OwnerLeft {
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Level: journal.Problem, Kind: "shutdown_uninstall_failed", Detail: err.Error()})
		return
	}
	journal.Record(journal.Event{GuildID: guildID, Guild: name, Kind: "shutdown_uninstalled",
		Detail: "project stopped: tool removed from the server"})
}
