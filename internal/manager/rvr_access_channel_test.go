package manager

import (
	"slices"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestParseAccessMessage(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		revoke bool
		ids    []snowflake.ID
		names  []string
	}{
		{"mentions", "<@123456789012345678> <@!223456789012345678>", false,
			[]snowflake.ID{123456789012345678, 223456789012345678}, nil},
		{"pasted column with display names", "pierre.dupont\nJean Dupont\n\n@Marc\n", false, nil,
			[]string{"pierre.dupont", "Jean Dupont", "Marc"}},
		{"sentence ignored", "Voici la liste complète de ma légion pour ce soir\n@marc", false, nil, []string{"marc"}},
		{"header line ignored", "Légion Lumière :\nmarc", false, nil, []string{"marc"}},
		{"revoke", "retirer <@123456789012345678> @marc", true, []snowflake.ID{123456789012345678}, []string{"marc"}},
		{"revoke alone on its line", "Retirer:\nmarc\nluc", true, nil, []string{"marc", "luc"}},
		{"several on a line", "ajouter @marc, @luc; @Marc", false, nil, []string{"marc", "luc"}},
		{"raw id", "323456789012345678", false, []snowflake.ID{323456789012345678}, nil},
		{"display name with @", "@Jean Dupont", false, nil, []string{"Jean Dupont"}},
	}
	for _, c := range cases {
		got := ParseAccessMessage(c.in)
		if got.Revoke != c.revoke || !slices.Equal(got.IDs, c.ids) || !slices.Equal(got.Names, c.names) {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestDisplayName(t *testing.T) {
	nick, global := "Chef", "Jean"
	mb := discord.Member{User: discord.User{Username: "jean.d", GlobalName: &global}}
	if got := DisplayName(mb); got != "Jean" {
		t.Fatalf("global name: %q", got)
	}
	mb.Nick = &nick
	if got := DisplayName(mb); got != "Chef" {
		t.Fatalf("nickname: %q", got)
	}
	if normName("  Jean   DUPONT ") != "jean dupont" {
		t.Fatal("normName")
	}
}
