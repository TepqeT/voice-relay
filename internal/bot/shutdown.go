package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
)

// Since 2026-11-21 Voice Relay serves a single server. Everywhere else it
// uninstalls itself at start (session stopped, channels and panel deleted,
// relay bots and main bot leave; roles are kept) and leaves any server that
// adds it. The public site only shows "project stopped".

// OnlyGuild is the one server Voice Relay still serves.
const OnlyGuild snowflake.ID = 100000000000000072

// SiteStopped replaces every page of the public site with the notice.
const SiteStopped = true

// guildAllowed reports whether Voice Relay works in guildID.
func guildAllowed(guildID snowflake.ID) bool { return guildID == OnlyGuild }

// guildsSettle is how long the main bot waits after start for the servers it
// is in to reach its cache before it leaves the other ones.
var guildsSettle = 30 * time.Second

// leaveOtherGuilds uninstalls from every server but OnlyGuild.
func (b *Bot) leaveOtherGuilds(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(guildsSettle):
	}
	for _, g := range b.siteMgr.OwnerGuilds() {
		if guildAllowed(g.ID) {
			continue
		}
		slog.InfoContext(ctx, "single server: leaving a server", slog.String("guildID", g.ID.String()))
		b.uninstallGuild(ctx, g.ID, g.Name)
	}
}

// leaveAfterJoin uninstalls from a server that added the main bot when it is
// not OnlyGuild (an old invite link).
func (b *Bot) leaveAfterJoin(ctx context.Context, guildID snowflake.ID) {
	if guildAllowed(guildID) {
		return
	}
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
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Level: journal.Problem, Kind: "single_server_uninstall_failed", Detail: err.Error()})
		return
	}
	journal.Record(journal.Event{GuildID: guildID, Guild: name, Kind: "single_server_left",
		Detail: "not the server Voice Relay serves: tool removed"})
}
