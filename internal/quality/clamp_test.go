package quality

import (
	"math"
	"testing"
)

func TestClampInt64(t *testing.T) {
	cases := map[uint64]int64{0: 0, 42: 42, math.MaxInt64: math.MaxInt64, math.MaxUint64: math.MaxInt64}
	for in, want := range cases {
		if got := clampInt64(in); got != want {
			t.Errorf("clampInt64(%d) = %d, want %d", in, got, want)
		}
	}
}
