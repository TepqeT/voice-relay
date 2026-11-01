package manager

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
)

// The access role is handed to every member: the Voice Relay category is
// private to it, and Discord always shows a private channel to those who
// can see it, even on servers whose onboarding hides the channels members
// did not pick. Nobody has to ask for access any more.

// GrantAccessToAll gives the access role to every member of guildID who
// lacks it, bots excepted. Once per guild per run; the calls follow
// Discord's rate limits, so a large server takes a few minutes.
func (m *Service) GrantAccessToAll(ctx context.Context, guildID snowflake.ID) {
	role := m.AccessRoleID(guildID)
	if role == 0 {
		return
	}
	if _, done := m.grantedAll.LoadOrStore(guildID, true); done {
		return
	}
	g := guildID.String()
	given, failed := 0, 0
	var after snowflake.ID
	for {
		page, err := m.ownerClient.Rest.GetMembers(guildID, 1000, after, rest.WithCtx(ctx))
		if err != nil {
			journal.Record(journal.Event{GuildID: guildID, Level: journal.Warning, Kind: "access_role_refused",
				Detail: "could not list the members to give them the access role: " + err.Error()})
			return
		}
		for _, mem := range page {
			if mem.User.Bot || slices.Contains(mem.RoleIDs, role) {
				continue
			}
			if err := m.ownerClient.Rest.AddMemberRole(guildID, mem.User.ID, role, rest.WithCtx(ctx), rest.WithReason("Voice Relay: channels visible to every member")); err != nil {
				failed++
				if failed == 1 {
					slog.Warn("access role: grant refused", slog.String("guildID", g), slog.Any("err", err))
				}
				continue
			}
			given++
			if given%100 == 0 {
				slog.Info("access role: granting", slog.String("guildID", g), slog.Int("given", given))
			}
		}
		if len(page) < 1000 {
			break
		}
		after = page[len(page)-1].User.ID
	}
	level := journal.Info
	if failed > 0 {
		level = journal.Warning
	}
	journal.Record(journal.Event{GuildID: guildID, Level: level, Kind: "access_role_granted",
		Detail: fmt.Sprintf("access role given to %d member(s), %d refused", given, failed)})
}

// GrantAccessOnJoin gives the access role to a member who just joined.
func (m *Service) GrantAccessOnJoin(ctx context.Context, guildID, userID snowflake.ID) {
	role := m.AccessRoleID(guildID)
	if role == 0 {
		return
	}
	if err := m.ownerClient.Rest.AddMemberRole(guildID, userID, role, rest.WithCtx(ctx), rest.WithReason("Voice Relay: channels visible to every member")); err != nil {
		slog.Warn("access role: not given to a new member", slog.String("guildID", guildID.String()), slog.Any("err", err))
	}
}
