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
	// ServerFacts describes guildID as seen from userID, to spot lending.
	ServerFacts(guildID, userID snowflake.ID) ServerFacts
	// Uninstall removes the tool from guildID: stops the raid, deletes the
	// raid channels, makes the relay bots then the owner bot leave.
	Uninstall(ctx context.Context, guildID snowflake.ID) (UninstallReport, error)
}

// UninstallReport is the outcome of Uninstall.
type UninstallReport struct {
	Left      []string // relay bots that left the server
	Failed    []string // relay bots that could not leave
	OwnerLeft bool
}

// ServerFacts is what the bot knows of a server and a subscriber in it.
type ServerFacts struct {
	BotPresent  bool
	MemberCount int
	Member      bool // the subscriber is a member of the server
	Manages     bool // the subscriber has Manage Server there
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
	RelayBots          []BotStatus
	MissingPermissions []string // keys translated by the site: perm.<key>
	Roles              []RoleStatus
	PanelChannel       string // channel where the bot keeps the control panel, "" when off
	Onboarding         string // panel among the onboarding default channels: "ok", "missing", "" when no onboarding
}

// Ready reports whether nothing is left to do but hand out the roles.
func (g GuildStatus) Ready() bool {
	if !g.BotPresent || len(g.MissingPermissions) > 0 {
		return false
	}
	for _, h := range g.RelayBots {
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

// RelayCount is the number of relay bots, present or not.
func (g GuildStatus) RelayCount() int { return len(g.RelayBots) }

// RelayPresent counts the relay bots already in the server.
func (g GuildStatus) RelayPresent() int {
	n := 0
	for _, h := range g.RelayBots {
		if h.Present {
			n++
		}
	}
	return n
}

// NextRelay is the first relay bot still to invite, nil when all are in. The site offers it as a single button so the administrator chains the
// authorisations Discord asks for, one per bot, without picking from a list.
func (g GuildStatus) NextRelay() *BotStatus {
	for i := range g.RelayBots {
		if !g.RelayBots[i].Present {
			return &g.RelayBots[i]
		}
	}
	return nil
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

// BotStatus is one relay bot of a server.
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
