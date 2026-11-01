package manager

import (
	"log/slog"
	"slices"
	"sync"

	"github.com/disgoorg/snowflake/v2"
)

// Squad lead return: each relay bot captures the squad lead (or a speaker)
// sitting in its listener channel and sends that voice to the speaker
// channel. The speakers can cut that return per listener channel, or all at
// once, from the panel. Cuts last for the running session: every start
// begins with every return on.

type feedbackKey struct{ guildID, botID snowflake.ID }

type listenerFeedback struct {
	muted sync.Map // feedbackKey -> struct{}
}

// feedbackAllow wraps allow for the relay bot botID: nothing is captured
// while its listener channel's return is cut.
func (m *Service) feedbackAllow(guildID, botID snowflake.ID, allow func(snowflake.ID) bool) func(snowflake.ID) bool {
	key := feedbackKey{guildID, botID}
	return func(userID snowflake.ID) bool {
		if _, cut := m.feedback.muted.Load(key); cut {
			return false
		}
		return allow(userID)
	}
}

// resetFeedback turns every return of guildID back on (new session).
func (m *Service) resetFeedback(guildID snowflake.ID) {
	m.feedback.muted.Range(func(k, _ any) bool {
		if k.(feedbackKey).guildID == guildID {
			m.feedback.muted.Delete(k)
		}
		return true
	})
}

// ListenerFeed is one listener channel of the running session and whether
// its squad lead return is cut.
type ListenerFeed struct {
	ChannelID snowflake.ID
	Name      string // channel name, "" when not cached
	BotID     snowflake.ID
	Muted     bool
}

// ListenerFeeds lists the listener channels of guildID's running session, in
// channel order. Empty when no session runs.
func (m *Service) ListenerFeeds(guildID snowflake.ID) []ListenerFeed {
	if !m.HasActiveSession(guildID) {
		return nil
	}
	byChannel := map[snowflake.ID]snowflake.ID{}
	for _, bot := range m.poolSvc.GetIDs() {
		if ch, ok := m.store.GetBoundChannel(guildID, bot); ok {
			byChannel[ch] = bot
		}
	}
	var order []snowflake.ID
	if rc, ok := m.RaidChannelsOf(guildID); ok {
		order = rc.Squads
	} else {
		for ch := range byChannel {
			order = append(order, ch)
		}
		slices.Sort(order)
	}
	var out []ListenerFeed
	for _, ch := range order {
		bot, ok := byChannel[ch]
		if !ok {
			continue
		}
		_, cut := m.feedback.muted.Load(feedbackKey{guildID, bot})
		name := ""
		if c, ok := m.ownerClient.Caches.Channel(ch); ok {
			name = c.Name()
		}
		out = append(out, ListenerFeed{ChannelID: ch, Name: name, BotID: bot, Muted: cut})
	}
	return out
}

// SetListenerMuted cuts (muted) or restores the squad lead return of one
// listener channel; channelID 0 applies to every listener channel. Returns
// false when the channel is not part of the running session.
func (m *Service) SetListenerMuted(guildID, channelID snowflake.ID, muted bool) bool {
	done := false
	for _, f := range m.ListenerFeeds(guildID) {
		if channelID != 0 && f.ChannelID != channelID {
			continue
		}
		key := feedbackKey{guildID, f.BotID}
		if muted {
			m.feedback.muted.Store(key, struct{}{})
		} else {
			m.feedback.muted.Delete(key)
		}
		done = true
	}
	if done {
		slog.Info("squad lead return changed", slog.String("guildID", guildID.String()),
			slog.String("channelID", channelID.String()), slog.Bool("muted", muted))
	}
	return done
}

// CanControlFeedback reports whether a member may cut the returns: the
// speaker role, or server managers.
func (m *Service) CanControlFeedback(guildID snowflake.ID, roleIDs []snowflake.ID, isManager bool) bool {
	if isManager {
		return true
	}
	role, ok := m.callerRole(guildID)
	return ok && role != 0 && slices.Contains(roleIDs, role)
}

// speakerChannelAllow narrows the capture of the speaker channel to the
// speaker role: anyone else in that channel is not relayed.
func (m *Service) speakerChannelAllow(guildID snowflake.ID, allow func(snowflake.ID) bool) func(snowflake.ID) bool {
	role, ok := m.callerRole(guildID)
	if !ok || role == 0 {
		return allow
	}
	return func(userID snowflake.ID) bool {
		return allow(userID) && m.hasRole(guildID, userID, role)
	}
}

// SquadLeadKeyAlways reports whether squad leads are relayed whenever they
// talk (true) or only with Discord's priority push-to-talk key.
func (m *Service) SquadLeadKeyAlways() bool { return m.squadKey() == SquadKeyAlways }
