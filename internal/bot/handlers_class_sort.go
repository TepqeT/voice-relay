package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/classsort"
	"github.com/TepqeT/voice-relay/internal/i18n"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// panelSortID is the panel button that sorts the waiting players.
const panelSortID = "/panel/sort"

// ClassSortService is the class sorting surface of the manager. Asserted on
// h.rvr so the RvRService test doubles stay valid.
type ClassSortService interface {
	ClassSortEnabled() bool
	ClassSortWaiting() string
	CanSortPlayers(guildID snowflake.ID, roleIDs []snowflake.ID, perms discord.Permissions) bool
	SortPlayers(ctx context.Context, guildID snowflake.ID) (manager.SortReport, error)
}

// classSortTrigger is implemented by the manager: it sorts the waiting
// channel when a member joins it during a raid.
type classSortTrigger interface {
	OnVoiceChannelJoin(guildID, channelID snowflake.ID)
}

func (h *CommandHandlers) classSort() (ClassSortService, bool) {
	s, ok := h.rvr.(ClassSortService)
	if !ok || !s.ClassSortEnabled() {
		return nil, false
	}
	return s, true
}

func (h *CommandHandlers) registerClassSort(r handler.Router) {
	r.ButtonComponent(panelSortID, h.withGuildButton(h.handlePanelSort))
}

// handlePanelSort runs the sort for a raid lead and answers with the recap.
func (h *CommandHandlers) handlePanelSort(guildID snowflake.ID, loc *i18n.Localizer, _ discord.ButtonInteractionData, e *handler.ComponentEvent) error {
	s, ok := h.classSort()
	if !ok {
		return e.CreateMessage(ephemeral(loc.T("sort.disabled")))
	}
	member := e.Member()
	if member == nil || !s.CanSortPlayers(guildID, member.RoleIDs, member.Permissions) {
		return e.CreateMessage(ephemeral(loc.T("sort.need_raid_lead")))
	}
	if err := e.DeferCreateMessage(true); err != nil {
		return err
	}
	rep, err := s.SortPlayers(e.Ctx, guildID)
	h.componentFollowUp(e, renderSort(loc, s.ClassSortWaiting(), rep, err))
	return nil
}

// renderSort is the recap shown to the raid leads.
func renderSort(loc *i18n.Localizer, waiting string, rep manager.SortReport, err error) string {
	switch {
	case errors.Is(err, manager.ErrClassSortDisabled):
		return loc.T("sort.disabled")
	case errors.Is(err, manager.ErrNoActiveSession):
		return loc.T("sort.no_raid")
	case errors.Is(err, manager.ErrWaitingChannelMissing):
		return loc.T("sort.no_waiting", "Waiting", waiting)
	case errors.Is(err, manager.ErrNoSquadChannels):
		return loc.T("sort.no_squads")
	case err != nil:
		return loc.T("sort.error", "Error", err.Error())
	}
	lines := []string{loc.T("sort.result", "Moved", rep.Moved, "Squads", rep.Squads)}
	if rep.Failed > 0 {
		lines = append(lines, loc.T("sort.failed", "Failed", rep.Failed))
	}
	if rep.PermissionDenied {
		lines = append(lines, loc.T("sort.perm_missing"))
	}
	if len(rep.Waiting) == 0 {
		lines = append(lines, loc.T("sort.waiting_none"))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, loc.T("sort.waiting_header"))
	for _, w := range rep.Waiting {
		lines = append(lines, fmt.Sprintf("- <@%s> : %s", w.UserID, loc.T(reasonKey(w.Reason))))
	}
	return strings.Join(lines, "\n")
}

func reasonKey(r classsort.Reason) string {
	switch r {
	case classsort.ReasonNoClass, classsort.ReasonMultipleClasses, classsort.ReasonNoRoom, classsort.ReasonSquadLead:
		return "sort.reason." + string(r)
	}
	return "sort.reason.no_room"
}
