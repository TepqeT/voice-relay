package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/classsort"
	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

type fakeSortRvR struct {
	fakeRvR
	enabled bool
}

func (f fakeSortRvR) ClassSortEnabled() bool   { return f.enabled }
func (f fakeSortRvR) ClassSortWaiting() string { return "Waiting" }
func (f fakeSortRvR) CanSortPlayers(snowflake.ID, []snowflake.ID, discord.Permissions) bool {
	return true
}
func (f fakeSortRvR) SortPlayers(context.Context, snowflake.ID) (manager.SortReport, error) {
	return manager.SortReport{}, nil
}

func TestBuildPanelSortButton(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	loc := bundle.For("fr", "")
	for _, enabled := range []bool{false, true} {
		h := &CommandHandlers{bundle: bundle}
		h.SetRvR(fakeSortRvR{enabled: enabled})
		msg := h.buildPanel(1, loc)
		ids := panelButtonIDs(msg)
		has := strings.Contains(strings.Join(ids, " "), panelSortID)
		if has != enabled {
			t.Fatalf("enabled=%v: buttons %v", enabled, ids)
		}
		if !enabled {
			if strings.Contains(panelText(msg), "Répartir") {
				t.Fatalf("sort text shown while disabled: %q", panelText(msg))
			}
			continue
		}
		if len(msg.Components) != 2 {
			t.Fatalf("sort must sit on the players row: %d rows", len(msg.Components))
		}
		if !strings.Contains(panelText(msg), "Waiting") {
			t.Fatalf("content %q", panelText(msg))
		}
	}
}

func TestRenderSort(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	loc := bundle.For("fr", "")
	got := renderSort(loc, "Waiting", manager.SortReport{
		Squads: 3, Moved: 5, Failed: 1, PermissionDenied: true,
		Waiting: []manager.SortWaiting{
			{UserID: 11, Reason: classsort.ReasonNoClass},
			{UserID: 12, Reason: classsort.ReasonMultipleClasses},
			{UserID: 13, Reason: classsort.ReasonSquadLead},
			{UserID: 14, Reason: classsort.ReasonNoRoom},
		},
	}, nil)
	for _, want := range []string{
		"Joueurs déplacés : 5 (salons Listener : 3)", "refusés par Discord : 1", "Déplacer des membres",
		"<@11> : aucun rôle de classe", "<@12> : plusieurs rôles de classe",
		"<@13> : Squad Lead", "<@14> : plus de place",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recap misses %q:\n%s", want, got)
		}
	}
	if got := renderSort(loc, "Waiting", manager.SortReport{Squads: 2}, nil); !strings.Contains(got, "Plus personne en attente") {
		t.Errorf("empty recap = %q", got)
	}
	errs := map[error]string{
		manager.ErrNoActiveSession:       "Aucune session en cours",
		manager.ErrWaitingChannelMissing: "« Waiting » introuvable",
		manager.ErrNoSquadChannels:       "aucun salon Listener",
		manager.ErrClassSortDisabled:     "pas configuré",
		errors.New("boom"):               "boom",
	}
	for err, want := range errs {
		if got := renderSort(loc, "Waiting", manager.SortReport{}, err); !strings.Contains(got, want) {
			t.Errorf("renderSort(%v) = %q, want %q", err, got, want)
		}
	}
}
