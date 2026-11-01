package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(time.Second, 3)
	l.now = func() time.Time { return now }
	for i := range 3 {
		if !l.Allow("a") {
			t.Fatalf("call %d refused within the burst", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th call in a row must be refused")
	}
	if !l.Allow("b") {
		t.Fatal("another key has its own budget")
	}
	now = now.Add(time.Second)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("one token per interval")
	}
	now = now.Add(time.Hour)
	l.Allow("c")
	if l.Len() != 1 {
		t.Fatalf("idle keys must be forgotten, %d left", l.Len())
	}
}
