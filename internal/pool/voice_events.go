package pool

import (
	"sync"

	"github.com/disgoorg/disgo/voice"
)

// Voice gateway events. disgo v0.20.0 takes one event handler per voice
// manager, fixed when the client is built (WithConnEventHandlerFunc); a conn
// no longer offers SetEventHandlerFunc. Every client installs VoiceEventsOpt,
// and whoever needs the events of one connection subscribes to its gateway
// with WatchVoiceGateway.
//
// UPSTREAM(disgo v0.20.0): no per-conn event handler after the conn exists.

var voiceWatchers sync.Map // voice.Gateway -> voice.EventHandlerFunc

// VoiceEventsOpt routes the voice gateway events of a client's connections
// to the watchers registered with WatchVoiceGateway.
func VoiceEventsOpt() voice.ManagerConfigOpt {
	return voice.WithConnConfigOpts(voice.WithConnEventHandlerFunc(dispatchVoiceEvent))
}

func dispatchVoiceEvent(gw voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
	if fn, ok := voiceWatchers.Load(gw); ok {
		fn.(voice.EventHandlerFunc)(gw, op, seq, data)
	}
}

// WatchVoiceGateway calls fn for every event of gw, replacing any previous
// watcher of that gateway. A nil gateway (test doubles) is ignored. Entries
// live as long as the process: one per voice connection opened, a few per
// session, so they are not reclaimed.
func WatchVoiceGateway(gw voice.Gateway, fn voice.EventHandlerFunc) {
	if gw == nil || fn == nil {
		return
	}
	voiceWatchers.Store(gw, fn)
}

// UnwatchVoiceGateway forgets the watcher of gw.
func UnwatchVoiceGateway(gw voice.Gateway) {
	if gw != nil {
		voiceWatchers.Delete(gw)
	}
}
