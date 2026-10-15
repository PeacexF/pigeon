//go:build integration

package slack_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/slack"
)

// Run with: PIGEON_SLACK_WEBHOOK_URL=... (or PIGEON_SLACK_BOT_TOKEN=... PIGEON_SLACK_CHANNEL=...) make test-integration
func TestIntegrationSend(t *testing.T) {
	var ch *slack.Channel
	switch url, token := os.Getenv("PIGEON_SLACK_WEBHOOK_URL"), os.Getenv("PIGEON_SLACK_BOT_TOKEN"); {
	case url != "":
		ch = slack.New(url)
	case token != "":
		ch = slack.NewBot(token, os.Getenv("PIGEON_SLACK_CHANNEL"))
	default:
		t.Skip("PIGEON_SLACK_WEBHOOK_URL or PIGEON_SLACK_BOT_TOKEN not set")
	}
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
