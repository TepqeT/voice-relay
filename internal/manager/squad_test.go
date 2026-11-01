package manager

import (
	"testing"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

func TestSquadGate(t *testing.T) {
	const raidLead, squadLead, member snowflake.ID = 1, 2, 3
	flags := map[snowflake.ID]voice.SpeakingFlags{}
	gate := squadGate(
		func(id snowflake.ID) bool { return id != member },
		func(id snowflake.ID) bool { return id == raidLead },
		func(id snowflake.ID) (voice.SpeakingFlags, bool) { f, ok := flags[id]; return f, ok },
	)
	if !gate(raidLead) {
		t.Error("raid lead must always pass")
	}
	if gate(member) {
		t.Error("member without role must never pass")
	}
	if gate(squadLead) {
		t.Error("squad lead without speaking flags must not pass")
	}
	flags[squadLead] = voice.SpeakingFlagMicrophone
	if gate(squadLead) {
		t.Error("squad lead on the normal key must not pass")
	}
	flags[squadLead] = voice.SpeakingFlagMicrophone | voice.SpeakingFlagPriority
	if !gate(squadLead) {
		t.Error("squad lead on the priority key must pass")
	}
}

func TestRecordSpeakingStoresLatestFlags(t *testing.T) {
	m := &Service{}
	m.recordSpeaking(10, 2, voice.SpeakingFlagPriority)
	m.recordSpeaking(10, 2, voice.SpeakingFlagMicrophone)
	v, ok := m.squad.speaking.Load(speakingKey{guildID: 10, userID: 2})
	if !ok || v.(voice.SpeakingFlags) != voice.SpeakingFlagMicrophone {
		t.Fatalf("flags = %v %v", v, ok)
	}
	m.recordSpeaking(10, 0, voice.SpeakingFlagPriority) // unknown user ignored
	if _, ok := m.squad.speaking.Load(speakingKey{guildID: 10, userID: 0}); ok {
		t.Fatal("user 0 must be ignored")
	}
}
