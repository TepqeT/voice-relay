package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/journal"
	"github.com/TepqeT/voice-relay/internal/manager"
)

// rolesManager answers CreateMissingRoles with a fixed report.
type rolesManager struct {
	fakeSiteManager
	rep   manager.RoleSetupReport
	err   error
	calls int
}

func (r *rolesManager) CreateMissingRoles(context.Context, snowflake.ID) (manager.RoleSetupReport, error) {
	r.calls++
	return r.rep, r.err
}

// The roles are created as soon as the main bot joins, one step fewer for
// the administrator; the outcome goes in the install journal.
func TestRolesCreatedOnJoin(t *testing.T) {
	const g snowflake.ID = 4242
	ok := &rolesManager{rep: manager.RoleSetupReport{Created: []string{"Speaker", "Squad Lead"}}}
	createRolesOnJoin(ok, g, "Test")
	if ok.calls != 1 || !hasKind(journal.ForGuild(g, 10), "roles_created_on_join") {
		t.Fatalf("calls=%d journal=%v", ok.calls, journal.ForGuild(g, 10))
	}

	const g2 snowflake.ID = 4343
	refused := &rolesManager{rep: manager.RoleSetupReport{PermissionDenied: true}, err: errors.New("50013")}
	createRolesOnJoin(refused, g2, "Test")
	if !hasKind(journal.ForGuild(g2, 10), "roles_on_join_failed") {
		t.Fatalf("refusal not journaled: %v", journal.ForGuild(g2, 10))
	}

	const g3 snowflake.ID = 4444
	createRolesOnJoin(&rolesManager{rep: manager.RoleSetupReport{Existing: []string{"Speaker"}}}, g3, "Test")
	if len(journal.ForGuild(g3, 10)) != 0 {
		t.Fatal("nothing to create: nothing to journal")
	}
}

func hasKind(evs []journal.Event, kind string) bool {
	for _, e := range evs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
