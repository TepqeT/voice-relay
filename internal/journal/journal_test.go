package journal

import "testing"

func TestJournal(t *testing.T) {
	reset()
	Record(Event{GuildID: 1, Kind: "bot_joined"})
	Record(Event{GuildID: 2, Kind: "missing_permissions", Level: Problem})
	Record(Event{GuildID: 1, Kind: "role_refused", Level: Warning})
	if got := Recent(10, false); len(got) != 3 || got[0].Kind != "role_refused" {
		t.Fatalf("recent %+v", got)
	}
	if got := Recent(10, true); len(got) != 2 {
		t.Fatalf("problems only %+v", got)
	}
	if got := ForGuild(1, 10); len(got) != 2 || got[1].Kind != "bot_joined" {
		t.Fatalf("guild 1 %+v", got)
	}
	for range capacity + 10 {
		Record(Event{Kind: "x"})
	}
	if len(Recent(1000, false)) != capacity {
		t.Fatal("the journal must stay bounded")
	}
}
