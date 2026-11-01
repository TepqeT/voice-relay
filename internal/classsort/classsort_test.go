package classsort

import (
	"fmt"
	"reflect"
	"testing"
)

func players(class string, n int, prefix string) []Player {
	out := make([]Player, n)
	for i := range out {
		out[i] = Player{ID: fmt.Sprintf("%s%d", prefix, i), Classes: []string{class}}
	}
	return out
}

func emptySquads(n int) [][]Player { return make([][]Player, n) }

func countBySquad(res Result, n int) []int {
	c := make([]int, n)
	for _, m := range res.Moves {
		c[m.Squad]++
	}
	return c
}

func TestPlanNoPlayers(t *testing.T) {
	res := Plan(nil, emptySquads(3), Rule{})
	if len(res.Moves) != 0 || len(res.Waiting) != 0 {
		t.Fatalf("got %+v, want nothing", res)
	}
}

func TestPlanNoSquads(t *testing.T) {
	res := Plan(players("clerc", 2, "c"), nil, Rule{})
	if len(res.Moves) != 0 || len(res.Waiting) != 2 || res.Waiting[0].Reason != ReasonNoRoom {
		t.Fatalf("got %+v, want 2 waiting no_room", res)
	}
}

func TestPlanBalancedSpreadsEachClass(t *testing.T) {
	var w []Player
	w = append(w, players("Clerc", 4, "c")...)
	w = append(w, players("Templier", 2, "t")...)
	w = append(w, players("Sorcier", 3, "s")...)
	res := Plan(w, emptySquads(2), Rule{})
	if len(res.Waiting) != 0 {
		t.Fatalf("waiting = %+v", res.Waiting)
	}
	perSquad := map[string][2]int{}
	for _, m := range res.Moves {
		class := map[byte]string{'c': "clerc", 't': "templier", 's': "sorcier"}[m.PlayerID[0]]
		v := perSquad[class]
		v[m.Squad]++
		perSquad[class] = v
	}
	for class, v := range perSquad {
		if d := v[0] - v[1]; d > 1 || d < -1 {
			t.Errorf("%s unbalanced: %v", class, v)
		}
	}
	if c := countBySquad(res, 2); c[0]-c[1] > 1 || c[1]-c[0] > 1 {
		t.Errorf("squad sizes unbalanced: %v", c)
	}
}

func TestPlanSingleClass(t *testing.T) {
	res := Plan(players("Gladiateur", 7, "g"), emptySquads(3), Rule{})
	if got, want := countBySquad(res, 3), []int{3, 2, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPlanCountsPlayersAlreadyInSquads(t *testing.T) {
	squads := [][]Player{players("Clerc", 2, "old"), nil}
	res := Plan(players("Clerc", 2, "c"), squads, Rule{})
	if got, want := countBySquad(res, 2), []int{0, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPlanMorePlayersThanPlaces(t *testing.T) {
	res := Plan(players("Clerc", 5, "c"), emptySquads(2), Rule{Max: 2})
	if len(res.Moves) != 4 {
		t.Fatalf("moves = %d, want 4", len(res.Moves))
	}
	if len(res.Waiting) != 1 || res.Waiting[0] != (Waiting{"c4", ReasonNoRoom}) {
		t.Fatalf("waiting = %+v, want c4 no_room", res.Waiting)
	}
}

func TestPlanImpossibleQuotas(t *testing.T) {
	rule, err := ParseRule("Clerc=0; Templier=1")
	if err != nil {
		t.Fatal(err)
	}
	w := append(players("Clerc", 2, "c"), players("Sorcier", 1, "s")...)
	w = append(w, players("Templier", 3, "t")...)
	res := Plan(w, emptySquads(2), rule)
	if len(res.Moves) != 2 {
		t.Fatalf("moves = %+v, want the 2 templiers that fit", res.Moves)
	}
	for _, m := range res.Moves {
		if m.PlayerID[0] != 't' {
			t.Errorf("unexpected move %+v", m)
		}
	}
	if len(res.Waiting) != 4 {
		t.Fatalf("waiting = %+v, want 4", res.Waiting)
	}
	for _, wt := range res.Waiting {
		if wt.Reason != ReasonNoRoom {
			t.Errorf("reason = %s, want no_room", wt.Reason)
		}
	}
}

func TestPlanQuotasPerSquad(t *testing.T) {
	rule, err := ParseRule("clerc=2/1")
	if err != nil {
		t.Fatal(err)
	}
	res := Plan(players("CLERC", 6, "c"), emptySquads(4), rule)
	if got, want := countBySquad(res, 4), []int{2, 1, 1, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(res.Waiting) != 1 {
		t.Fatalf("waiting = %+v, want 1", res.Waiting)
	}
}

func TestPlanDefaultQuota(t *testing.T) {
	rule, err := ParseRule("Clerc=1; *=2")
	if err != nil {
		t.Fatal(err)
	}
	res := Plan(players("Sorcier", 3, "s"), emptySquads(1), rule)
	if len(res.Moves) != 2 || len(res.Waiting) != 1 {
		t.Fatalf("got %+v, want 2 moved 1 waiting", res)
	}
}

func TestPlanNoOrMultipleClassesAndSquadLeads(t *testing.T) {
	w := []Player{
		{ID: "none"},
		{ID: "two", Classes: []string{"Clerc", "Sorcier"}},
		{ID: "lead", Classes: []string{"Clerc"}, SquadLead: true},
		{ID: "ok", Classes: []string{"Clerc"}},
	}
	res := Plan(w, emptySquads(2), Rule{})
	want := []Waiting{{"none", ReasonNoClass}, {"two", ReasonMultipleClasses}, {"lead", ReasonSquadLead}}
	if !reflect.DeepEqual(res.Waiting, want) {
		t.Fatalf("waiting = %+v, want %+v", res.Waiting, want)
	}
	if len(res.Moves) != 1 || res.Moves[0].PlayerID != "ok" {
		t.Fatalf("moves = %+v", res.Moves)
	}
}

func TestParseRule(t *testing.T) {
	for _, s := range []string{"", "balanced", " Balanced ", "équilibré"} {
		r, err := ParseRule(s)
		if err != nil || !r.Balanced() || r.Max != 0 {
			t.Errorf("ParseRule(%q) = %+v, %v; want balanced", s, r, err)
		}
	}
	r, err := ParseRule("balanced; max=6")
	if err != nil || !r.Balanced() || r.Max != 6 {
		t.Errorf("balanced;max=6 = %+v, %v", r, err)
	}
	r, err = ParseRule("Templier=1, Clerc=2/1, *=0")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Quotas, map[string][]int{"templier": {1}, "clerc": {2, 1}}) || !reflect.DeepEqual(r.Default, []int{0}) {
		t.Errorf("got %+v", r)
	}
	for _, bad := range []string{"Clerc", "Clerc=x", "Clerc=-1", "=2", "max=-3", "Clerc=1//2"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) accepted", bad)
		}
	}
}
