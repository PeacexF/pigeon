package truncate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		fn    func(string, int) string
		in    string
		limit int
		want  string
	}{
		{"runes short", Runes, "héllo", 5, "héllo"},
		{"runes cut", Runes, "héllo world", 5, "héll…"},
		{"runes emoji", Runes, "😀😀😀", 2, "😀…"},
		{"utf16 at limit", UTF16, "abcd", 4, "abcd"},
		{"utf16 cut", UTF16, "abcdef", 4, "abc…"},
		// 😀 is two UTF-16 units: an odd budget must not split it.
		{"utf16 emoji", UTF16, strings.Repeat("😀", 10), 6, "😀😀…"},
		{"bytes short", Bytes, "abc", 3, "abc"},
		{"bytes cut", Bytes, "abcdefgh", 6, "abc…"},
		{"bytes multibyte", Bytes, "ééééé", 7, "éé…"},
		{"bytes tiny limit", Bytes, "abcdef", 2, "ab"},
		{"zero limit", Runes, "abc", 0, ""},
		{"one", Runes, "abc", 1, "…"},
	}
	for _, tt := range tests {
		if got := tt.fn(tt.in, tt.limit); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func FuzzBytes(f *testing.F) {
	f.Add("héllo wörld 😀", 7)
	f.Fuzz(func(t *testing.T, s string, limit int) {
		if !utf8.ValidString(s) || limit < 0 || limit > 1<<16 {
			return
		}
		got := Bytes(s, limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Errorf("Bytes(%q, %d) = %q", s, limit, got)
		}
		if len(s) <= limit && got != s {
			t.Errorf("Bytes(%q, %d) changed text within the limit", s, limit)
		}
	})
}
