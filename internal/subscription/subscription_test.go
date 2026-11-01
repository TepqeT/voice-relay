package subscription

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestActiveAndLabel(t *testing.T) {
	life := Subscription{Plan: PlanLifetime, Gift: true}
	if !life.Active(now.AddDate(50, 0, 0)) || life.Label(now) != "À vie (offert)" {
		t.Fatalf("lifetime: %v %q", life.Active(now), life.Label(now))
	}
	until := Subscription{Plan: PlanUntil, Expires: now.AddDate(0, 1, 0)}
	if !until.Active(now) || until.Label(now) != "Jusqu'au 01/12/2026" {
		t.Fatalf("until: %q", until.Label(now))
	}
	midnight := Subscription{Plan: PlanUntil, Expires: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	if got := midnight.Label(now); got != "Jusqu'au 31/12/2026" {
		t.Fatalf("exclusive end shown as %q, want the last covered day", got)
	}
	if until.Active(now.AddDate(0, 2, 0)) || until.Label(now.AddDate(0, 2, 0)) != "Expiré le 01/12/2026" {
		t.Fatal("expired subscription still active")
	}
	if (Subscription{}).Active(now) {
		t.Fatal("zero subscription active")
	}
}

func TestGrantRevokeAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "subscriptions.json")
	s := open(t, path)
	if s.Active(1) {
		t.Fatal("active before grant")
	}
	if err := s.Grant(Subscription{GuildID: 1, GuildName: "Test Server", Plan: PlanLifetime, Gift: true, GrantedBy: 9}); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(Subscription{GuildID: 2, GuildName: "aion 2 France", Plan: PlanUntil, Expires: now.AddDate(1, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	if !s.Active(1) || !s.Active(2) {
		t.Fatal("granted servers not active")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file %v, mode %v", err, info.Mode())
	}

	reloaded := open(t, path)
	list := reloaded.List()
	if len(list) != 2 || list[0].GuildName != "aion 2 France" || list[1].GuildName != "Test Server" {
		t.Fatalf("reloaded list = %+v", list)
	}
	if sub, _ := reloaded.Get(1); sub.GrantedBy != 9 || !sub.Gift || sub.GrantedAt.IsZero() {
		t.Fatalf("reloaded = %+v", sub)
	}

	if err := reloaded.Revoke(1); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Revoke(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
	if open(t, path).Active(1) {
		t.Fatal("revocation not saved")
	}
}

func TestGrantValidation(t *testing.T) {
	s := open(t, "")
	for _, bad := range []Subscription{
		{Plan: PlanLifetime},
		{GuildID: 1, Plan: "forever"},
		{GuildID: 1, Plan: PlanUntil, Expires: now.Add(-time.Hour)},
	} {
		if err := s.Grant(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Grant(%+v) = %v, want ErrInvalid", bad, err)
		}
	}
	// A lifetime grant clears a leftover end date.
	if err := s.Grant(Subscription{GuildID: 1, Plan: PlanLifetime, Expires: now}); err != nil {
		t.Fatal(err)
	}
	if sub, _ := s.Get(1); !sub.Expires.IsZero() {
		t.Fatal("lifetime kept an end date")
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
}
