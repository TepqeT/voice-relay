package bot

import (
	"context"
	"fmt"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/guild"
	"github.com/TepqeT/voice-relay/internal/manager"
	"github.com/TepqeT/voice-relay/internal/web"
)

// siteManager is the manager surface the administration site uses.
type siteManager interface {
	OwnerInGuild(guildID snowflake.ID) bool
	MemberCanManageGuild(guildID, userID snowflake.ID) (allowed, known bool)
	MemberPresent(guildID, userID snowflake.ID) (string, bool)
	MissingOwnerPermissions(guildID snowflake.ID) discord.Permissions
	RoleSetupStatus(guildID snowflake.ID) []manager.RoleSetup
	CreateMissingRoles(ctx context.Context, guildID snowflake.ID) (manager.RoleSetupReport, error)
	OwnerGuilds() []manager.GuildRef
	GuildFacts(guildID, userID snowflake.ID) manager.GuildFacts
	Uninstall(ctx context.Context, guildID snowflake.ID) (manager.UninstallReport, error)
}

// siteOwnerPermissions is what the site's invite asks for the main bot:
// Administrator, so a server never has permissions to tick by hand. Channel
// overwrites, moves, mutes, deafens and the panel all follow from it.
const siteOwnerPermissions = discord.PermissionAdministrator

// siteRelayPermissions is what the site's invite asks for each relay bot:
// Administrator too, so a relay bot joins any channel, even a restricted one,
// whatever the server's overwrites.
const siteRelayPermissions = discord.PermissionAdministrator

// webBackend adapts the manager to the site.
type webBackend struct {
	m        siteManager
	ownerID  snowflake.ID
	botName  func() string  // owner bot display name, known once connected
	speakers []snowflake.ID // relay bot application IDs, in token order
	panel    *panelKeeper   // nil when PANEL_CHANNEL is off
}

func newWebBackend(m siteManager, ownerID snowflake.ID, botName func() string, speakerTokens []string) *webBackend {
	b := &webBackend{m: m, ownerID: ownerID, botName: botName}
	for _, tok := range speakerTokens {
		if id, ok := guild.BotUserID(tok); ok {
			b.speakers = append(b.speakers, id)
		}
	}
	return b
}

// lockedInvite is a bot invite with the server preselected and locked.
func lockedInvite(clientID, guildID snowflake.ID, perms discord.Permissions) string {
	return fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot&permissions=%d&guild_id=%s&disable_guild_select=true",
		clientID, perms, guildID)
}

func (b *webBackend) BotName() string {
	if n := b.botName(); n != "" {
		return n
	}
	return "Voice Relay"
}

func (b *webBackend) CanManage(guildID, userID snowflake.ID) (bool, bool) {
	return b.m.MemberCanManageGuild(guildID, userID)
}

func (b *webBackend) GuildStatus(guildID snowflake.ID) web.GuildStatus {
	st := web.GuildStatus{
		BotPresent:   b.m.OwnerInGuild(guildID),
		BotInviteURL: lockedInvite(b.ownerID, guildID, siteOwnerPermissions),
	}
	for i, id := range b.speakers {
		name, present := "", false
		if st.BotPresent {
			name, present = b.m.MemberPresent(guildID, id)
		}
		if name == "" {
			name = fmt.Sprintf("Voice Relay %d", i+1)
		}
		st.RelayBots = append(st.RelayBots, web.BotStatus{Name: name, Present: present, InviteURL: lockedInvite(id, guildID, siteRelayPermissions)})
	}
	if b.panel != nil {
		st.PanelChannel = b.panel.ChannelName()
	}
	if !st.BotPresent {
		return st
	}
	if missing := b.m.MissingOwnerPermissions(guildID); missing != 0 {
		st.MissingPermissions = permissionKeys(missing)
	}
	for _, r := range b.m.RoleSetupStatus(guildID) {
		st.Roles = append(st.Roles, web.RoleStatus{Purpose: r.Purpose, Name: r.Name, Exists: r.ID != 0, AboveBot: r.AboveBot})
	}
	return st
}

func (b *webBackend) SetupRoles(ctx context.Context, guildID snowflake.ID) (web.SetupReport, error) {
	rep, err := b.m.CreateMissingRoles(ctx, guildID)
	return web.SetupReport{Created: rep.Created, Existing: rep.Existing, Failed: rep.Failed, PermissionDenied: rep.PermissionDenied}, err
}

func (b *webBackend) BotGuilds() []web.GuildRef {
	gs := b.m.OwnerGuilds()
	out := make([]web.GuildRef, len(gs))
	for i, g := range gs {
		out[i] = web.GuildRef{ID: g.ID, Name: g.Name}
	}
	return out
}

func (b *webBackend) ServerFacts(guildID, userID snowflake.ID) web.ServerFacts {
	f := b.m.GuildFacts(guildID, userID)
	return web.ServerFacts{BotPresent: f.BotPresent, MemberCount: f.MemberCount, Member: f.Member, Manages: f.Manages}
}

func (b *webBackend) Uninstall(ctx context.Context, guildID snowflake.ID) (web.UninstallReport, error) {
	if b.panel != nil {
		b.panel.Remove(ctx, guildID) // while the bot can still delete it
	}
	rep, err := b.m.Uninstall(ctx, guildID)
	return web.UninstallReport{Left: rep.Left, Failed: rep.Failed, OwnerLeft: rep.OwnerLeft}, err
}

// permissionKeys lists perms as keys the site translates (perm.<key>).
func permissionKeys(perms discord.Permissions) []string {
	keys := []struct {
		p   discord.Permissions
		key string
	}{
		{discord.PermissionViewChannel, "view"},
		{discord.PermissionManageChannels, "manage_channels"},
		{discord.PermissionManageRoles, "manage_roles"},
		{discord.PermissionConnect, "connect"},
		{discord.PermissionSpeak, "speak"},
		{discord.PermissionUseVAD, "vad"},
		{discord.PermissionPrioritySpeaker, "priority"},
		{discord.PermissionMuteMembers, "mute"},
		{discord.PermissionDeafenMembers, "deafen"},
		{discord.PermissionMoveMembers, "move"},
		{discord.PermissionEmbedLinks, "embed"},
	}
	var out []string
	for _, k := range keys {
		if perms.Has(k.p) {
			out = append(out, k.key)
			perms &^= k.p
		}
	}
	if perms != 0 {
		out = append(out, "other")
	}
	return out
}

// applicationAdmins are the people who own the bot application: its owner,
// or the owner of the team that holds it.
func applicationAdmins(app *discord.Application) []snowflake.ID {
	var out []snowflake.ID
	if app == nil {
		return out
	}
	if app.Owner != nil && app.Owner.ID != 0 {
		out = append(out, app.Owner.ID)
	}
	if app.Team != nil && app.Team.OwnerID != 0 {
		out = append(out, app.Team.OwnerID)
	}
	return out
}
