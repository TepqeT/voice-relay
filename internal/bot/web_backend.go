package bot

import (
	"context"
	"fmt"
	"strings"

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
}

// siteOwnerPermissions is what the site's invite asks for: the speaker set,
// Deafen Members, channel creation, and the moderation the raids use.
const siteOwnerPermissions = ownerPermissions | channelPermissions |
	discord.PermissionMuteMembers | discord.PermissionMoveMembers

// webBackend adapts the manager to the site.
type webBackend struct {
	m        siteManager
	ownerID  snowflake.ID
	botName  func() string  // owner bot display name, known once connected
	speakers []snowflake.ID // Héraut application IDs, in token order
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
	return "le bot principal"
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
			name = fmt.Sprintf("Héraut %d", i+1)
		}
		st.Heralds = append(st.Heralds, web.BotStatus{Name: name, Present: present, InviteURL: lockedInvite(id, guildID, botPermissions)})
	}
	if !st.BotPresent {
		return st
	}
	if missing := b.m.MissingOwnerPermissions(guildID); missing != 0 {
		st.MissingPermissions = permissionNames(missing)
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

// permissionNames lists the French names of perms shown in Discord's settings.
func permissionNames(perms discord.Permissions) []string {
	names := []struct {
		p    discord.Permissions
		name string
	}{
		{discord.PermissionViewChannel, "Voir les salons"},
		{discord.PermissionManageChannels, "Gérer les salons"},
		{discord.PermissionManageRoles, "Gérer les rôles"},
		{discord.PermissionConnect, "Se connecter"},
		{discord.PermissionSpeak, "Parler"},
		{discord.PermissionUseVAD, "Utiliser la détection de la voix"},
		{discord.PermissionPrioritySpeaker, "Voix prioritaire"},
		{discord.PermissionMuteMembers, "Rendre les membres muets"},
		{discord.PermissionDeafenMembers, "Mettre en sourdine des membres"},
		{discord.PermissionMoveMembers, "Déplacer des membres"},
	}
	var out []string
	for _, n := range names {
		if perms.Has(n.p) {
			out = append(out, n.name)
			perms &^= n.p
		}
	}
	if perms != 0 {
		out = append(out, strings.TrimSpace(perms.String()))
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
