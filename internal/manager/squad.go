package manager

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/diag"
	"github.com/TepqeT/voice-relay/internal/pool"
	"github.com/TepqeT/voice-relay/internal/store"
)

// Squad lead calls: one member per legion (the squad lead role) can speak to
// the raid leads from their squad channel. The raid runs in star topology: the
// raid lead channel hears every squad lead, each squad hears only the raid
// leads, so squads never hear each other.
//
// With SquadKeyPriority, a squad lead is relayed only while transmitting with
// Discord's "Push to Talk (Priority)" keybind: their normal key keeps talking
// to their squad only. The priority flag is read from the SPEAKING events the
// bots receive in each squad channel.

// Squad lead key modes.
const (
	SquadKeyPriority = "priority" // relay only with the priority push-to-talk key
	SquadKeyAlways   = "always"   // relay whenever they speak (use push-to-talk)
)

type speakingKey struct {
	guildID snowflake.ID
	userID  snowflake.ID
}

type squadState struct {
	roles    sync.Map // guildID -> snowflake.ID, resolved squad lead role of the current raid
	speaking sync.Map // speakingKey -> voice.SpeakingFlags, last flags seen
}

// resolveSquadRole finds the squad lead role of guildID: a role ID, or a role
// name compared case-insensitively. Returns 0 when not configured or absent.
// callerRole returns the raid lead role: the capture role bound with /setup,
// else RAID_LEAD_ROLE found by name or ID in the guild.
func (m *Service) callerRole(guildID snowflake.ID) (snowflake.ID, bool) {
	return m.boundOrConfiguredRole(guildID, store.RoleTypeCaller)
}

// managerRole returns the role allowed to drive raids: the one bound with
// /setup, else the raid lead role from RAID_LEAD_ROLE.
func (m *Service) managerRole(guildID snowflake.ID) (snowflake.ID, bool) {
	return m.boundOrConfiguredRole(guildID, store.RoleTypeManager)
}

func (m *Service) boundOrConfiguredRole(guildID snowflake.ID, kind store.RoleType) (snowflake.ID, bool) {
	if id, ok := m.store.GetBoundRole(guildID, kind); ok {
		return id, true
	}
	if m.rvr.opts.RaidLeadRole == "" || m.ownerClient == nil {
		return 0, false
	}
	if id := m.resolveRole(guildID, m.rvr.opts.RaidLeadRole); id != 0 {
		return id, true
	}
	return 0, false
}

func (m *Service) resolveSquadRole(guildID snowflake.ID) snowflake.ID {
	return m.resolveRole(guildID, m.rvr.opts.SquadLeadRole)
}

// SquadLeadsEnabled reports whether guildID has a squad lead role, which makes
// the panel start raids in star topology.
func (m *Service) SquadLeadsEnabled(guildID snowflake.ID) bool {
	return m.resolveSquadRole(guildID) != 0
}

// beginSquad resolves and remembers the squad lead role for a starting raid.
func (m *Service) beginSquad(guildID snowflake.ID) snowflake.ID {
	id := m.resolveSquadRole(guildID)
	if id == 0 {
		m.squad.roles.Delete(guildID)
		if m.rvr.opts.SquadLeadRole != "" {
			slog.Warn("squad lead role not found, squad calls disabled",
				slog.String("guildID", guildID.String()), slog.String("role", m.rvr.opts.SquadLeadRole))
		}
		return 0
	}
	m.squad.roles.Store(guildID, id)
	slog.Info("squad lead calls active",
		slog.String("guildID", guildID.String()),
		slog.String("roleID", id.String()),
		slog.String("key", m.squadKey()),
	)
	return id
}

func (m *Service) squadRole(guildID snowflake.ID) snowflake.ID {
	if v, ok := m.squad.roles.Load(guildID); ok {
		return v.(snowflake.ID)
	}
	return 0
}

func (m *Service) squadKey() string {
	if m.rvr.opts.SquadLeadKey == SquadKeyAlways {
		return SquadKeyAlways
	}
	return SquadKeyPriority
}

// recordSpeaking stores the speaking flags a user announced. Called from the
// voice gateway of every bot connection.
func (m *Service) recordSpeaking(guildID, userID snowflake.ID, flags voice.SpeakingFlags) {
	if userID == 0 {
		return
	}
	k := speakingKey{guildID: guildID, userID: userID}
	prev, loaded := m.squad.speaking.Swap(k, flags)
	if loaded && prev.(voice.SpeakingFlags) == flags {
		return
	}
	if role := m.squadRole(guildID); role != 0 {
		if m.hasRole(guildID, userID, role) {
			slog.Info("squad lead speaking flags",
				slog.String("guildID", guildID.String()),
				slog.String("userID", userID.String()),
				slog.Int("flags", int(flags)),
				slog.Bool("priority", flags&voice.SpeakingFlagPriority != 0))
		}
		diag.For(guildID).Event("speaking_flags", map[string]any{
			"user_id":  userID.String(),
			"flags":    int(flags),
			"priority": flags&voice.SpeakingFlagPriority != 0,
		})
	}
}

// hasRole reports whether a cached member of guildID holds roleID.
func (m *Service) hasRole(guildID, userID, roleID snowflake.ID) bool {
	if roleID == 0 {
		return false
	}
	member, ok := m.ownerClient.Caches.Member(guildID, userID)
	return ok && slices.Contains(member.RoleIDs, roleID)
}

// squadAllow wraps the caller filter of the squad channels. A raid lead is
// always relayed; a squad lead (squad role without the caller role) only while
// holding the priority push-to-talk key, unless the key mode is "always".
func (m *Service) squadAllow(guildID snowflake.ID, filter *AllowFilter) func(snowflake.ID) bool {
	squadRole := m.squadRole(guildID)
	if squadRole == 0 || m.squadKey() == SquadKeyAlways {
		return filter.Check
	}
	callerRole := filter.RoleID()
	isRaidLead := func(userID snowflake.ID) bool {
		return callerRole == 0 || m.hasRole(guildID, userID, callerRole)
	}
	flagsOf := func(userID snowflake.ID) (voice.SpeakingFlags, bool) {
		v, ok := m.squad.speaking.Load(speakingKey{guildID: guildID, userID: userID})
		if !ok {
			return 0, false
		}
		return v.(voice.SpeakingFlags), true
	}
	gate := squadGate(filter.Check, isRaidLead, flagsOf)
	var lastGated sync.Map // userID -> unix nanoseconds of the last report
	return func(userID snowflake.ID) bool {
		if gate(userID) {
			return true
		}
		// Allowed by the role filter but not by the gate: a squad lead whose
		// voice reaches the bot without the priority flag. Say so, at most
		// every 5 s per member, so a silent squad lead call can be explained.
		if filter.Check(userID) && !isRaidLead(userID) {
			now := time.Now().UnixNano()
			if v, ok := lastGated.Load(userID); !ok || now-v.(int64) >= int64(5*time.Second) {
				lastGated.Store(userID, now)
				f, seen := flagsOf(userID)
				slog.Info("squad lead heard without the priority key: not relayed to the raid leads",
					slog.String("guildID", guildID.String()),
					slog.String("userID", userID.String()),
					slog.Int("flags", int(f)),
					slog.Bool("flagsSeen", seen))
				diag.For(guildID).Event("squad_lead_gated", map[string]any{
					"user_id": userID.String(), "flags": int(f), "flags_seen": seen})
			}
		}
		return false
	}
}

// squadGate lets through: nobody the caller filter rejects; every raid lead;
// a squad lead only while their last speaking flags carry the priority bit.
func squadGate(allowed, isRaidLead func(snowflake.ID) bool, flags func(snowflake.ID) (voice.SpeakingFlags, bool)) func(snowflake.ID) bool {
	return func(userID snowflake.ID) bool {
		if !allowed(userID) {
			return false
		}
		if isRaidLead(userID) {
			return true // a raid lead talking from a squad channel
		}
		f, ok := flags(userID)
		return ok && f&voice.SpeakingFlagPriority != 0
	}
}

// logPrioritySpeakerCheck says, for every bot bound in guildID, whether it
// holds Priority Speaker in its channel. Without it Discord ignores the
// priority flag the bots send and squads are not lowered during calls.
func (m *Service) logPrioritySpeakerCheck(guildID snowflake.ID) {
	if !pool.PrioritySpeaking() || m.ownerClient == nil {
		return
	}
	bots := append([]snowflake.ID{m.ownerBotID}, m.poolSvc.GetIDs()...)
	var ok, missing, unknown []string
	for _, botID := range bots {
		chID, bound := m.store.GetBoundChannel(guildID, botID)
		if !bound {
			continue
		}
		channel, okCh := m.ownerClient.Caches.Channel(chID)
		member, okMb := m.ownerClient.Caches.Member(guildID, botID)
		if !okCh || !okMb {
			unknown = append(unknown, botID.String())
			continue
		}
		if m.ownerClient.Caches.MemberPermissionsInChannel(channel, member).Has(discord.PermissionPrioritySpeaker) {
			ok = append(ok, botID.String())
		} else {
			missing = append(missing, botID.String())
		}
	}
	level := slog.LevelInfo
	if len(missing) > 0 {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "priority speaker check",
		slog.String("guildID", guildID.String()),
		slog.Int("ok", len(ok)),
		slog.Any("missing", missing),
		slog.Any("unknown", unknown))
	diag.For(guildID).Event("priority_speaker_check", map[string]any{
		"ok": len(ok), "missing": missing, "unknown": unknown})
}

// SquadLeadsToSquads reports whether squad leads are also relayed to the
// other squads. The raid then runs mix-minus (RaidModeGuildCaller): every
// channel hears every other channel's raid leads and squad leads, never
// itself; squad members without the role are never relayed.
func (m *Service) SquadLeadsToSquads() bool { return m.rvr.opts.SquadLeadsToSquads }
