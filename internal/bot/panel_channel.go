package bot

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/TepqeT/voice-relay/internal/journal"
	"github.com/TepqeT/voice-relay/internal/ratelimit"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Panel channel: a text channel (PANEL_CHANNEL) where the control panel is
// always shown, so raid leads never type a command. The bot creates the
// channel when missing (read-only for members), keeps a single panel message
// in it, refreshes it at every start (language or settings changed) and
// recreates the channel if someone deletes it.

// panelState is what is remembered per guild, across restarts.
type panelState struct {
	ChannelID snowflake.ID `json:"channel_id"`
	MessageID snowflake.ID `json:"message_id"`
	Created   bool         `json:"created"` // the bot created the channel: removed on uninstall
}

// panelRest is the part of the Discord REST API the panel channel uses.
type panelRest interface {
	CreateGuildChannel(guildID snowflake.ID, c discord.GuildChannelCreate, opts ...rest.RequestOpt) (discord.GuildChannel, error)
	DeleteChannel(channelID snowflake.ID, opts ...rest.RequestOpt) error
	CreateMessage(channelID snowflake.ID, m discord.MessageCreate, opts ...rest.RequestOpt) (*discord.Message, error)
	UpdateMessage(channelID, messageID snowflake.ID, m discord.MessageUpdate, opts ...rest.RequestOpt) (*discord.Message, error)
	UpdateChannel(channelID snowflake.ID, c discord.ChannelUpdate, opts ...rest.RequestOpt) (discord.Channel, error)
}

type panelKeeper struct {
	name   string // configured channel name or ID
	rest   panelRest
	caches cache.Caches
	selfID func() snowflake.ID
	build  func(guildID snowflake.ID) discord.MessageCreate
	file   string // JSON state, "" keeps it in memory
	// home, when set, returns the permanent category the panel channel lives
	// in (created on demand).
	home func(ctx context.Context, guildID snowflake.ID) (snowflake.ID, error)
	// onboard, when set, adds the category and the panel channel to the
	// server's onboarding default channels so members see them.
	onboard func(ctx context.Context, guildID snowflake.ID, ids ...snowflake.ID)

	mu    sync.Mutex
	state map[snowflake.ID]panelState
	busy  map[snowflake.ID]bool   // an Ensure is running for the guild
	shown map[snowflake.ID]string // last panel sent, to skip identical edits
	fixed map[snowflake.ID]bool   // overwrites of a created channel refreshed this run
	retry *ratelimit.Limiter      // spaces the attempts on servers without a panel
}

func newPanelKeeper(name string, r panelRest, caches cache.Caches, selfID func() snowflake.ID, build func(snowflake.ID) discord.MessageCreate, file string) *panelKeeper {
	k := &panelKeeper{name: strings.TrimSpace(name), rest: r, caches: caches, selfID: selfID, build: build, file: file,
		state: map[snowflake.ID]panelState{}, busy: map[snowflake.ID]bool{}, shown: map[snowflake.ID]string{}, fixed: map[snowflake.ID]bool{},
		retry: ratelimit.New(5*time.Minute, 1)}
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			if err := json.Unmarshal(b, &k.state); err != nil {
				slog.Warn("panel channel: unreadable state, starting fresh", slog.String("file", file), slog.Any("err", err))
				k.state = map[snowflake.ID]panelState{}
			}
		}
	}
	return k
}

// ChannelName is the configured panel channel, shown on the site.
func (k *panelKeeper) ChannelName() string { return k.name }

// Ensure makes sure guildID has the panel channel and an up-to-date panel
// message. Concurrent calls for the same guild collapse into one. An
// unchanged panel costs no Discord call.
func (k *panelKeeper) Ensure(ctx context.Context, guildID snowflake.ID) {
	k.ensure(ctx, guildID, false)
}

// ensure with force set edits the message even when nothing changed, which
// also notices a message deleted while the bot was not looking.
func (k *panelKeeper) ensure(ctx context.Context, guildID snowflake.ID, force bool) {
	k.mu.Lock()
	if k.busy[guildID] {
		k.mu.Unlock()
		return
	}
	k.busy[guildID] = true
	st := k.state[guildID]
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		delete(k.busy, guildID)
		k.mu.Unlock()
	}()

	var parent snowflake.ID
	if k.home != nil {
		var err error
		if parent, err = k.home(ctx, guildID); err != nil {
			journal.Record(journal.Event{GuildID: guildID, Level: journal.Problem, Kind: "home_category_refused", Detail: "could not create the category: " + err.Error()})
		}
	}
	channelID := k.findChannel(guildID, st.ChannelID)
	if channelID != 0 && channelID == st.ChannelID && st.Created && parent != 0 {
		k.moveInto(ctx, channelID, parent)
	}
	if channelID == 0 {
		ch, err := k.rest.CreateGuildChannel(guildID, discord.GuildTextChannelCreate{
			Name:                 k.name,
			ParentID:             parent,
			PermissionOverwrites: k.overwrites(guildID),
		}, rest.WithCtx(ctx), rest.WithReason("Voice Relay control panel"))
		if err != nil {
			journal.Record(journal.Event{GuildID: guildID, Level: journal.Problem, Kind: "panel_channel_refused", Detail: "could not create the panel channel: " + err.Error()})
			return
		}
		channelID = ch.ID()
		st = panelState{ChannelID: channelID, Created: true}
	} else if channelID != st.ChannelID {
		st = panelState{ChannelID: channelID} // an existing channel the server made
	}

	k.refreshOverwrites(ctx, guildID, channelID, st.Created)
	if k.onboard != nil {
		k.onboard(ctx, guildID, parent, channelID)
	}

	msg := k.build(guildID)
	sig := panelSignature(msg)
	k.mu.Lock()
	same := !force && st.MessageID != 0 && channelID == st.ChannelID && k.shown[guildID] == sig
	k.mu.Unlock()
	if same {
		return // nothing changed since the last edit
	}
	if st.MessageID != 0 {
		_, err := k.rest.UpdateMessage(channelID, st.MessageID, discord.MessageUpdate{
			Content:         &msg.Content,
			Embeds:          &msg.Embeds,
			Components:      &msg.Components,
			AllowedMentions: msg.AllowedMentions,
		}, rest.WithCtx(ctx))
		if err == nil {
			k.save(guildID, st)
			k.remember(guildID, sig)
			return
		}
		if !isUnknownMessage(err) {
			slog.Warn("panel channel: panel update failed", slog.String("guildID", guildID.String()), slog.Any("err", err))
			return
		}
	}
	m, err := k.rest.CreateMessage(channelID, msg, rest.WithCtx(ctx))
	if err != nil {
		journal.Record(journal.Event{GuildID: guildID, Level: journal.Problem, Kind: "panel_post_refused", Detail: "could not post the panel: " + err.Error()})
		return
	}
	st.MessageID = m.ID
	k.save(guildID, st)
	k.remember(guildID, sig)
	slog.Info("panel channel: panel posted", slog.String("guildID", guildID.String()), slog.String("channelID", channelID.String()))
}

// Watch refreshes every known panel every interval until ctx ends, so the
// live counts follow people joining and leaving; unchanged panels cost no
// Discord call.
func (k *panelKeeper) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	// Every forceEvery ticks the edit is forced, in case the message vanished
	// without an event (bot offline at the time).
	const forceEvery = 40
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			k.mu.Lock()
			guilds := make([]snowflake.ID, 0, len(k.state))
			known := make(map[snowflake.ID]bool, len(k.state))
			for g := range k.state {
				guilds = append(guilds, g)
				known[g] = true
			}
			k.mu.Unlock()
			for _, g := range guilds {
				k.ensure(ctx, g, tick%forceEvery == 0)
			}
			// Servers still without a panel (refused at join, permission
			// fixed since): try again, at most every few minutes each.
			for g := range k.caches.Guilds() {
				if !known[g.ID] && k.retry.Allow(g.ID.String()) {
					k.Ensure(ctx, g.ID)
				}
			}
		}
	}
}

func (k *panelKeeper) remember(guildID snowflake.ID, sig string) {
	k.mu.Lock()
	k.shown[guildID] = sig
	k.mu.Unlock()
}

// panelSignature identifies what a panel message shows.
func panelSignature(m discord.MessageCreate) string {
	b, _ := json.Marshal(struct {
		C string
		E []discord.Embed
		K []discord.LayoutComponent
	}{m.Content, m.Embeds, m.Components})
	return string(b)
}

// moveInto puts the bot's panel channel into the home category when it sits
// elsewhere (a panel made before the category existed).
func (k *panelKeeper) moveInto(ctx context.Context, channelID, parent snowflake.ID) {
	ch, ok := k.caches.Channel(channelID)
	if !ok {
		return
	}
	if gc, ok := ch.(discord.GuildMessageChannel); ok {
		if p := gc.ParentID(); p != nil && *p == parent {
			return
		}
	}
	if _, err := k.rest.UpdateChannel(channelID, discord.GuildTextChannelUpdate{ParentID: &parent}, rest.WithCtx(ctx)); err != nil {
		slog.Warn("panel channel: move into the home category refused", slog.String("channelID", channelID.String()), slog.Any("err", err))
	}
}

// OnMessageDeleted reposts the panel when its message was deleted.
func (k *panelKeeper) OnMessageDeleted(ctx context.Context, guildID, messageID snowflake.ID) {
	k.mu.Lock()
	st, ok := k.state[guildID]
	k.mu.Unlock()
	if !ok || st.MessageID != messageID {
		return
	}
	st.MessageID = 0
	k.save(guildID, st)
	k.remember(guildID, "")
	k.Ensure(ctx, guildID)
}

// OnChannelDeleted recreates the panel when its channel was deleted.
func (k *panelKeeper) OnChannelDeleted(ctx context.Context, guildID, channelID snowflake.ID) {
	k.mu.Lock()
	st, ok := k.state[guildID]
	k.mu.Unlock()
	if !ok || st.ChannelID != channelID {
		return
	}
	k.save(guildID, panelState{})
	k.remember(guildID, "")
	k.Ensure(ctx, guildID)
}

// Remove deletes the panel channel when the bot created it (uninstall) and
// forgets the guild.
func (k *panelKeeper) Remove(ctx context.Context, guildID snowflake.ID) {
	k.mu.Lock()
	st, ok := k.state[guildID]
	delete(k.state, guildID)
	k.mu.Unlock()
	k.persist()
	if !ok || !st.Created || st.ChannelID == 0 {
		return
	}
	if err := k.rest.DeleteChannel(st.ChannelID, rest.WithCtx(ctx), rest.WithReason("Voice Relay uninstalled")); err != nil {
		slog.Warn("panel channel: deletion refused", slog.String("guildID", guildID.String()), slog.Any("err", err))
	}
}

// IsPanelChannel reports whether channelID is guildID's panel channel.
func (k *panelKeeper) IsPanelChannel(guildID, channelID snowflake.ID) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return channelID != 0 && k.state[guildID].ChannelID == channelID
}

// findChannel returns the panel channel: the remembered one if it still
// exists, else a text channel matching the configured name or ID.
func (k *panelKeeper) findChannel(guildID, remembered snowflake.ID) snowflake.ID {
	if remembered != 0 {
		if ch, ok := k.caches.Channel(remembered); ok && ch.Type() == discord.ChannelTypeGuildText {
			return remembered
		}
	}
	id, idErr := snowflake.Parse(k.name)
	want := strings.ToLower(strings.ReplaceAll(k.name, " ", "-"))
	for ch := range k.caches.ChannelsForGuild(guildID) {
		if ch.Type() != discord.ChannelTypeGuildText {
			continue
		}
		if (idErr == nil && ch.ID() == id) || strings.EqualFold(ch.Name(), want) {
			return ch.ID()
		}
	}
	return 0
}

// overwrites make the created channel read-only for members (they still see
// the panel and click its buttons) and writable for the bot.
func (k *panelKeeper) overwrites(guildID snowflake.ID) []discord.PermissionOverwrite {
	ow := []discord.PermissionOverwrite{
		// View is allowed explicitly: on servers where @everyone cannot see
		// channels by default, the panel would stay hidden, and Discord
		// refuses an onboarding default channel @everyone cannot see.
		discord.RolePermissionOverwrite{RoleID: guildID,
			Allow: discord.PermissionViewChannel | discord.PermissionReadMessageHistory,
			Deny:  discord.PermissionSendMessages | discord.PermissionCreatePublicThreads | discord.PermissionCreatePrivateThreads | discord.PermissionAddReactions},
	}
	if self := k.selfID(); self != 0 {
		ow = append(ow, discord.MemberPermissionOverwrite{UserID: self, Allow: discord.PermissionViewChannel | discord.PermissionSendMessages | discord.PermissionEmbedLinks})
	}
	return maskPanelOverwrites(ow, k.grantable(guildID))
}

// refreshOverwrites sets, once per run, the current overwrites on a panel
// channel the bot created, so channels made by an older version get the
// rules of this one. A channel the server made is left as it is.
func (k *panelKeeper) refreshOverwrites(ctx context.Context, guildID, channelID snowflake.ID, created bool) {
	k.mu.Lock()
	done := k.fixed[guildID]
	k.fixed[guildID] = true
	k.mu.Unlock()
	if done || !created || channelID == 0 {
		return
	}
	ow := k.overwrites(guildID)
	if _, err := k.rest.UpdateChannel(channelID, discord.GuildTextChannelUpdate{PermissionOverwrites: &ow}, rest.WithCtx(ctx), rest.WithReason("Voice Relay control panel")); err != nil {
		slog.Warn("panel channel: overwrites not refreshed", slog.String("guildID", guildID.String()), slog.Any("err", err))
	}
}

// grantable is what the bot may put in an overwrite: Discord refuses
// (50013) an overwrite carrying a permission the bot does not hold itself.
// Unknown (not cached yet) means everything.
func (k *panelKeeper) grantable(guildID snowflake.ID) discord.Permissions {
	self, ok := k.caches.SelfMember(guildID)
	if !ok {
		return discord.PermissionsAll
	}
	p := k.caches.MemberPermissions(self)
	if p.Has(discord.PermissionAdministrator) {
		return discord.PermissionsAll
	}
	return p
}

// maskPanelOverwrites keeps in each overwrite only what grant allows and
// drops the overwrites left empty.
func maskPanelOverwrites(ow []discord.PermissionOverwrite, grant discord.Permissions) []discord.PermissionOverwrite {
	out := make([]discord.PermissionOverwrite, 0, len(ow))
	for _, o := range ow {
		switch v := o.(type) {
		case discord.RolePermissionOverwrite:
			v.Allow, v.Deny = v.Allow&grant, v.Deny&grant
			if v.Allow|v.Deny != 0 {
				out = append(out, v)
			}
		case discord.MemberPermissionOverwrite:
			v.Allow, v.Deny = v.Allow&grant, v.Deny&grant
			if v.Allow|v.Deny != 0 {
				out = append(out, v)
			}
		}
	}
	return out
}

func (k *panelKeeper) save(guildID snowflake.ID, st panelState) {
	k.mu.Lock()
	if st == (panelState{}) {
		delete(k.state, guildID)
	} else {
		k.state[guildID] = st
	}
	k.mu.Unlock()
	k.persist()
}

func (k *panelKeeper) persist() {
	if k.file == "" {
		return
	}
	k.mu.Lock()
	b, err := json.MarshalIndent(k.state, "", "  ")
	k.mu.Unlock()
	if err != nil {
		return
	}
	tmp := k.file + ".tmp"
	if err := os.MkdirAll(filepath.Dir(k.file), 0o755); err == nil {
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, k.file)
		}
		if err != nil {
			slog.Warn("panel channel: state not saved", slog.String("file", k.file), slog.Any("err", err))
		}
	}
}

// isUnknownMessage reports Discord's "Unknown Message" or "Unknown Channel".
func isUnknownMessage(err error) bool {
	var re *rest.Error
	return errors.As(err, &re) && (re.Code == 10008 || re.Code == 10003)
}
