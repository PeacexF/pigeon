//go:build integration

package discord_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/discord"
)

// Run with: PIGEON_DISCORD_WEBHOOK_URL=... make test-integration
func TestIntegrationSend(t *testing.T) {
	url := os.Getenv("PIGEON_DISCORD_WEBHOOK_URL")
	if url == "" {
		t.Skip("PIGEON_DISCORD_WEBHOOK_URL not set")
	}
	ch := discord.New(url, discord.WithSilent())
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
