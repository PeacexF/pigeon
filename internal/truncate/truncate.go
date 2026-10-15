// Package truncate shortens text to a provider's length limit, never
// splitting a character, and marks the cut with "…".
//
// Providers count length differently, hence one function per unit.
package truncate

import (
	"unicode/utf16"
	"unicode/utf8"
)

// Ellipsis marks truncated text.
const Ellipsis = "…"

// Runes truncates s to at most limit Unicode code points.
func Runes(s string, limit int) string {
	return cut(s, limit, func(rune) int { return 1 }, 1)
}

// UTF16 truncates s to at most limit UTF-16 code units, the unit JavaScript
// string lengths count.
func UTF16(s string, limit int) string {
	return cut(s, limit, utf16Width, 1)
}

// Bytes truncates s to at most limit bytes of UTF-8.
func Bytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return cut(s, limit, utf8.RuneLen, len(Ellipsis))
}

func utf16Width(r rune) int { return max(utf16.RuneLen(r), 1) }

func cut(s string, limit int, width func(rune) int, ellipsisWidth int) string {
	if limit <= 0 {
		return ""
	}
	total := 0
	for _, r := range s {
		total += max(width(r), 1)
	}
	if total <= limit {
		return s
	}
	budget, suffix := limit-ellipsisWidth, Ellipsis
	if budget < 0 {
		budget, suffix = limit, ""
	}
	n := 0
	for i, r := range s {
		w := max(width(r), 1)
		if n+w > budget {
			return s[:i] + suffix
		}
		n += w
	}
	return s
}
