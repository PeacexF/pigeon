// Package render picks and executes the template for an event and channel.
package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/PeacexF/pigeon/internal/route"
)

// Spec describes one template before parsing.
type Spec struct {
	Pattern  string
	Text     string
	Title    *string
	Channels []string // empty means every channel
}

// Input is what templates see as dot.
type Input struct {
	ID      string
	Type    string
	Channel string
	Time    time.Time
	Data    map[string]any
}

// Registry holds parsed templates. The zero value is an empty registry.
type Registry struct {
	entries []entry
}

type entry struct {
	pattern  string
	channels []string
	text     *template.Template
	title    *template.Template
	exact    bool
	literals int
}

// Add parses spec. Missing map keys are execution errors, not "<no value>".
func (r *Registry) Add(spec Spec, funcs template.FuncMap) error {
	e := entry{
		pattern:  spec.Pattern,
		channels: spec.Channels,
		exact:    !strings.Contains(spec.Pattern, "*"),
		literals: len(spec.Pattern) - strings.Count(spec.Pattern, "*"),
	}
	var err error
	if e.text, err = parse(spec.Pattern, spec.Text, funcs); err != nil {
		return err
	}
	if spec.Title != nil {
		if e.title, err = parse(spec.Pattern+" (title)", *spec.Title, funcs); err != nil {
			return err
		}
	}
	r.entries = append(r.entries, e)
	return nil
}

func parse(name, text string, funcs template.FuncMap) (*template.Template, error) {
	return template.New(name).Option("missingkey=error").Funcs(funcs).Parse(text)
}

// Render executes the best template for in.Type and in.Channel. Without a
// matching template, the text is the type followed by sorted "key: value" lines.
func (r *Registry) Render(in Input) (title, text string, err error) {
	e := r.resolve(in.Type, in.Channel)
	if e == nil {
		return "", fallback(in.Type, in.Data), nil
	}
	if text, err = execute(e.text, in); err != nil {
		return "", "", err
	}
	if e.title != nil {
		if title, err = execute(e.title, in); err != nil {
			return "", "", err
		}
	}
	return title, text, nil
}

// resolve ranks matches: channel-scoped first, then exact patterns, then the
// most literal characters. Ties go to the earlier registration.
func (r *Registry) resolve(eventType, channel string) *entry {
	var best *entry
	for i := range r.entries {
		e := &r.entries[i]
		if !route.Match(e.pattern, eventType) || (len(e.channels) > 0 && !slices.Contains(e.channels, channel)) {
			continue
		}
		if best == nil || e.beats(best) {
			best = e
		}
	}
	return best
}

func (e *entry) beats(o *entry) bool {
	if a, b := len(e.channels) > 0, len(o.channels) > 0; a != b {
		return a
	}
	if e.exact != o.exact {
		return e.exact
	}
	return e.literals > o.literals
}

func execute(t *template.Template, in Input) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, in); err != nil {
		return "", err
	}
	return b.String(), nil
}

func fallback(eventType string, data map[string]any) string {
	var b strings.Builder
	b.WriteString(eventType)
	for _, k := range slices.Sorted(maps.Keys(data)) {
		fmt.Fprintf(&b, "\n%s: %v", k, data[k])
	}
	return b.String()
}
