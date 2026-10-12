package route

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"alert.disk", "alert.disk", true},
		{"alert.disk", "alert.disks", false},
		{"alert.*", "alert.disk", true},
		{"alert.*", "alert.disk.full", true},
		{"alert.*", "alert.", true},
		{"alert.*", "alert", false},
		{"*.critical", "db.critical", true},
		{"*.critical", "db.critical.x", false},
		{"*", "", true},
		{"*", "anything.at.all", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"**", "x", true},
		{"", "", true},
		{"", "x", false},
		{"a*a*a", "aaaa", true},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.s); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func FuzzMatch(f *testing.F) {
	for _, seed := range [][2]string{{"alert.*", "alert.disk"}, {"*.x*", "a.xb"}, {"a*b", "ab"}, {"", ""}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, s string) {
		if !utf8.ValidString(pattern) || !utf8.ValidString(s) {
			t.Skip("the regexp oracle needs valid UTF-8")
		}
		parts := strings.Split(pattern, "*")
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		want := regexp.MustCompile(`^(?s:` + strings.Join(parts, ".*") + `)$`).MatchString(s)
		if got := Match(pattern, s); got != want {
			t.Errorf("Match(%q, %q) = %v, regexp says %v", pattern, s, got, want)
		}
	})
}

func TestLookup(t *testing.T) {
	var tbl Table[string]
	tbl.Add("order.*", "a", "b")
	tbl.Add("order.created", "c", "a")
	tbl.Add("alert.*", "d")

	if got, want := tbl.Lookup("order.created"), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup = %v, want %v", got, want)
	}
	if got := tbl.Lookup("user.signup"); got != nil {
		t.Errorf("Lookup(unrouted) = %v, want nil", got)
	}
}
