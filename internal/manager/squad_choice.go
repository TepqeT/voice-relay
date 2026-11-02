package manager

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/statefile"
)

// Squad choice: the raid leads pick on the panel how many squads the next
// raid creates, up to the relay bots available. Saved per guild in
// squad-count.json; no choice means as many as the configuration allows.

type squadChoices struct {
	mu     sync.Mutex
	loaded bool
	n      map[snowflake.ID]int
}

func (m *Service) squadChoiceFile() string {
	if m.channels.opts.StateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.channels.opts.StateFile), "squad-count.json")
}

func (m *Service) squadChoicesLocked() map[snowflake.ID]int {
	c := &m.squadChoice
	if !c.loaded {
		c.loaded = true
		c.n = map[snowflake.ID]int{}
		if f := m.squadChoiceFile(); f != "" {
			if b, err := statefile.ReadFile(f); err == nil {
				_ = json.Unmarshal(b, &c.n)
			}
		}
	}
	return c.n
}

// SquadCapacity is the most squads guildID can have: its enabled relay bots,
// within the configured maximum.
func (m *Service) SquadCapacity(guildID snowflake.ID) int {
	m.mu.RLock()
	n := 0
	if st := m.statuses[guildID]; st != nil {
		for _, sp := range st.Speakers {
			if sp.Enabled {
				n++
			}
		}
	}
	m.mu.RUnlock()
	return min(n, max(m.channels.opts.SquadCount, 1))
}

// SquadTarget is how many squads the next raid of guildID creates: the
// panel's choice when there is one, else the capacity.
func (m *Service) SquadTarget(guildID snowflake.ID) int {
	capacity := m.SquadCapacity(guildID)
	m.squadChoice.mu.Lock()
	n, ok := m.squadChoicesLocked()[guildID]
	m.squadChoice.mu.Unlock()
	if !ok || n < 1 || n > capacity {
		return capacity
	}
	return n
}

// SetSquadTarget saves how many squads the next raids of guildID create.
// Values above the capacity are kept (more relay bots may come back) and
// capped when used.
func (m *Service) SetSquadTarget(guildID snowflake.ID, n int) {
	m.squadChoice.mu.Lock()
	defer m.squadChoice.mu.Unlock()
	choices := m.squadChoicesLocked()
	if n < 1 {
		delete(choices, guildID)
	} else {
		choices[guildID] = n
	}
	if f := m.squadChoiceFile(); f != "" {
		b, err := json.MarshalIndent(choices, "", "  ")
		if err == nil {
			err = os.WriteFile(f, b, 0o600)
		}
		if err != nil {
			slog.Warn("squad choice: not saved", slog.String("file", f), slog.Any("err", err))
		}
	}
	slog.Info("squad count chosen", slog.String("guildID", guildID.String()), slog.Int("squads", n))
}
