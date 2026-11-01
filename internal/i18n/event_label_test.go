package i18n

import (
	"strings"
	"testing"
)

func TestCustomEventLabel(t *testing.T) {
	b := MustBundle()
	if b.EventLabel() != "Voice Relay" {
		t.Fatalf("default label = %q", b.EventLabel())
	}
	b.SetEventLabel(" Briefing ")
	got := b.For("fr", "").T("access.batch.kicked", "List", "x")
	if got != "Sortis du vocal Briefing : x" {
		t.Fatalf("got %q", got)
	}
	b.SetEventLabel("")
	if b.EventLabel() != "Voice Relay" {
		t.Fatal("empty label must restore the default")
	}
}

func TestDescriptionsStayWithinDiscordLimit(t *testing.T) {
	b := MustBundle()
	b.SetEventLabel(strings.Repeat("x", 120))
	for _, v := range b.DescriptionLocalizations("cmd.start.description") {
		if n := len([]rune(v)); n > 100 {
			t.Fatalf("description is %d chars", n)
		}
	}
	if got := TruncateRunes("abcdef", 4); got != "abc…" {
		t.Fatalf("TruncateRunes = %q", got)
	}
}
