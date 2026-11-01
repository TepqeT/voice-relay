package diag

import (
	"math"
	"sync/atomic"
	"time"
)

// overlapTick is the length of one Opus frame: one mixed frame stands for
// 20 ms of several voices at once.
const overlapTick = 20 * time.Millisecond

// overlapCounter measures how long several callers actually spoke at the same
// time. Every destination mixer reports each frame it had to mix, so the same
// 20 ms is reported once per mixer: frames closer than 15 ms to the last
// counted one are folded into it.
type overlapCounter struct {
	last   atomic.Int64 // unix nanoseconds of the last counted frame
	frames atomic.Int64
	peak   atomic.Int32
}

func (o *overlapCounter) reset() {
	o.last.Store(0)
	o.frames.Store(0)
	o.peak.Store(0)
}

func (o *overlapCounter) add(sources int, now time.Time) {
	n := now.UnixNano()
	last := o.last.Load()
	if n-last >= int64(15*time.Millisecond) && o.last.CompareAndSwap(last, n) {
		o.frames.Add(1)
	}
	n32 := int32(math.MaxInt32)
	if sources < math.MaxInt32 {
		n32 = int32(sources) // #nosec G115 -- bounded by the check above
	}
	for {
		p := o.peak.Load()
		if n32 <= p || o.peak.CompareAndSwap(p, n32) {
			return
		}
	}
}

func (o *overlapCounter) seconds() float64 {
	return round1(float64(o.frames.Load()) * overlapTick.Seconds())
}

// MixFrame records that a mixer combined sources voices into one frame (two
// or more callers speaking at once). Hot path, lock-free; ignored when no
// raid is being recorded.
func (g *Guild) MixFrame(sources int) {
	if g == nil || sources < 2 {
		return
	}
	g.overlap.add(sources, time.Now())
}
