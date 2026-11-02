package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

const panelGuild snowflake.ID = 10

func textChannel(t *testing.T, id snowflake.ID, name string) discord.GuildTextChannel {
	t.Helper()
	var ch discord.GuildTextChannel
	raw := fmt.Sprintf(`{"id":"%d","guild_id":"%d","name":%q,"type":0}`, id, panelGuild, name)
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

// fakePanelRest records the calls and mirrors created channels in the cache,
// like the gateway would.
type fakePanelRest struct {
	t          *testing.T
	caches     cache.Caches
	next       snowflake.ID
	created    []discord.GuildTextChannelCreate
	deleted    []snowflake.ID
	posted     []snowflake.ID // channel of each new message
	updated    []snowflake.ID // message updated
	lostUpdate bool           // the next update answers Unknown Message
	moved      []snowflake.ID
	reperm     []snowflake.ID // channels whose overwrites were replaced
}

func (f *fakePanelRest) CreateGuildChannel(guildID snowflake.ID, c discord.GuildChannelCreate, _ ...rest.RequestOpt) (discord.GuildChannel, error) {
	tc := c.(discord.GuildTextChannelCreate)
	f.created = append(f.created, tc)
	f.next++
	ch := textChannel(f.t, 700+f.next, tc.Name)
	f.caches.AddChannel(ch)
	return ch, nil
}
func (f *fakePanelRest) UpdateChannel(id snowflake.ID, c discord.ChannelUpdate, _ ...rest.RequestOpt) (discord.Channel, error) {
	if u, ok := c.(discord.GuildTextChannelUpdate); ok && u.PermissionOverwrites != nil {
		f.reperm = append(f.reperm, id)
		return nil, nil
	}
	f.moved = append(f.moved, id)
	return nil, nil
}
func (f *fakePanelRest) DeleteChannel(id snowflake.ID, _ ...rest.RequestOpt) error {
	f.deleted = append(f.deleted, id)
	f.caches.RemoveChannel(id)
	return nil
}
func (f *fakePanelRest) CreateMessage(channelID snowflake.ID, _ discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	f.posted = append(f.posted, channelID)
	f.next++
	return &discord.Message{ID: 900 + f.next, ChannelID: channelID}, nil
}
func (f *fakePanelRest) UpdateMessage(channelID, messageID snowflake.ID, _ discord.MessageUpdate, _ ...rest.RequestOpt) (*discord.Message, error) {
	if f.lostUpdate {
		f.lostUpdate = false
		return nil, &rest.Error{Code: 10008, Message: "Unknown Message"}
	}
	f.updated = append(f.updated, messageID)
	return &discord.Message{ID: messageID, ChannelID: channelID}, nil
}

func newTestKeeper(t *testing.T, file string) (*panelKeeper, *fakePanelRest, cache.Caches) {
	t.Helper()
	caches := cache.New(cache.WithCaches(cache.FlagsAll))
	caches.AddGuild(discord.Guild{ID: panelGuild, Name: "Test"})
	fr := &fakePanelRest{t: t, caches: caches}
	k := newPanelKeeper("raid-panel", fr, caches, func() snowflake.ID { return 1 },
		func(snowflake.ID) discord.MessageCreate { return discord.MessageCreate{Content: "panel"} }, file)
	return k, fr, caches
}

func TestPanelChannelCreatedAndKeptUpToDate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "panel.json")
	k, fr, _ := newTestKeeper(t, file)
	ctx := context.Background()

	k.Ensure(ctx, panelGuild)
	if len(fr.created) != 1 || fr.created[0].Name != "raid-panel" || len(fr.posted) != 1 {
		t.Fatalf("created %v, posted %v: want one channel and one panel", fr.created, fr.posted)
	}
	readOnly := false
	for _, ow := range fr.created[0].PermissionOverwrites {
		if r, ok := ow.(discord.RolePermissionOverwrite); ok && r.RoleID == panelGuild && r.Deny.Has(discord.PermissionSendMessages) {
			readOnly = true
		}
	}
	if !readOnly {
		t.Error("members must not be able to write in the panel channel")
	}
	chID := fr.posted[0]
	if !k.IsPanelChannel(panelGuild, chID) {
		t.Error("panel channel not remembered")
	}

	// Restart: same channel, the message is edited, nothing new posted.
	k2, fr2, caches2 := newTestKeeper(t, file)
	caches2.AddChannel(textChannel(t, chID, "raid-panel"))
	k2.Ensure(ctx, panelGuild)
	if len(fr2.created) != 0 || len(fr2.posted) != 0 || len(fr2.updated) != 1 {
		t.Fatalf("after restart: created %v posted %v updated %v", fr2.created, fr2.posted, fr2.updated)
	}

	// Nothing changed: no Discord call at all.
	k2.Ensure(ctx, panelGuild)
	if len(fr2.updated) != 1 || len(fr2.posted) != 0 {
		t.Fatalf("unchanged panel edited again: updated %v posted %v", fr2.updated, fr2.posted)
	}

	// The message vanished while the bot was offline: the periodic forced
	// edit finds out and reposts.
	fr2.lostUpdate = true
	k2.ensure(ctx, panelGuild, true)
	if len(fr2.posted) != 1 || fr2.posted[0] != chID {
		t.Fatalf("lost message not reposted: %v", fr2.posted)
	}

	// Someone deletes the message: reposted at once.
	k2.mu.Lock()
	msgID := k2.state[panelGuild].MessageID
	k2.mu.Unlock()
	k2.OnMessageDeleted(ctx, panelGuild, 424242) // another message
	k2.OnMessageDeleted(ctx, panelGuild, msgID)
	if len(fr2.posted) != 2 {
		t.Fatalf("deleted panel message not reposted: %v", fr2.posted)
	}

	// Someone deleted the channel: it comes back.
	caches2.RemoveChannel(chID)
	k2.OnChannelDeleted(ctx, panelGuild, chID)
	if len(fr2.created) != 1 || len(fr2.posted) != 3 {
		t.Fatalf("deleted channel not recreated: created %v posted %v", fr2.created, fr2.posted)
	}

	// Uninstall removes the channel the bot created.
	newCh := fr2.posted[2]
	k2.Remove(ctx, panelGuild)
	if len(fr2.deleted) != 1 || fr2.deleted[0] != newCh || k2.IsPanelChannel(panelGuild, newCh) {
		t.Fatalf("uninstall: deleted %v", fr2.deleted)
	}
}

func TestPanelChannelReusesExistingChannel(t *testing.T) {
	k, fr, caches := newTestKeeper(t, "")
	caches.AddChannel(textChannel(t, 555, "Raid-Panel"))
	k.Ensure(context.Background(), panelGuild)
	if len(fr.created) != 0 || len(fr.posted) != 1 || fr.posted[0] != 555 {
		t.Fatalf("created %v posted %v: the existing channel must be used", fr.created, fr.posted)
	}
	// Not the bot's channel: uninstall leaves it.
	k.Remove(context.Background(), panelGuild)
	if len(fr.deleted) != 0 {
		t.Fatalf("a channel the server made was deleted: %v", fr.deleted)
	}
}

func TestPanelChannelIgnoresOtherDeletions(t *testing.T) {
	k, fr, _ := newTestKeeper(t, "")
	k.Ensure(context.Background(), panelGuild)
	k.OnChannelDeleted(context.Background(), panelGuild, 12345)
	if len(fr.created) != 1 {
		t.Fatalf("another channel's deletion recreated the panel: %v", fr.created)
	}
}

// With a home category the panel channel is created inside it.
func TestPanelChannelInHomeCategory(t *testing.T) {
	k, fr, _ := newTestKeeper(t, "")
	k.home = func(context.Context, snowflake.ID) (snowflake.ID, error) { return 444, nil }
	k.Ensure(context.Background(), panelGuild)
	if len(fr.created) != 1 || fr.created[0].ParentID != 444 {
		t.Fatalf("created %+v: the panel channel must sit in the home category", fr.created)
	}
}

// The bot only puts in the panel channel's overwrites what it holds itself:
// Discord refuses the whole channel (50013) otherwise.
func TestPanelChannelOverwritesWithinBotPermissions(t *testing.T) {
	k, fr, caches := newTestKeeper(t, "")
	caches.SetSelfUser(discord.OAuth2User{User: discord.User{ID: 1, Bot: true}})
	caches.AddRole(discord.Role{ID: panelGuild, GuildID: panelGuild, Name: "@everyone"})
	caches.AddRole(discord.Role{ID: 50, GuildID: panelGuild, Name: "Voice Relay", Position: 1,
		Permissions: discord.PermissionViewChannel | discord.PermissionSendMessages | discord.PermissionManageChannels | discord.PermissionManageRoles})
	caches.AddMember(discord.Member{GuildID: panelGuild, User: discord.User{ID: 1, Bot: true}, RoleIDs: []snowflake.ID{50}})
	k.Ensure(context.Background(), panelGuild)
	if len(fr.created) != 1 {
		t.Fatalf("created %v", fr.created)
	}
	for _, ow := range fr.created[0].PermissionOverwrites {
		var p discord.Permissions
		switch v := ow.(type) {
		case discord.RolePermissionOverwrite:
			p = v.Allow | v.Deny
		case discord.MemberPermissionOverwrite:
			p = v.Allow | v.Deny
		}
		if p.Has(discord.PermissionEmbedLinks) || p.Has(discord.PermissionCreatePublicThreads) {
			t.Fatalf("overwrite %+v carries a permission the bot does not hold", ow)
		}
	}
}

// The panel is visible to @everyone explicitly, and a channel the bot made
// before gets the current overwrites once after a restart.
func TestPanelChannelVisibleToEveryone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "panel.json")
	k, fr, _ := newTestKeeper(t, file)
	k.Ensure(context.Background(), panelGuild)
	visible := false
	for _, ow := range fr.created[0].PermissionOverwrites {
		if r, ok := ow.(discord.RolePermissionOverwrite); ok && r.RoleID == panelGuild && r.Allow.Has(discord.PermissionViewChannel) {
			visible = true
		}
	}
	if !visible {
		t.Fatal("@everyone must be allowed to view the panel channel")
	}
	chID := fr.posted[0]
	k2, fr2, caches2 := newTestKeeper(t, file)
	caches2.AddChannel(textChannel(t, chID, "raid-panel"))
	k2.Ensure(context.Background(), panelGuild)
	k2.Ensure(context.Background(), panelGuild)
	if len(fr2.reperm) != 1 || fr2.reperm[0] != chID {
		t.Fatalf("overwrites refreshed on %v, want once on %d", fr2.reperm, chID)
	}
}

// With a view role the panel is private to it, still read-only.
func TestPanelChannelPrivateToViewRole(t *testing.T) {
	k, fr, _ := newTestKeeper(t, "")
	k.viewRole = func(snowflake.ID) snowflake.ID { return 77 }
	k.Ensure(context.Background(), panelGuild)
	var hidden, shown bool
	for _, ow := range fr.created[0].PermissionOverwrites {
		if r, ok := ow.(discord.RolePermissionOverwrite); ok {
			hidden = hidden || (r.RoleID == panelGuild && r.Deny.Has(discord.PermissionViewChannel))
			shown = shown || (r.RoleID == 77 && r.Allow.Has(discord.PermissionViewChannel) && r.Deny.Has(discord.PermissionSendMessages))
		}
	}
	if !hidden || !shown {
		t.Fatalf("overwrites %+v: want hidden from @everyone, read-only for the role", fr.created[0].PermissionOverwrites)
	}
}

// A panel channel the bot made public is deleted, taken out of the
// onboarding defaults and recreated private: onboarding keeps hiding a
// channel that was public first.
func TestPanelChannelRecreatedPrivate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "panel.json")
	k, fr, _ := newTestKeeper(t, file)
	k.Ensure(context.Background(), panelGuild)
	old := fr.posted[0]

	k2, fr2, caches2 := newTestKeeper(t, file)
	fr2.next = 50 // new channel IDs must differ from the first keeper's
	caches2.AddChannel(textChannel(t, old, "raid-panel"))
	k2.viewRole = func(snowflake.ID) snowflake.ID { return 77 }
	var unlisted []snowflake.ID
	k2.unlist = func(_ context.Context, _ snowflake.ID, ids ...snowflake.ID) { unlisted = append(unlisted, ids...) }
	k2.Ensure(context.Background(), panelGuild)
	if len(fr2.deleted) != 1 || fr2.deleted[0] != old || len(unlisted) != 1 || unlisted[0] != old {
		t.Fatalf("deleted %v unlisted %v, want the old public channel %d", fr2.deleted, unlisted, old)
	}
	if len(fr2.created) != 1 || len(fr2.posted) != 1 || fr2.posted[0] == old {
		t.Fatalf("created %v posted %v: want a new channel with the panel", fr2.created, fr2.posted)
	}
	// Done once: a restart keeps the private channel.
	k3, fr3, caches3 := newTestKeeper(t, file)
	caches3.AddChannel(textChannel(t, fr2.posted[0], "raid-panel"))
	k3.viewRole = k2.viewRole
	k3.Ensure(context.Background(), panelGuild)
	if len(fr3.deleted) != 0 || len(fr3.created) != 0 {
		t.Fatalf("the private channel must be kept: deleted %v created %v", fr3.deleted, fr3.created)
	}
}
