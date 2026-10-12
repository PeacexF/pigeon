//go:build integration

package telegram_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/telegram"
)

// Run with: PIGEON_TELEGRAM_TOKEN=... PIGEON_TELEGRAM_CHAT_ID=... make test-integration
func TestIntegrationSend(t *testing.T) {
	token, chatID := os.Getenv("PIGEON_TELEGRAM_TOKEN"), os.Getenv("PIGEON_TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		t.Skip("PIGEON_TELEGRAM_TOKEN and PIGEON_TELEGRAM_CHAT_ID not set")
	}

	var delivered, dead int
	p, err := pigeon.New(
		pigeon.WithChannel("tg", telegram.New(token, chatID, telegram.WithSilent())),
		pigeon.WithRoute("*", "tg"),
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
