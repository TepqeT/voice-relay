package subscription

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

func TestActiveAndLastDay(t *testing.T) {
	if !(Subscription{Plan: PlanLifetime}).Active(now.AddDate(50, 0, 0)) {
		t.Fatal("lifetime expired")
	}
	until := Subscription{Plan: PlanUntil, Expires: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !until.Active(now) || until.Active(until.Expires) {
		t.Fatal("until bounds")
	}
	if until.LastDay().Format("02/01/2006") != "31/12/2026" {
		t.Fatalf("last day = %s", until.LastDay())
	}
	if (Subscription{}).Active(now) {
		t.Fatal("zero subscription active")
	}
}

// One account, several servers; a second account cannot take a covered server.
func TestAccountCoversSeveralServers(t *testing.T) {
	s := open(t, "")
	if err := s.Activate(7, 100, "Test Server"); !errors.Is(err, ErrNoSubscription) {
		t.Fatalf("activate without subscription = %v", err)
	}
	if err := s.Grant(Subscription{UserID: 7, UserName: "Alice", Plan: PlanLifetime, Gift: true}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []struct {
		id   uint64
		name string
	}{{100, "Test Server"}, {200, "Aion 2 France"}, {300, "Légion"}} {
		if err := s.Activate(7, snowflakeID(g.id), g.name); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Guilds(7); len(got) != 3 || got[0].GuildName != "Aion 2 France" {
		t.Fatalf("guilds = %+v", got)
	}
	if !s.Active(100) || !s.Active(300) || s.Active(999) {
		t.Fatal("coverage")
	}
	if err := s.Activate(7, 100, "Test Server"); err != nil {
		t.Fatalf("re-activation must be a no-op: %v", err)
	}
	_ = s.Grant(Subscription{UserID: 8, Plan: PlanLifetime})
	if err := s.Activate(8, 100, "Test Server"); !errors.Is(err, ErrTaken) {
		t.Fatalf("taking a covered server = %v", err)
	}
	if err := s.Deactivate(8, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account deactivating = %v", err)
	}
	if err := s.Deactivate(7, 100); err != nil || s.Coverage(100).State != StateNone {
		t.Fatalf("deactivate: %v %v", err, s.Coverage(100))
	}
}

func TestServerLimit(t *testing.T) {
	s := open(t, "")
	_ = s.Grant(Subscription{UserID: 7, Plan: PlanLifetime, MaxGuilds: 2})
	_ = s.Activate(7, 1, "a")
	_ = s.Activate(7, 2, "b")
	if err := s.Activate(7, 3, "c"); !errors.Is(err, ErrLimit) {
		t.Fatalf("third server = %v", err)
	}
}

// Expiry and revocation leave the link, so the bot can say the subscription
// ended rather than that there never was one.
func TestExpiredAndRevoked(t *testing.T) {
	s := open(t, "")
	_ = s.Grant(Subscription{UserID: 7, Plan: PlanUntil, Expires: now.Add(24 * time.Hour)})
	_ = s.Activate(7, 100, "Test Server")
	if s.Coverage(100).State != StateActive {
		t.Fatal("should be active")
	}
	s.now = func() time.Time { return now.Add(48 * time.Hour) }
	c := s.Coverage(100)
	if c.State != StateExpired || c.Sub.UserID != 7 || c.Link.GuildName != "Test Server" {
		t.Fatalf("expired coverage = %+v", c)
	}
	// Another account may take over an expired server.
	_ = s.Grant(Subscription{UserID: 8, Plan: PlanLifetime})
	if err := s.Activate(8, 100, "Test Server"); err != nil {
		t.Fatalf("takeover of an expired server = %v", err)
	}
	if err := s.Revoke(8); err != nil {
		t.Fatal(err)
	}
	if c := s.Coverage(100); c.State != StateExpired || c.Sub.UserID != 0 {
		t.Fatalf("revoked coverage = %+v", c)
	}
	if err := s.Revoke(8); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "subscriptions.json")
	s := open(t, path)
	_ = s.Grant(Subscription{UserID: 7, UserName: "Alice", Plan: PlanLifetime, Gift: true, GrantedBy: 9})
	_ = s.Activate(7, 100, "Test Server")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v", err)
	}
	r := open(t, path)
	if sub, _ := r.Get(7); sub.GrantedBy != 9 || !sub.Gift || sub.GrantedAt.IsZero() {
		t.Fatalf("reloaded = %+v", sub)
	}
	if !r.Active(100) {
		t.Fatal("link not reloaded")
	}
}

func TestGrantValidation(t *testing.T) {
	s := open(t, "")
	for _, bad := range []Subscription{
		{Plan: PlanLifetime},
		{UserID: 1, Plan: "forever"},
		{UserID: 1, Plan: PlanUntil, Expires: now.Add(-time.Hour)},
		{UserID: 1, Plan: PlanLifetime, MaxGuilds: -1},
	} {
		if err := s.Grant(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Grant(%+v) = %v", bad, err)
		}
	}
	if err := s.Grant(Subscription{UserID: 1, Plan: PlanLifetime, Expires: now}); err != nil {
		t.Fatal(err)
	}
	if sub, _ := s.Get(1); !sub.Expires.IsZero() {
		t.Fatal("lifetime kept an end date")
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

func snowflakeID(v uint64) snowflake.ID { return snowflake.ID(v) }
