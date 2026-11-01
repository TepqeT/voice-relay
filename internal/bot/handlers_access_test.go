package bot

import (
	"strings"
	"testing"

	"github.com/disgoorg/snowflake/v2"
)

func TestAccessCommandsOffWithoutService(t *testing.T) {
	h := &CommandHandlers{}
	if cmds := h.AccessCommands(); len(cmds) != 0 {
		t.Fatalf("access commands without an access service: %d", len(cmds))
	}
}

// With the default label the names read "Voice Relay: ..." in English and
// "Voice Relay : ..." in French.
func TestDefaultCommandNames(t *testing.T) {
	label := (&CommandHandlers{}).eventLabel()
	g, r := accessCommandNamesEN(label)
	n, d := squadLeadCommandNamesEN(label)
	if got, want := []string{g, r, n, d}, []string{"Voice Relay: grant access", "Voice Relay: revoke access", "Voice Relay: make squad lead", "Voice Relay: remove squad lead"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("English names %q, want %q", got, want)
	}
	g, r = accessCommandNames(label)
	n, d = squadLeadCommandNames(label)
	if got, want := []string{g, r, n, d}, []string{"Voice Relay : autoriser", "Voice Relay : retirer", "Voice Relay : nommer chef", "Voice Relay : retirer chef"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("French names %q, want %q", got, want)
	}
}

// Any label fits Discord's 32 characters and the four names stay distinct.
func TestCommandNamesFitAndDiffer(t *testing.T) {
	for _, label := range []string{"Voice Relay", "Weekly community briefing session", "Ops"} {
		for _, names := range [][4]string{
			func() [4]string {
				g, r := accessCommandNamesEN(label)
				n, d := squadLeadCommandNamesEN(label)
				return [4]string{g, r, n, d}
			}(),
			func() [4]string {
				g, r := accessCommandNames(label)
				n, d := squadLeadCommandNames(label)
				return [4]string{g, r, n, d}
			}(),
		} {
			seen := map[string]bool{}
			for _, s := range names {
				if k := len([]rune(s)); k == 0 || k > 32 {
					t.Errorf("%q: %q has %d runes", label, s, k)
				}
				seen[s] = true
			}
			if len(seen) != 4 {
				t.Errorf("%q: names collide: %q", label, names)
			}
		}
	}
}

func TestTooFast(t *testing.T) {
	const g, u snowflake.ID = 4242, 99
	allowed := 0
	for range 10 {
		if !tooFast(g, u) {
			allowed++
		}
	}
	if allowed != 4 {
		t.Fatalf("%d clicks went through in a burst, want 4", allowed)
	}
	if tooFast(g, 100) {
		t.Fatal("another member has their own budget")
	}
}
