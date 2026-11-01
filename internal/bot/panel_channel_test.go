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
}

func (f *fakePanelRest) CreateGuildChannel(guildID snowflake.ID, c discord.GuildChannelCreate, _ ...rest.RequestOpt) (discord.GuildChannel, error) {
	tc := c.(discord.GuildTextChannelCreate)
	f.created = append(f.created, tc)
	f.next++
	ch := textChannel(f.t, 700+f.next, tc.Name)
	f.caches.AddChannel(ch)
	return ch, nil
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

	// Someone deleted the message: a new one is posted.
	fr2.lostUpdate = true
	k2.Ensure(ctx, panelGuild)
	if len(fr2.posted) != 1 || fr2.posted[0] != chID {
		t.Fatalf("lost message not reposted: %v", fr2.posted)
	}

	// Someone deleted the channel: it comes back.
	caches2.RemoveChannel(chID)
	k2.OnChannelDeleted(ctx, panelGuild, chID)
	if len(fr2.created) != 1 || len(fr2.posted) != 2 {
		t.Fatalf("deleted channel not recreated: created %v posted %v", fr2.created, fr2.posted)
	}

	// Uninstall removes the channel the bot created.
	newCh := fr2.posted[1]
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
