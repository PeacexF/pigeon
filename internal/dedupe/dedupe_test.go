package dedupe

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestReserve(t *testing.T) {
	w := New(100)

	if owner, ok := w.Reserve("k", "e1", t0, time.Hour); !ok || owner != "e1" {
		t.Fatalf("first Reserve = %q, %v", owner, ok)
	}
	if owner, ok := w.Reserve("k", "e2", t0.Add(59*time.Minute), time.Hour); ok || owner != "e1" {
		t.Errorf("Reserve within window = %q, %v; want e1, false", owner, ok)
	}
	if owner, ok := w.Reserve("k", "e3", t0.Add(time.Hour), time.Hour); !ok || owner != "e3" {
		t.Errorf("Reserve after window = %q, %v; want e3, true", owner, ok)
	}
}

func TestRelease(t *testing.T) {
	w := New(100)
	w.Reserve("k", "e1", t0, time.Hour)

	w.Release("k", "someone-else")
	if _, ok := w.Reserve("k", "e2", t0, time.Hour); ok {
		t.Error("Release by a non-owner freed the key")
	}

	w.Release("k", "e1")
	if _, ok := w.Reserve("k", "e2", t0, time.Hour); !ok {
		t.Error("key still held after Release by its owner")
	}

	// The stale FIFO entry from e1 must not evict e2's reservation early.
	w.Reserve("other", "e3", t0.Add(30*time.Minute), time.Hour)
	if _, ok := w.Reserve("k", "e4", t0.Add(30*time.Minute), time.Hour); ok {
		t.Error("stale entry evicted the current holder")
	}
}

func TestMaxKeys(t *testing.T) {
	w := New(3)
	for i := range 5 {
		w.Reserve(fmt.Sprint(i), "e", t0, time.Hour)
	}
	if n := w.Len(); n != 3 {
		t.Errorf("Len = %d, want 3", n)
	}
	if _, ok := w.Reserve("0", "e", t0, time.Hour); !ok {
		t.Error("oldest key was not evicted")
	}
	if _, ok := w.Reserve("4", "e", t0, time.Hour); ok {
		t.Error("newest key was evicted")
	}
}

func TestPerKeyTTL(t *testing.T) {
	w := New(100)
	w.Reserve("long", "e1", t0, time.Hour)
	w.Reserve("short", "e2", t0, time.Minute)
	if _, ok := w.Reserve("short", "e3", t0.Add(2*time.Minute), time.Minute); !ok {
		t.Error("expired key behind a longer-lived one is still held")
	}
	if _, ok := w.Reserve("long", "e4", t0.Add(2*time.Minute), time.Minute); ok {
		t.Error("unexpired key was freed")
	}
}
