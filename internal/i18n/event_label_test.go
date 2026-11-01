package i18n

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDefaultEventLabelKeepsMessages checks that, with the default label,
// every message that said "RvR" before EVENT_LABEL (snapshot of v27 in
// testdata) still renders exactly the same.
func TestDefaultEventLabelKeepsMessages(t *testing.T) {
	data, err := os.ReadFile("testdata/rvr_labels_v27.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]string
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	b := MustBundle()
	for lang, msgs := range golden {
		raw, err := localesFS.ReadFile(path.Join("locales", lang+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var cur map[string]any
		if err := yaml.Unmarshal(raw, &cur); err != nil {
			t.Fatal(err)
		}
		loc := b.For(lang, "")
		for key, want := range msgs {
			s, _ := cur[key].(string)
			if got := strings.ReplaceAll(s, "{{.Event}}", DefaultEventLabel); got != want {
				t.Errorf("%s %s: %q, want %q", lang, key, got, want)
			}
			if strings.Contains(want, "{{") {
				continue // other variables: covered by the raw comparison
			}
			if got := loc.T(key); got != want {
				t.Errorf("%s %s rendered %q, want %q", lang, key, got, want)
			}
		}
	}
	if n := len(golden["fr"]); n == 0 {
		t.Fatal("empty snapshot")
	}
}

func TestCustomEventLabel(t *testing.T) {
	b := MustBundle()
	if b.EventLabel() != "RvR" {
		t.Fatalf("default label = %q", b.EventLabel())
	}
	b.SetEventLabel(" GvG ")
	got := b.For("fr", "").T("raid.already_active")
	if got != "⚠️ Un raid GvG est déjà actif sur ce serveur." {
		t.Fatalf("got %q", got)
	}
	b.SetEventLabel("")
	if b.EventLabel() != "RvR" {
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
