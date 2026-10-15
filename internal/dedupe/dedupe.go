// Package dedupe remembers idempotency keys for a fixed window.
package dedupe

import (
	"sync"
	"time"
)

// Window is a bounded set of expiring keys. Callers normally use one TTL,
// so insertion order is expiry order and a FIFO replaces a heap; a key that
// outlives one behind it is evicted late but never honored past its expiry.
type Window struct {
	mu      sync.Mutex
	max     int
	entries map[string]holder
	order   []ref
}

type holder struct {
	owner   string
	expires time.Time
}

type ref struct{ key, owner string }

// New returns an empty Window. Past maxKeys keys, the oldest are forgotten early.
func New(maxKeys int) *Window {
	return &Window{max: maxKeys, entries: make(map[string]holder)}
}

// Reserve claims key for owner for ttl. If key is already held, it returns
// the holder and false.
func (w *Window) Reserve(key, owner string, now time.Time, ttl time.Duration) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.evict(now)
	if h, ok := w.entries[key]; ok && now.Before(h.expires) {
		return h.owner, false
	}
	w.entries[key] = holder{owner: owner, expires: now.Add(ttl)}
	w.order = append(w.order, ref{key, owner})
	return owner, true
}

// Release frees key if owner still holds it.
func (w *Window) Release(key, owner string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h, ok := w.entries[key]; ok && h.owner == owner {
		delete(w.entries, key)
	}
}

// Len returns the number of held keys.
func (w *Window) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.entries)
}

func (w *Window) evict(now time.Time) {
	for len(w.order) > 0 {
		front := w.order[0]
		h, ok := w.entries[front.key]
		switch {
		case !ok || h.owner != front.owner: // released or re-reserved since
		case now.Before(h.expires) && len(w.entries) < w.max:
			return
		default:
			delete(w.entries, front.key)
		}
		w.order = w.order[1:]
	}
}
