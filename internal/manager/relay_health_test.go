package manager

import (
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/TepqeT/voice-relay/internal/dave"
)

// A relay is reported silent only after two checks in a row with people in
// its channel and outbound encryption failing; any healthy check resets it.
func TestRecordRelayCheckStreak(t *testing.T) {
	const bot snowflake.ID = 7
	streak := map[snowflake.ID]int{}
	check := func(listeners int, state string) int {
		recordRelayCheck(1, relayCheck{botID: bot, channelID: 2, listeners: listeners, daveState: state}, streak)
		return streak[bot]
	}
	if check(3, dave.StateNoKey) != 1 {
		t.Fatal("first failing check not counted")
	}
	if check(3, dave.StateEncrypted) != 0 {
		t.Fatal("a healthy check must reset the streak")
	}
	if check(0, dave.StateNoKey) != 0 {
		t.Fatal("an empty channel is not a silent relay")
	}
	check(2, dave.StateNoKey)
	if check(2, dave.StateNoKey) != silentRelayChecks {
		t.Fatalf("streak = %d, want %d after two failing checks", streak[bot], silentRelayChecks)
	}
	if check(2, dave.StatePassthrough) != 0 {
		t.Fatal("passthrough carries plain audio: not silent")
	}
}
