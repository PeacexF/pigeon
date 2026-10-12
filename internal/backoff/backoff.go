// Package backoff computes retry delays.
package backoff

import (
	"math/rand/v2"
	"time"
)

// FullJitter returns a random delay in [0, min(maxDelay, base·2^(attempt-1))].
// Spreading retries over the whole range avoids synchronized retry storms.
func FullJitter(attempt int, base, maxDelay time.Duration) time.Duration {
	ceil := maxDelay
	if shift := attempt - 1; shift >= 0 && shift < 62 && base <= maxDelay>>shift {
		ceil = base << shift
	}
	return rand.N(ceil + 1) //nolint:gosec // jitter needs no cryptographic randomness
}
