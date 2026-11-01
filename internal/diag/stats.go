// Package diag records detailed raid diagnostics: how each raid lead's audio
// reaches the bot (loss, reordering, jitter, bursts), how each bot plays it out
// (stalls, queueing, drops), which Discord voice server each bot uses, and the
// events around them. It writes one JSON Lines file per raid for post-test
// analysis and a compact health line in the logs.
//
// Everything on the audio path is a counter update: the expensive work (JSON,
// files, runtime stats) happens on the snapshot ticker, never per packet.
package diag

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Inter-arrival buckets, in milliseconds, for packets of one talk spurt.
// Discord sends one packet every 20 ms: values well below are bursts (packets
// held back then released together), values well above are late packets.
var gapBoundsMs = []float64{5, 15, 25, 40, 60, 100, 200}

// spurtGap separates talk spurts: Discord stops sending during silence, so a
// longer gap is a pause in speech, not a network problem.
const spurtGap = 200 * time.Millisecond

// maxSeqJump bounds a sequence gap counted as loss; larger jumps are a stream
// restart (new SSRC, client reconnect).
const maxSeqJump = 500

// streamRestartGap: a packet older than the reference arriving after this much
// silence starts a new stream rather than being a late packet.
const streamRestartGap = time.Second

// SourceStats is the receive side of one raid lead (one Discord user).
type SourceStats struct {
	mu sync.Mutex

	packets   int64
	lost      int64
	reordered int64
	dup       int64
	spurts    int64
	gaps      [8]int64 // len(gapBoundsMs)+1
	maxGapMs  float64

	jitterMs    float64 // RFC 3550 interarrival jitter, smoothed
	maxJitterMs float64

	haveLast    bool
	lastSeq     uint16
	lastTS      uint32
	lastArrival time.Time
}

// observe records one packet. Called on the UDP receive goroutine.
func (s *SourceStats) observe(seq uint16, ts uint32, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packets++
	if !s.haveLast {
		s.haveLast = true
		s.spurts++
		s.lastSeq, s.lastTS, s.lastArrival = seq, ts, at
		return
	}
	delta := int16(seq - s.lastSeq) // #nosec G115 -- intentional wrap: RTP sequence numbers roll over at 65535
	switch {
	case delta == 0:
		s.dup++
		return
	case int(delta) < -maxSeqJump || (delta < 0 && at.Sub(s.lastArrival) >= streamRestartGap):
		// Far behind, or behind after a long silence: the client restarted
		// its stream (rejoin, new SSRC) with a new random sequence number.
		// Treat it as a new reference, not as thousands of late packets.
		s.spurts++
		s.lastSeq, s.lastTS, s.lastArrival = seq, ts, at
		return
	case delta < 0:
		// Late packet from the past: counted, but it does not move the
		// reference point, or every later packet would look lost.
		s.reordered++
		return
	case int(delta) > maxSeqJump:
		s.spurts++ // stream restart
	default:
		if delta > 1 {
			s.lost += int64(delta - 1)
		}
	}

	gap := at.Sub(s.lastArrival)
	if gap >= spurtGap {
		s.spurts++
	} else {
		ms := float64(gap.Microseconds()) / 1000
		s.gaps[bucket(ms)]++
		if ms > s.maxGapMs {
			s.maxGapMs = ms
		}
		// RFC 3550 section 6.4.1, with the 48 kHz RTP clock.
		sent := float64(int32(ts-s.lastTS)) / 48.0 // #nosec G115 -- intentional wrap: RTP timestamps roll over
		d := math.Abs(ms - sent)
		s.jitterMs += (d - s.jitterMs) / 16
		if s.jitterMs > s.maxJitterMs {
			s.maxJitterMs = s.jitterMs
		}
	}
	s.lastSeq, s.lastTS, s.lastArrival = seq, ts, at
}

func bucket(ms float64) int {
	for i, b := range gapBoundsMs {
		if ms < b {
			return i
		}
	}
	return len(gapBoundsMs)
}

// SourceSnapshot is the activity of one source over one interval.
type SourceSnapshot struct {
	UserID      string  `json:"user_id"`
	Packets     int64   `json:"packets"`
	Lost        int64   `json:"lost"`
	Reordered   int64   `json:"reordered"`
	Duplicates  int64   `json:"duplicates"`
	Spurts      int64   `json:"talk_spurts"`
	Gaps        []int64 `json:"gaps_ms_hist"` // <5,<15,<25,<40,<60,<100,<200,>=200
	MaxGapMs    float64 `json:"max_gap_ms"`
	JitterMs    float64 `json:"jitter_ms"`
	MaxJitterMs float64 `json:"max_jitter_ms"`
}

// take returns the interval counters and resets them. The jitter estimate and
// the sequence reference persist across intervals.
func (s *SourceStats) take(userID string) SourceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := SourceSnapshot{
		UserID:      userID,
		Packets:     s.packets,
		Lost:        s.lost,
		Reordered:   s.reordered,
		Duplicates:  s.dup,
		Spurts:      s.spurts,
		Gaps:        append([]int64(nil), s.gaps[:]...),
		MaxGapMs:    round1(s.maxGapMs),
		JitterMs:    round1(s.jitterMs),
		MaxJitterMs: round1(s.maxJitterMs),
	}
	s.packets, s.lost, s.reordered, s.dup, s.spurts = 0, 0, 0, 0, 0
	s.gaps = [8]int64{}
	s.maxGapMs = 0
	s.maxJitterMs = s.jitterMs
	return snap
}

// reset clears every counter. Fields are zeroed one by one: assigning a whole
// SourceStats{} would also overwrite the held mutex, and the deferred Unlock
// then crashes the process ("sync: unlock of unlocked mutex").
func (s *SourceStats) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packets, s.lost, s.reordered, s.dup, s.spurts = 0, 0, 0, 0, 0
	s.gaps = [8]int64{}
	s.maxGapMs, s.jitterMs, s.maxJitterMs = 0, 0, 0
	s.haveLast, s.lastSeq, s.lastTS, s.lastArrival = false, 0, 0, time.Time{}
}

// OutputStats is the playout side of one bot: how regularly the Discord audio
// sender gets a frame from it.
type OutputStats struct {
	relay       atomic.Int64 // relayed frames handed to the sender
	announce    atomic.Int64 // announcement frames handed to the sender
	drops       atomic.Int64 // frames discarded to cap latency
	depthSum    atomic.Int64 // queued frames behind each relayed frame
	depthMax    atomic.Int64
	stalls40    atomic.Int64 // sender waited 40-60 ms for a frame mid-speech
	stalls60    atomic.Int64 // sender waited 60 ms or more mid-speech
	maxInterval atomic.Int64 // microseconds, mid-speech
	last        atomic.Int64 // unix nanoseconds of the previous frame
}

// Frame records one frame handed to the sender. depth is the number of relayed
// frames still queued, drops the frames discarded during this call.
func (o *OutputStats) Frame(announcement bool, depth, drops int) {
	if o == nil {
		return
	}
	now := time.Now().UnixNano()
	if prev := o.last.Swap(now); prev != 0 {
		iv := time.Duration(now - prev)
		if iv < spurtGap {
			us := iv.Microseconds()
			for {
				cur := o.maxInterval.Load()
				if us <= cur || o.maxInterval.CompareAndSwap(cur, us) {
					break
				}
			}
			switch {
			case iv >= 60*time.Millisecond:
				o.stalls60.Add(1)
			case iv >= 40*time.Millisecond:
				o.stalls40.Add(1)
			}
		}
	}
	if announcement {
		o.announce.Add(1)
		return
	}
	o.relay.Add(1)
	o.drops.Add(int64(drops))
	o.depthSum.Add(int64(depth))
	d := int64(depth)
	for {
		cur := o.depthMax.Load()
		if d <= cur || o.depthMax.CompareAndSwap(cur, d) {
			break
		}
	}
}

// OutputSnapshot is the activity of one bot output over one interval.
type OutputSnapshot struct {
	BotID         string  `json:"bot_id"`
	VoiceServer   string  `json:"voice_server,omitempty"`
	Relayed       int64   `json:"relayed"`
	Announcement  int64   `json:"announcement"`
	Drops         int64   `json:"drops"`
	DepthAvg      float64 `json:"queue_avg"`
	DepthMax      int64   `json:"queue_max"`
	Stalls40      int64   `json:"stalls_40ms"`
	Stalls60      int64   `json:"stalls_60ms"`
	MaxIntervalMs float64 `json:"max_interval_ms"`
}

func (o *OutputStats) take(botID string) OutputSnapshot {
	relay := o.relay.Swap(0)
	depthSum := o.depthSum.Swap(0)
	snap := OutputSnapshot{
		BotID:         botID,
		Relayed:       relay,
		Announcement:  o.announce.Swap(0),
		Drops:         o.drops.Swap(0),
		DepthMax:      o.depthMax.Swap(0),
		Stalls40:      o.stalls40.Swap(0),
		Stalls60:      o.stalls60.Swap(0),
		MaxIntervalMs: round1(float64(o.maxInterval.Swap(0)) / 1000),
	}
	if relay > 0 {
		snap.DepthAvg = round1(float64(depthSum) / float64(relay))
	}
	return snap
}

func (o *OutputStats) reset() {
	o.relay.Store(0)
	o.announce.Store(0)
	o.drops.Store(0)
	o.depthSum.Store(0)
	o.depthMax.Store(0)
	o.stalls40.Store(0)
	o.stalls60.Store(0)
	o.maxInterval.Store(0)
	o.last.Store(0)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
