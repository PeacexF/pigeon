// Package dedupe remembers idempotency keys for a fixed window.
package dedupe

import (
	"sync"
	"time"
)

// Window is a bounded set of keys that expire after a fixed duration. Since
// every key lives equally long, insertion order is expiry order, so a FIFO
// replaces a heap.
type Window struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]holder
	order   []ref
}

type holder struct {
	owner   string
	expires time.Time
}

type ref struct{ key, owner string }

// New returns a Window keeping keys for ttl. Past max keys, the oldest are forgotten early.
func New(ttl time.Duration, maxKeys int) *Window {
	return &Window{ttl: ttl, max: maxKeys, entries: make(map[string]holder)}
}

// Reserve claims key for owner. If key is already held, it returns the
// holder and false.
func (w *Window) Reserve(key, owner string, now time.Time) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.evict(now)
	if h, ok := w.entries[key]; ok {
		return h.owner, false
	}
	w.entries[key] = holder{owner: owner, expires: now.Add(w.ttl)}
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
