package backoff

import (
	"testing"
	"time"
)

func TestFullJitterBounds(t *testing.T) {
	tests := []struct {
		attempt int
		ceil    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{4, 8 * time.Second},
		{10, time.Minute},
		{1000, time.Minute},
		{0, time.Minute},
	}
	for _, tt := range tests {
		for range 1000 {
			if d := FullJitter(tt.attempt, time.Second, time.Minute); d < 0 || d > tt.ceil {
				t.Fatalf("FullJitter(%d) = %v, want within [0, %v]", tt.attempt, d, tt.ceil)
			}
		}
	}
}

func TestFullJitterSpreads(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 100 {
		seen[FullJitter(5, time.Second, time.Minute)] = true
	}
	if len(seen) < 50 {
		t.Errorf("only %d distinct delays in 100 draws", len(seen))
	}
}
