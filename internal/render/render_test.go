package render

import (
	"strings"
	"testing"
)

func mustAdd(t *testing.T, r *Registry, spec Spec) {
	t.Helper()
	if err := r.Add(spec, nil); err != nil {
		t.Fatalf("Add(%q): %v", spec.Pattern, err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	var r Registry
	mustAdd(t, &r, Spec{Pattern: "*", Text: "catch-all"})
	mustAdd(t, &r, Spec{Pattern: "order.*", Text: "orders"})
	mustAdd(t, &r, Spec{Pattern: "order.cr*", Text: "longer wildcard"})
	mustAdd(t, &r, Spec{Pattern: "order.created", Text: "exact"})
	mustAdd(t, &r, Spec{Pattern: "*", Text: "scoped", Channels: []string{"vip"}})
	mustAdd(t, &r, Spec{Pattern: "order.*", Text: "orders again"})

	tests := []struct{ eventType, channel, want string }{
		{"order.created", "vip", "scoped"},
		{"order.created", "ops", "exact"},
		{"order.creditnote", "ops", "longer wildcard"},
		{"order.shipped", "ops", "orders"},
		{"user.signup", "ops", "catch-all"},
	}
	for _, tt := range tests {
		_, got, err := r.Render(Input{Type: tt.eventType, Channel: tt.channel})
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("Render(%q, %q) = %q, want %q", tt.eventType, tt.channel, got, tt.want)
		}
	}
}

func TestRenderTitleAndData(t *testing.T) {
	var r Registry
	mustAdd(t, &r, Spec{Pattern: "x", Text: "{{.Data.n}} on {{.Channel}}", Title: new("{{.Type}}")})

	title, text, err := r.Render(Input{Type: "x", Channel: "c", Data: map[string]any{"n": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if title != "x" || text != "3 on c" {
		t.Errorf("Render = %q, %q", title, text)
	}
}

func TestRenderMissingKey(t *testing.T) {
	var r Registry
	mustAdd(t, &r, Spec{Pattern: "x", Text: "{{.Data.missing}}"})
	mustAdd(t, &r, Spec{Pattern: "y", Text: "ok", Title: new("{{.Data.missing}}")})

	for _, typ := range []string{"x", "y"} {
		if _, _, err := r.Render(Input{Type: typ, Data: map[string]any{}}); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Errorf("Render(%q) err = %v, want missing key error", typ, err)
		}
	}
}

func TestRenderFallback(t *testing.T) {
	var r Registry
	_, text, err := r.Render(Input{Type: "user.signup", Data: map[string]any{"z": 1, "a": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "user.signup\na: b\nz: 1"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestAddRejectsBadTemplates(t *testing.T) {
	var r Registry
	if err := r.Add(Spec{Pattern: "x", Text: "{{"}, nil); err == nil {
		t.Error("bad text accepted")
	}
	if err := r.Add(Spec{Pattern: "x", Text: "ok", Title: new("{{")}, nil); err == nil {
		t.Error("bad title accepted")
	}
	if err := r.Add(Spec{Pattern: "x", Text: "{{nope}}"}, nil); err == nil {
		t.Error("unknown func accepted")
	}
}

func FuzzRender(f *testing.F) {
	f.Add("{{.Data.v}}", "hello")
	f.Add("{{.Type}}: {{.Data.v | printf \"%q\"}}", "<b>&</b>")
	f.Fuzz(func(_ *testing.T, text, value string) {
		var r Registry
		if err := r.Add(Spec{Pattern: "*", Text: text}, nil); err != nil {
			return
		}
		_, _, _ = r.Render(Input{Type: "x", Data: map[string]any{"v": value}})
	})
}
