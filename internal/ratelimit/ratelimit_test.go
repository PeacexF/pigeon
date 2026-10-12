package ratelimit

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestBurstThenRefill(t *testing.T) {
	l := New(2, time.Second)

	for i := range 2 {
		if d := l.Take(t0); d != 0 {
			t.Fatalf("burst token %d: wait %v", i, d)
		}
	}
	if d := l.Take(t0); d != 500*time.Millisecond {
		t.Errorf("empty bucket: wait %v, want 500ms", d)
	}
	if d := l.Take(t0.Add(500 * time.Millisecond)); d != 0 {
		t.Errorf("after refill: wait %v, want 0", d)
	}
}

func TestRefillCapsAtBurst(t *testing.T) {
	l := New(1, time.Second)
	l.Take(t0)
	if d := l.Take(t0.Add(time.Hour)); d != 0 {
		t.Fatalf("wait %v after an hour idle", d)
	}
	if d := l.Take(t0.Add(time.Hour)); d == 0 {
		t.Error("idle time banked more than one burst")
	}
}

func TestUnlimited(t *testing.T) {
	l := New(0, 0)
	for range 1000 {
		if d := l.Take(t0); d != 0 {
			t.Fatalf("unlimited limiter asked to wait %v", d)
		}
	}
}

func TestPause(t *testing.T) {
	l := New(0, 0)
	l.Pause(t0.Add(10 * time.Second))
	l.Pause(t0.Add(5 * time.Second)) // shorter pause must not shorten the longer one

	if d := l.Take(t0); d != 10*time.Second {
		t.Errorf("paused: wait %v, want 10s", d)
	}
	if d := l.Take(t0.Add(10 * time.Second)); d != 0 {
		t.Errorf("after pause: wait %v, want 0", d)
	}
}

func TestPauseDoesNotSpendTokens(t *testing.T) {
	l := New(1, time.Second)
	l.Pause(t0.Add(time.Second))
	l.Take(t0)
	if d := l.Take(t0.Add(time.Second)); d != 0 {
		t.Errorf("token spent while paused: wait %v", d)
	}
}
