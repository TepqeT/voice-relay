package web

import "github.com/TepqeT/voice-relay/internal/stats"

// What the site counts, shown on the administration page. Numbers only,
// never who (see package stats).
const (
	statHome         = "home"          // home page shown
	statGuide        = "guide"         // guide page shown
	statLogin        = "login"         // "Sign in" clicked
	statSignIn       = "signin"        // Discord sign-in completed
	statSignInFailed = "signin_failed" // refused by the user or by Discord
	statServers      = "servers"       // server list shown
	statSetup        = "setup"         // roles created from a server page
	statUninstall    = "uninstall"     // relay removed from a server
	statDonation     = "donation"      // donation received
)

// statRow is one line of the statistics table.
type statRow struct {
	Key string // message key suffix (stat.<Key>)
	stats.Totals
}

// statRows lists the counters in the order shown.
func (s *Server) statRows() []statRow {
	keys := []string{stats.Visitors, statHome, statGuide, statLogin, statSignIn, statSignInFailed, statServers, statSetup, statUninstall, statDonation}
	rows := make([]statRow, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, statRow{Key: k, Totals: s.stats.Totals(k)})
	}
	return rows
}
