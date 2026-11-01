// Package web is the administration site: an administrator signs in with
// Discord, picks one of the servers they manage, invites the bots with links
// locked to that server, checks the bot's permissions and creates the roles
// the configuration needs. Then they only hand the roles out.
package web

import (
	"context"

	"github.com/disgoorg/snowflake/v2"
)

// Backend is what the site needs from the bot. Implemented in internal/bot.
type Backend interface {
	// BotName is the display name of the owner bot.
	BotName() string
	// GuildStatus describes the installation of guildID.
	GuildStatus(guildID snowflake.ID) GuildStatus
	// CanManage reports whether userID may configure guildID, from the bot's
	// own cache; known is false when the bot cannot tell.
	CanManage(guildID, userID snowflake.ID) (allowed, known bool)
	// SetupRoles creates the missing roles of guildID.
	SetupRoles(ctx context.Context, guildID snowflake.ID) (SetupReport, error)
	// BotGuilds lists the servers the owner bot is in, for the admin page.
	BotGuilds() []GuildRef
}

// GuildRef names a server.
type GuildRef struct {
	ID   snowflake.ID
	Name string
}

// GuildStatus is the installation state of one server.
type GuildStatus struct {
	BotPresent         bool
	BotInviteURL       string // locked to the server
	Heralds            []BotStatus
	MissingPermissions []string
	Roles              []RoleStatus
}

// Ready reports whether nothing is left to do but hand out the roles.
func (g GuildStatus) Ready() bool {
	if !g.BotPresent || len(g.MissingPermissions) > 0 {
		return false
	}
	for _, h := range g.Heralds {
		if !h.Present {
			return false
		}
	}
	for _, r := range g.Roles {
		if !r.Exists || r.AboveBot {
			return false
		}
	}
	return true
}

// MissingRoles counts the roles still to create.
func (g GuildStatus) MissingRoles() int {
	n := 0
	for _, r := range g.Roles {
		if !r.Exists {
			n++
		}
	}
	return n
}

// BotStatus is one relay bot (Héraut) of a server.
type BotStatus struct {
	Name      string
	Present   bool
	InviteURL string // locked to the server
}

// RoleStatus is one configured role of a server.
type RoleStatus struct {
	Purpose  string // shown label, in French
	Name     string
	Exists   bool
	AboveBot bool
}

// SetupReport is the outcome of SetupRoles.
type SetupReport struct {
	Created          []string
	Existing         []string
	Failed           []string
	PermissionDenied bool
}
