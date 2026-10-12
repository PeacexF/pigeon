package id

import (
	"regexp"
	"testing"
	"testing/synctest"
	"time"
)

var uuidV7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestFormat(t *testing.T) {
	for range 100 {
		if s := New(); !uuidV7.MatchString(s) {
			t.Fatalf("%q is not a UUIDv7", s)
		}
	}
}

func TestUnique(t *testing.T) {
	seen := make(map[string]bool)
	for range 10_000 {
		s := New()
		if seen[s] {
			t.Fatalf("duplicate %s", s)
		}
		seen[s] = true
	}
}

func TestSortsByTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := New()
		time.Sleep(time.Millisecond)
		if b := New(); b <= a {
			t.Errorf("%s generated after %s sorts before it", b, a)
		}
	})
}
