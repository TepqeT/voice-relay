package bot

import "testing"

func TestAccessCommandNamesFitDiscordLimit(t *testing.T) {
	for _, f := range []string{"Élyséens", "Asmodiens", "Une faction au nom beaucoup trop long"} {
		g, r := accessCommandNames("RvR", f)
		if n := len([]rune(g)); n == 0 || n > 32 {
			t.Errorf("%q: grant name %q has %d runes", f, g, n)
		}
		if n := len([]rune(r)); n == 0 || n > 32 {
			t.Errorf("%q: revoke name %q has %d runes", f, r, n)
		}
		if g == r {
			t.Errorf("%q: grant and revoke share the name %q", f, g)
		}
	}
	if g, _ := accessCommandNames("RvR", "Élyséens"); g != "RvR Élyséens : autoriser" {
		t.Errorf("grant = %q", g)
	}
}

func TestAccessCommandsOffWithoutService(t *testing.T) {
	h := &CommandHandlers{}
	if cmds := h.AccessCommands(); len(cmds) != 0 {
		t.Fatalf("access commands without an access service: %d", len(cmds))
	}
}

func TestSquadLeadCommandNamesFitAndDiffer(t *testing.T) {
	for _, f := range []string{"Élyséens", "Asmodiens", "Une faction au nom beaucoup trop long"} {
		n, d := squadLeadCommandNames("RvR", f)
		g, r := accessCommandNames("RvR", f)
		for _, s := range []string{n, d} {
			if k := len([]rune(s)); k == 0 || k > 32 {
				t.Errorf("%q: %q has %d runes", f, s, k)
			}
		}
		names := map[string]bool{n: true, d: true, g: true, r: true}
		if len(names) != 4 {
			t.Errorf("%q: command names collide: %q %q %q %q", f, n, d, g, r)
		}
	}
}

// With the default label the command names are exactly those of v27.
func TestDefaultEventLabelKeepsCommandNames(t *testing.T) {
	h := &CommandHandlers{}
	label := h.eventLabel()
	cases := []struct {
		faction                  string
		grant, revoke, name, dis string
	}{
		{"Élyséens", "RvR Élyséens : autoriser", "RvR Élyséens : retirer", "RvR Élyséens : nommer chef", "RvR Élyséens : retirer chef"},
		{"Asmodiens", "RvR Asmodiens : autoriser", "RvR Asmodiens : retirer", "RvR Asmodiens : nommer chef", "RvR Asmodiens : retirer chef"},
		{"Une faction au nom beaucoup trop long", "RvR : autoriser", "RvR : retirer", "RvR : nommer chef", "RvR : retirer chef"},
	}
	for _, c := range cases {
		g, r := accessCommandNames(label, c.faction)
		n, d := squadLeadCommandNames(label, c.faction)
		if g != c.grant || r != c.revoke || n != c.name || d != c.dis {
			t.Errorf("%q: got %q %q %q %q", c.faction, g, r, n, d)
		}
	}
}

func TestLongEventLabelCommandNames(t *testing.T) {
	label := "Guerre de territoire hebdomadaire"
	for _, f := range []string{"Rouges", "Une faction au nom beaucoup trop long"} {
		g, r := accessCommandNames(label, f)
		n, d := squadLeadCommandNames(label, f)
		names := map[string]bool{}
		for _, s := range []string{g, r, n, d} {
			if k := len([]rune(s)); k == 0 || k > 32 {
				t.Errorf("%q: %q has %d runes", f, s, k)
			}
			names[s] = true
		}
		if len(names) != 4 {
			t.Errorf("%q: names collide: %q %q %q %q", f, g, r, n, d)
		}
	}
}

// Without a faction the names drop it instead of leaving a double space.
func TestCommandNamesWithoutFaction(t *testing.T) {
	g, r := accessCommandNames("RvR", "")
	n, d := squadLeadCommandNames("RvR", " ")
	got := []string{g, r, n, d}
	want := []string{"RvR : autoriser", "RvR : retirer", "RvR : nommer chef", "RvR : retirer chef"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("name %d = %q, want %q", i, got[i], want[i])
		}
	}
}
