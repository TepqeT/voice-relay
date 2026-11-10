package pool

import (
	"testing"

	"github.com/disgoorg/disgo/voice"
)

type fakeGateway struct{ voice.Gateway }

func TestVoiceEventsReachTheGatewayWatcher(t *testing.T) {
	a, b := &fakeGateway{}, &fakeGateway{}
	var gotA, gotB []voice.Opcode
	WatchVoiceGateway(a, func(_ voice.Gateway, op voice.Opcode, _ int, _ voice.GatewayMessageData) { gotA = append(gotA, op) })
	WatchVoiceGateway(b, func(_ voice.Gateway, op voice.Opcode, _ int, _ voice.GatewayMessageData) { gotB = append(gotB, op) })
	t.Cleanup(func() { UnwatchVoiceGateway(a); UnwatchVoiceGateway(b) })

	dispatchVoiceEvent(a, voice.OpcodeReady, 1, nil)
	dispatchVoiceEvent(b, voice.OpcodeSpeaking, 2, nil)
	dispatchVoiceEvent(&fakeGateway{}, voice.OpcodeReady, 3, nil) // nobody watches: dropped
	dispatchVoiceEvent(nil, voice.OpcodeReady, 4, nil)

	if len(gotA) != 1 || gotA[0] != voice.OpcodeReady || len(gotB) != 1 || gotB[0] != voice.OpcodeSpeaking {
		t.Fatalf("a=%v b=%v", gotA, gotB)
	}
	UnwatchVoiceGateway(a)
	dispatchVoiceEvent(a, voice.OpcodeReady, 5, nil)
	if len(gotA) != 1 {
		t.Fatal("event delivered after Unwatch")
	}
	WatchVoiceGateway(nil, func(voice.Gateway, voice.Opcode, int, voice.GatewayMessageData) {}) // ignored, no panic
}
