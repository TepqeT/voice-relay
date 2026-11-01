package manager

import (
	"path/filepath"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/guild"
)

func TestSquadChoice(t *testing.T) {
	dir := t.TempDir()
	newSvc := func() *Service {
		m := &Service{statuses: map[snowflake.ID]*guild.Status{}}
		m.channels.opts = ChannelOptions{SquadCount: 45, StateFile: filepath.Join(dir, "rvr-channels.json")}
		st := guild.NewStatus(10, 1)
		for i := 1; i <= 11; i++ {
			st.Speakers[snowflake.ID(100+i)] = &guild.Speaker{ID: snowflake.ID(100 + i), Enabled: i != 11}
		}
		m.statuses[10] = st
		return m
	}
	m := newSvc()
	if c := m.SquadCapacity(10); c != 10 {
		t.Fatalf("capacity = %d, want 10 enabled relay bots", c)
	}
	if n := m.SquadTarget(10); n != 10 || m.squadLimit(10) != 45 {
		t.Fatalf("no choice: target %d limit %d", n, m.squadLimit(10))
	}
	m.SetSquadTarget(10, 4)
	if m.SquadTarget(10) != 4 || m.squadLimit(10) != 4 {
		t.Fatal("choice of 4 squads ignored")
	}
	if m2 := newSvc(); m2.SquadTarget(10) != 4 {
		t.Fatal("the choice must survive a restart")
	}
	m.channels.opts.SquadCount = 3
	if m.squadLimit(10) != 3 || m.SquadCapacity(10) != 3 {
		t.Fatal("the configured maximum still caps the choice")
	}
}
