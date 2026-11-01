package manager

import (
	"context"
	"fmt"
	"github.com/TepqeT/voice-relay/internal/journal"
	"log/slog"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Automatic setup: the roles the configuration names (speaker, squad lead,
// access, board, classes) are created when the server lacks them, so
// an administrator only has to hand them out. Running it again creates only
// what is still missing.

// RoleSetup is one role the configuration needs in a guild.
type RoleSetup struct {
	Purpose  string       // what the role is for: raid_lead (speaker), squad_lead, access, board, class
	Name     string       // configured name or ID
	ID       snowflake.ID // 0 when the guild has no such role
	AboveBot bool         // the role sits at or above the owner bot's highest role: the bot cannot manage it
	ByID     bool         // configured as an ID: cannot be created by name
}

// RoleSetupReport tells what CreateMissingRoles did.
type RoleSetupReport struct {
	Created          []string
	Existing         []string
	Failed           []string
	PermissionDenied bool // Discord refused for lack of Manage Roles
}

// wantedRoles lists the roles the configuration names, in display order.
func (m *Service) wantedRoles() []RoleSetup {
	var out []RoleSetup
	add := func(purpose, name string) {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, RoleSetup{Purpose: purpose, Name: name})
		}
	}
	add("raid_lead", m.rvr.opts.RaidLeadRole)
	add("squad_lead", m.rvr.opts.SquadLeadRole)
	add("access", m.channels.opts.AccessRole)
	add("board", m.channels.opts.BoardRole)
	for _, c := range m.sort.opts.ClassRoles {
		add("class", c)
	}
	return dedupeRoles(out)
}

// dedupeRoles drops repeated names (one role used for two purposes).
func dedupeRoles(in []RoleSetup) []RoleSetup {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, r := range in {
		k := strings.ToLower(r.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// RoleSetupStatus resolves every configured role in guildID.
func (m *Service) RoleSetupStatus(guildID snowflake.ID) []RoleSetup {
	top := m.botTopRolePosition(guildID)
	roles := m.wantedRoles()
	for i := range roles {
		r := &roles[i]
		_, err := snowflake.Parse(r.Name)
		r.ByID = err == nil
		r.ID = m.resolveRole(guildID, r.Name)
		if r.ID == 0 {
			continue
		}
		if role, ok := m.ownerClient.Caches.Role(guildID, r.ID); ok && top >= 0 && role.Position >= top {
			r.AboveBot = true
		}
	}
	return roles
}

// botTopRolePosition is the position of the owner bot's highest role, -1 when
// unknown (not cached yet).
func (m *Service) botTopRolePosition(guildID snowflake.ID) int {
	self, ok := m.ownerClient.Caches.SelfMember(guildID)
	if !ok {
		return -1
	}
	top := 0
	for _, id := range self.RoleIDs {
		if r, ok := m.ownerClient.Caches.Role(guildID, id); ok && r.Position > top {
			top = r.Position
		}
	}
	return top
}

// CreateMissingRoles creates the configured roles guildID lacks. Roles given
// as IDs cannot be created and are reported as failed.
func (m *Service) CreateMissingRoles(ctx context.Context, guildID snowflake.ID) (RoleSetupReport, error) {
	var rep RoleSetupReport
	if _, ok := m.ownerClient.Caches.Guild(guildID); !ok {
		return rep, fmt.Errorf("the owner bot is not in guild %s", guildID)
	}
	for _, r := range m.RoleSetupStatus(guildID) {
		switch {
		case r.ID != 0:
			rep.Existing = append(rep.Existing, r.Name)
			continue
		case r.ByID:
			rep.Failed = append(rep.Failed, r.Name)
			continue
		}
		role, err := m.ownerClient.Rest.CreateRole(guildID, discord.RoleCreate{Name: r.Name},
			rest.WithCtx(ctx), rest.WithReason("Voice Relay setup"))
		if err != nil {
			rep.Failed = append(rep.Failed, r.Name)
			if isPermissionError(err) {
				rep.PermissionDenied = true
			}
			journal.Record(journal.Event{GuildID: guildID, Level: journal.Problem, Kind: "role_refused", Detail: "could not create role " + r.Name + ": " + err.Error()})
			continue
		}
		m.ownerClient.Caches.AddRole(*role)
		rep.Created = append(rep.Created, r.Name)
	}
	slog.Info("setup: roles checked", slog.String("guildID", guildID.String()),
		slog.Int("created", len(rep.Created)), slog.Int("existing", len(rep.Existing)), slog.Int("failed", len(rep.Failed)))
	return rep, nil
}

// OwnerInGuild reports whether the owner bot is in guildID.
func (m *Service) OwnerInGuild(guildID snowflake.ID) bool {
	_, ok := m.ownerClient.Caches.Guild(guildID)
	return ok
}

// MemberCanManageGuild reports whether userID may configure guildID (Manage
// Server or Administrator, or owner). known is false when the member is not
// cached, in which case the caller decides.
func (m *Service) MemberCanManageGuild(guildID, userID snowflake.ID) (allowed, known bool) {
	mem, ok := m.ownerClient.Caches.Member(guildID, userID)
	if !ok {
		return false, false
	}
	p := m.ownerClient.Caches.MemberPermissions(mem)
	return p.Has(discord.PermissionAdministrator) || p.Has(discord.PermissionManageGuild), true
}

// MemberPresent reports whether userID (a bot, typically) is in guildID, and
// its display name when cached.
func (m *Service) MemberPresent(guildID, userID snowflake.ID) (string, bool) {
	mem, ok := m.ownerClient.Caches.Member(guildID, userID)
	if !ok {
		return "", false
	}
	return mem.EffectiveName(), true
}

// MissingOwnerPermissions lists the guild permissions the owner bot lacks for
// raids (channel creation, moderation, moves). Empty when unknown.
func (m *Service) MissingOwnerPermissions(guildID snowflake.ID) discord.Permissions {
	if _, ok := m.ownerClient.Caches.SelfMember(guildID); !ok {
		return 0
	}
	need := raidBotNeeds | discord.PermissionManageRoles | discord.PermissionDeafenMembers
	return need &^ m.grantablePermissions(guildID)
}

// GuildRef names a guild the owner bot is in.
type GuildRef struct {
	ID   snowflake.ID
	Name string
}

// OwnerGuilds lists the guilds the owner bot is in, by name.
func (m *Service) OwnerGuilds() []GuildRef {
	var out []GuildRef
	for g := range m.ownerClient.Caches.Guilds() {
		out = append(out, GuildRef{ID: g.ID, Name: g.Name})
	}
	slices.SortFunc(out, func(a, b GuildRef) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// GuildFacts is what the owner bot knows of a guild and one of its members,
// to spot a subscription lent to servers its holder does not run.
type GuildFacts struct {
	BotPresent  bool
	Name        string
	MemberCount int
	Member      bool // the user is in the guild (as far as the cache knows)
	Manages     bool // the user has Manage Server or Administrator there
}

// GuildFacts describes guildID as seen from userID.
func (m *Service) GuildFacts(guildID, userID snowflake.ID) GuildFacts {
	g, ok := m.ownerClient.Caches.Guild(guildID)
	if !ok {
		return GuildFacts{}
	}
	f := GuildFacts{BotPresent: true, Name: g.Name, MemberCount: g.MemberCount}
	if mem, ok := m.ownerClient.Caches.Member(guildID, userID); ok {
		f.Member = true
		p := m.ownerClient.Caches.MemberPermissions(mem)
		f.Manages = p.Has(discord.PermissionAdministrator) || p.Has(discord.PermissionManageGuild)
	}
	return f
}
