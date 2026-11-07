package stats

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCountsVisitorsOncePerDay(t *testing.T) {
	s := Open("")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.Visit("home", "203.0.113.9")
	s.Visit("guide", "203.0.113.9")
	s.Visit("home", "203.0.113.10")
	s.Visit("home", "")
	if got := s.Totals("home").Today; got != 3 {
		t.Fatalf("home today = %d, want 3", got)
	}
	if got := s.Totals(Visitors).Today; got != 2 {
		t.Fatalf("visitors today = %d, want 2", got)
	}
	now = now.AddDate(0, 0, 1)
	s.Visit("home", "203.0.113.9")
	tot := s.Totals(Visitors)
	if tot.Today != 1 || tot.Week != 3 || tot.All != 3 {
		t.Fatalf("visitors = %+v, want today 1, week 3, all 3", tot)
	}
}

func TestTotalsPeriodsAndPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stats.json")
	s := Open(file)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	for _, days := range []int{0, 1, 6, 7, 29, 30, 500} {
		s.Now = func() time.Time { return now.AddDate(0, 0, -days) }
		s.Count("signin")
	}
	s.Now = func() time.Time { return now }
	s.Count("signin") // today again: prunes the 500 days old entry
	got := s.Totals("signin")
	want := Totals{Today: 2, Week: 4, Month: 6, All: 7}
	if got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}

	again := Open(file)
	again.Now = func() time.Time { return now }
	if again.Totals("signin") != want {
		t.Fatalf("reloaded totals = %+v, want %+v", again.Totals("signin"), want)
	}
	if again.Totals("missing") != (Totals{}) {
		t.Fatal("unknown event should count nothing")
	}
}
