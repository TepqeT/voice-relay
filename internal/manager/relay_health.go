package manager

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/dave"
	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/guild"
)

// relayHealthInterval spaces the checks of each relay bot during a session.
const relayHealthInterval = time.Minute

// silentRelayChecks is how many checks in a row an occupied channel's relay
// must be unable to encrypt before it is reported silent (two minutes).
const silentRelayChecks = 2

// relayCheck is one relay bot at one check.
type relayCheck struct {
	botID     snowflake.ID
	channelID snowflake.ID
	listeners int
	daveState string // dave.State* or "" when the bot has no voice conn
}

// startRelayHealthWatcher records, every minute of the session, how many
// people sit in each relay bot's channel and the state of the bot's outbound
// encryption, and reports a relay that stays unable to encrypt while people
// listen. The 2026-11-13 session left five channels silent for twenty minutes
// with nothing in the diagnostics to say whether anyone was in them.
func (m *Service) startRelayHealthWatcher(ctx context.Context, session *guild.Session) {
	guildID := session.GuildID
	bots := make([]snowflake.ID, 0, len(session.Speakers))
	for _, sp := range session.Speakers {
		bots = append(bots, sp.ID)
	}
	if len(bots) == 0 {
		return
	}
	go func() {
		t := time.NewTicker(relayHealthInterval)
		defer t.Stop()
		streak := map[snowflake.ID]int{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, c := range m.relayChecks(guildID, bots) {
					recordRelayCheck(guildID, c, streak)
				}
			}
		}
	}()
}

// relayChecks reads the current state of each relay bot.
func (m *Service) relayChecks(guildID snowflake.ID, bots []snowflake.ID) []relayCheck {
	out := make([]relayCheck, 0, len(bots))
	for _, botID := range bots {
		c := relayCheck{botID: botID}
		if ch, ok := m.store.GetBoundChannel(guildID, botID); ok {
			c.channelID = ch
			c.listeners = m.countListeners(guildID, ch)
		}
		if client, ok := m.poolSvc.GetClientByID(botID); ok && client.VoiceManager != nil {
			if conn := client.VoiceManager.GetConn(guildID); conn != nil {
				c.daveState = dave.StateOf(conn.DAVE())
			}
		}
		out = append(out, c)
	}
	return out
}

// recordRelayCheck writes one check in the diagnostics and reports a relay
// that stays unable to encrypt while people listen.
func recordRelayCheck(guildID snowflake.ID, c relayCheck, streak map[snowflake.ID]int) {
	diag.For(guildID).Event("relay_health", map[string]any{
		"bot_id": c.botID.String(), "channel_id": c.channelID.String(),
		"listeners": c.listeners, "dave": c.daveState,
	})
	if c.listeners == 0 || c.daveState != dave.StateNoKey {
		streak[c.botID] = 0
		return
	}
	streak[c.botID]++
	if streak[c.botID] == silentRelayChecks {
		diag.For(guildID).Event("silent_relay", map[string]any{
			"bot_id": c.botID.String(), "channel_id": c.channelID.String(), "listeners": c.listeners,
		})
		slog.Warn("voice: relay bot cannot encrypt while people listen",
			slog.String("guildID", guildID.String()), slog.String("botUserID", c.botID.String()),
			slog.Int("listeners", c.listeners))
	}
}

// countListeners is the number of non-bot members in channelID.
func (m *Service) countListeners(guildID, channelID snowflake.ID) int {
	caches := m.ownerClient.Caches
	n := 0
	for _, vs := range slices.Collect(caches.VoiceStates(guildID)) {
		if vs.ChannelID == nil || *vs.ChannelID != channelID {
			continue
		}
		if member, ok := caches.Member(guildID, vs.UserID); ok && !m.IsBot(member.User) {
			n++
		}
	}
	return n
}
