//go:build integration

package ntfy_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/ntfy"
)

// Run with: PIGEON_NTFY_TOPIC=... [PIGEON_NTFY_URL=... PIGEON_NTFY_TOKEN=...] make test-integration
func TestIntegrationSend(t *testing.T) {
	topic := os.Getenv("PIGEON_NTFY_TOPIC")
	if topic == "" {
		t.Skip("PIGEON_NTFY_TOPIC not set")
	}
	opts := []ntfy.Option{ntfy.WithPriority(ntfy.PriorityLow)}
	if u := os.Getenv("PIGEON_NTFY_URL"); u != "" {
		opts = append(opts, ntfy.WithBaseURL(u))
	}
	if tok := os.Getenv("PIGEON_NTFY_TOKEN"); tok != "" {
		opts = append(opts, ntfy.WithToken(tok))
	}
	ch := ntfy.New(topic, opts...)
	var delivered, dead int
	p, err := pigeon.New(
		pigeon.WithChannel("it", ch),
		pigeon.WithRoute("*", "it"),
		pigeon.WithTemplate("*", "pigeon integration test at {{.Time.Format \"15:04:05\"}}", pigeon.Title("✅ {{.Type}}")),
		pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second}),
		pigeon.WithHooks(pigeon.Hooks{
			OnDelivered:  func(_ context.Context, _ *pigeon.Message) { delivered++ },
			OnDeadLetter: func(_ context.Context, _ *pigeon.Message, err error) { dead++; t.Log(err) },
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Send(t.Context(), pigeon.Event{Type: "test.integration"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 || dead != 0 {
		t.Errorf("delivered %d, dead %d", delivered, dead)
	}
}
