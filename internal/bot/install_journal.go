package bot

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
)

// installListeners record in the setup journal what happens to an install:
// the main bot or a relay bot added or removed, and right after the main
// bot joins, what still prevents the server from working (missing
// permissions, roles it cannot manage, relay bots not invited yet).
func installListeners(m siteManager, relayIDs func() []snowflake.ID) []bot.EventListener {
	isRelay := func(id snowflake.ID) bool { return slices.Contains(relayIDs(), id) }
	return []bot.EventListener{
		bot.NewListenerFunc(func(e *events.GuildJoin) {
			journal.Record(journal.Event{GuildID: e.GuildID, Guild: e.Guild.Name, Kind: "bot_added",
				Detail: fmt.Sprintf("main bot added (%d members)", e.Guild.MemberCount)})
			go func() {
				time.Sleep(5 * time.Second) // let the member and role caches fill
				checkInstall(m, e.GuildID, e.Guild.Name, relayIDs())
			}()
		}),
		bot.NewListenerFunc(func(e *events.GuildLeave) {
			journal.Record(journal.Event{GuildID: e.GuildID, Guild: e.Guild.Name, Level: journal.Warning, Kind: "bot_removed",
				Detail: "main bot removed from the server (kicked, server deleted or uninstalled)"})
		}),
		bot.NewListenerFunc(func(e *events.GuildMemberJoin) {
			if e.Member.User.Bot && isRelay(e.Member.User.ID) {
				journal.Record(journal.Event{GuildID: e.GuildID, Kind: "relay_bot_added", Detail: e.Member.User.Username + " joined"})
			}
		}),
		bot.NewListenerFunc(func(e *events.GuildMemberLeave) {
			if e.User.Bot && isRelay(e.User.ID) {
				journal.Record(journal.Event{GuildID: e.GuildID, Level: journal.Warning, Kind: "relay_bot_removed", Detail: e.User.Username + " left the server"})
			}
		}),
	}
}

// checkInstall records what still blocks guildID.
func checkInstall(m siteManager, guildID snowflake.ID, name string, relays []snowflake.ID) {
	if guildAllowed(guildID) { // elsewhere the bot is about to leave
		createRolesOnJoin(m, guildID, name)
	}
	if missing := m.MissingOwnerPermissions(guildID); missing != 0 {
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Level: journal.Problem, Kind: "missing_permissions",
			Detail: "main bot lacks: " + strings.Join(permissionKeys(missing), ", ") + " (re-invite it from the site)"})
	}
	var above []string
	for _, r := range m.RoleSetupStatus(guildID) {
		if r.AboveBot {
			above = append(above, r.Name)
		}
	}
	if len(above) > 0 {
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Level: journal.Warning, Kind: "roles_above_bot",
			Detail: "roles above the bot, it cannot manage them: " + strings.Join(above, ", ")})
	}
	present := 0
	for _, id := range relays {
		if _, ok := m.MemberPresent(guildID, id); ok {
			present++
		}
	}
	if present < len(relays) {
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Level: journal.Warning, Kind: "relay_bots_missing",
			Detail: fmt.Sprintf("%d of %d relay bots on the server", present, len(relays))})
	}
}

// createRolesOnJoin creates the roles the configuration names (Speaker,
// Squad Lead, access role, ...) as soon as the main bot joins, so the
// administrator no longer has to press "Create the roles" on the site: one
// step fewer in an install people found long. Roles that already exist are
// kept; the site's button stays for repairs.
func createRolesOnJoin(m siteManager, guildID snowflake.ID, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := m.CreateMissingRoles(ctx, guildID)
	switch {
	case err != nil || rep.PermissionDenied || len(rep.Failed) > 0:
		detail := "roles not created on join"
		if err != nil {
			detail += ": " + err.Error()
		}
		if len(rep.Failed) > 0 {
			detail += "; failed: " + strings.Join(rep.Failed, ", ")
		}
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Level: journal.Warning, Kind: "roles_on_join_failed", Detail: detail})
	case len(rep.Created) > 0:
		journal.Record(journal.Event{GuildID: guildID, Guild: name, Kind: "roles_created_on_join", Detail: "created: " + strings.Join(rep.Created, ", ")})
	}
}
