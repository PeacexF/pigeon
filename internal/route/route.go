// Package route maps event types to targets using * wildcard patterns.
package route

import "slices"

// Table maps patterns to targets. The zero value is an empty table.
type Table[T comparable] struct {
	entries []entry[T]
}

type entry[T comparable] struct {
	pattern string
	targets []T
}

// Add routes s matching pattern to targets.
func (t *Table[T]) Add(pattern string, targets ...T) {
	t.entries = append(t.entries, entry[T]{pattern: pattern, targets: targets})
}

// Lookup returns the union of targets of every pattern matching s, each once,
// in registration order.
func (t *Table[T]) Lookup(s string) []T {
	var out []T
	for _, e := range t.entries {
		if !Match(e.pattern, s) {
			continue
		}
		for _, target := range e.targets {
			if !slices.Contains(out, target) {
				out = append(out, target)
			}
		}
	}
	return out
}

// Match reports whether s matches pattern, where * matches any run of
// characters, dots included.
func Match(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
