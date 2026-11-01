package manager

import (
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// ChannelCount is a raid voice channel and the people (bots excluded) in it.
type ChannelCount struct {
	ChannelID snowflake.ID
	Members   int
}

// PanelStatus is what the control panel shows about guildID's raid.
type PanelStatus struct {
	Running  bool
	Since    time.Time      // raid channels creation, zero when unknown
	RaidLead ChannelCount   // zero ChannelID when the raid has no channels yet
	Squads   []ChannelCount // in squad order
	Waiting  ChannelCount   // class sorting waiting channel, zero when off
}

// PanelStatus describes the running raid of guildID for the control panel.
func (m *Service) PanelStatus(guildID snowflake.ID) PanelStatus {
	st := PanelStatus{Running: m.HasActiveSession(guildID)}
	if !st.Running {
		return st
	}
	rc, ok := m.RaidChannelsOf(guildID)
	if !ok {
		return st
	}
	st.Since = rc.Created
	count := func(id snowflake.ID) ChannelCount {
		if id == 0 {
			return ChannelCount{}
		}
		return ChannelCount{ChannelID: id, Members: m.humansIn(guildID, id)}
	}
	st.RaidLead = count(rc.RaidLead)
	for _, id := range rc.Squads {
		st.Squads = append(st.Squads, count(id))
	}
	st.Waiting = count(rc.Waiting)
	return st
}

// humansIn counts the cached non-bot members in voice channelID.
func (m *Service) humansIn(guildID, channelID snowflake.ID) int {
	caches := m.ownerClient.Caches
	n := 0
	for vs := range caches.VoiceStates(guildID) {
		if vs.ChannelID == nil || *vs.ChannelID != channelID {
			continue
		}
		if mem, ok := caches.Member(guildID, vs.UserID); ok && !m.IsBot(mem.User) {
			n++
		}
	}
	return n
}

// AccessRoleID is the role that opens the raid channels, 0 when access
// management is off or the role is missing.
func (m *Service) AccessRoleID(guildID snowflake.ID) snowflake.ID {
	if !m.AccessEnabled() {
		return 0
	}
	return m.resolveRole(guildID, m.channels.opts.AccessRole)
}

// BoardRoleID is the role that grants access, 0 when off or missing.
func (m *Service) BoardRoleID(guildID snowflake.ID) snowflake.ID {
	if !m.AccessEnabled() {
		return 0
	}
	return m.resolveRole(guildID, m.channels.opts.BoardRole)
}
