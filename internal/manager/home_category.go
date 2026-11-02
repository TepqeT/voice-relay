package manager

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/statefile"
)

// Home category: with HomeCategory set, one permanent category holds the
// panel channel and the raid voice channels, so the tool keeps to one place
// in the server. Remembered per guild in home-category.json.

type homeCategory struct {
	ID      snowflake.ID `json:"id"`
	Created bool         `json:"created"` // made by the bot: removed on uninstall
	// Private: created private to the access role. One made public first
	// is recreated: onboarding keeps hiding a channel that was public.
	Private bool `json:"private,omitempty"`
}

type homeCategories struct {
	mu     sync.Mutex
	loaded bool
	byID   map[snowflake.ID]homeCategory
}

func (m *Service) homeFile() string {
	if m.channels.opts.StateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.channels.opts.StateFile), "home-category.json")
}

func (m *Service) homeLocked() map[snowflake.ID]homeCategory {
	h := &m.home
	if !h.loaded {
		h.loaded = true
		h.byID = map[snowflake.ID]homeCategory{}
		if f := m.homeFile(); f != "" {
			if b, err := statefile.ReadFile(f); err == nil {
				_ = json.Unmarshal(b, &h.byID)
			}
		}
	}
	return h.byID
}

func (m *Service) saveHomeLocked() {
	f := m.homeFile()
	if f == "" {
		return
	}
	b, err := json.MarshalIndent(m.home.byID, "", "  ")
	if err == nil {
		err = os.WriteFile(f, b, 0o600)
	}
	if err != nil {
		slog.Warn("home category: state not saved", slog.String("file", f), slog.Any("err", err))
	}
}

// HomeCategoryName is the permanent category's name, "" when off.
func (m *Service) HomeCategoryName() string {
	if !m.HomeCategoryEnabled() {
		return ""
	}
	return strings.TrimSpace(m.channels.opts.Category)
}

// HomeCategoryEnabled reports whether the tool keeps a permanent category.
func (m *Service) HomeCategoryEnabled() bool {
	return m.channels.opts.HomeCategory && m.channels.opts.Category != ""
}

// EnsureHomeCategory returns guildID's permanent category, creating it when
// missing (a category with the configured name is reused as is).
func (m *Service) EnsureHomeCategory(ctx context.Context, guildID snowflake.ID) (snowflake.ID, error) {
	m.home.mu.Lock()
	defer m.home.mu.Unlock()
	homes := m.homeLocked()
	if h, ok := homes[guildID]; ok {
		if ch, ok := m.ownerClient.Caches.Channel(h.ID); ok && ch.Type() == discord.ChannelTypeGuildCategory {
			if h.Created && !h.Private && m.AccessRoleID(guildID) != 0 && !m.PanelStatus(guildID).Running {
				// Made public by an older version: recreate it private,
				// between sessions only (its voice channels live in it).
				if err := m.ownerClient.Rest.DeleteChannel(h.ID, rest.WithCtx(ctx), rest.WithReason("Voice Relay: category recreated private")); err != nil {
					slog.Warn("home category: old public category not deleted", slog.String("guildID", guildID.String()), slog.Any("err", err))
					return h.ID, nil
				}
				m.ownerClient.Caches.RemoveChannel(h.ID)
				delete(homes, guildID)
				m.saveHomeLocked()
				slog.Info("home category: recreating it private", slog.String("guildID", guildID.String()))
			} else {
				if h.Created {
					m.refreshHomeOverwrites(ctx, guildID, h.ID)
				}
				return h.ID, nil
			}
		}
	}
	name := strings.TrimSpace(m.channels.opts.Category)
	for ch := range m.ownerClient.Caches.ChannelsForGuild(guildID) {
		if ch.Type() == discord.ChannelTypeGuildCategory && strings.EqualFold(ch.Name(), name) {
			homes[guildID] = homeCategory{ID: ch.ID()}
			m.saveHomeLocked()
			return ch.ID(), nil
		}
	}
	cat, err := m.ownerClient.Rest.CreateGuildChannel(guildID, discord.GuildCategoryChannelCreate{
		Name:                 name,
		PermissionOverwrites: m.homeOverwrites(guildID),
	}, rest.WithCtx(ctx), rest.WithReason("Voice Relay home category"))
	if err != nil {
		return 0, err
	}
	m.ownerClient.Caches.AddChannel(cat)
	m.homeRefreshed.Store(guildID, true)
	homes[guildID] = homeCategory{ID: cat.ID(), Created: true, Private: m.AccessRoleID(guildID) != 0}
	m.saveHomeLocked()
	m.placeCategory(ctx, guildID, cat.ID())
	slog.Info("home category created", slog.String("guildID", guildID.String()), slog.String("categoryID", cat.ID().String()))
	return cat.ID(), nil
}

// RemoveHomeCategory deletes the permanent category when the bot created it
// (uninstall) and forgets it.
func (m *Service) RemoveHomeCategory(ctx context.Context, guildID snowflake.ID) {
	m.home.mu.Lock()
	h, ok := m.homeLocked()[guildID]
	delete(m.home.byID, guildID)
	m.saveHomeLocked()
	m.home.mu.Unlock()
	if !ok || !h.Created {
		return
	}
	if err := m.ownerClient.Rest.DeleteChannel(h.ID, rest.WithCtx(ctx), rest.WithReason("Voice Relay uninstalled")); err != nil {
		slog.Warn("home category: deletion refused", slog.String("guildID", guildID.String()), slog.Any("err", err))
	}
}

// homeOverwrites are the category's overwrites. The bots get what they need
// in its channels: Discord checks a new channel's overwrites against the
// bot's rights in the parent. With an access role (every member holds it)
// the category is private to it: a channel created private shows to those
// who can see it, even on servers with onboarding.
func (m *Service) homeOverwrites(guildID snowflake.ID) []discord.PermissionOverwrite {
	botAllow := voiceMember | discord.PermissionPrioritySpeaker | discord.PermissionSendMessages
	ow := []discord.PermissionOverwrite{discord.MemberPermissionOverwrite{UserID: m.ownerBotID, Allow: botAllow}}
	for _, id := range m.poolSvc.GetIDs() {
		ow = append(ow, discord.MemberPermissionOverwrite{UserID: id, Allow: voiceMember | discord.PermissionPrioritySpeaker})
	}
	if role := m.AccessRoleID(guildID); role != 0 {
		ow = append(ow,
			discord.RolePermissionOverwrite{RoleID: guildID, Deny: discord.PermissionViewChannel},
			discord.RolePermissionOverwrite{RoleID: role, Allow: discord.PermissionViewChannel})
	}
	return maskOverwrites(ow, m.grantablePermissions(guildID))
}

// refreshHomeOverwrites sets, once per run, the current overwrites on a
// category the bot created, so one made by an older version follows.
func (m *Service) refreshHomeOverwrites(ctx context.Context, guildID, categoryID snowflake.ID) {
	if _, done := m.homeRefreshed.LoadOrStore(guildID, true); done {
		return
	}
	ow := m.homeOverwrites(guildID)
	if _, err := m.ownerClient.Rest.UpdateChannel(categoryID, discord.GuildCategoryChannelUpdate{PermissionOverwrites: &ow},
		rest.WithCtx(ctx), rest.WithReason("Voice Relay home category")); err != nil {
		slog.Warn("home category: overwrites not refreshed", slog.String("guildID", guildID.String()), slog.Any("err", err))
		return
	}
	slog.Info("home category: overwrites refreshed", slog.String("guildID", guildID.String()))
}
