package bot

import "testing"

func TestAccessCommandNamesFitDiscordLimit(t *testing.T) {
	for _, f := range []string{"Élyséens", "Asmodiens", "Une faction au nom beaucoup trop long"} {
		g, r := accessCommandNames(f)
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
	if g, _ := accessCommandNames("Élyséens"); g != "RvR Élyséens : autoriser" {
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
		n, d := squadLeadCommandNames(f)
		g, r := accessCommandNames(f)
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
