//go:build integration

package smtp_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/smtp"
)

// Run with: PIGEON_SMTP_ADDR=host:587 PIGEON_SMTP_FROM=... PIGEON_SMTP_TO=... PIGEON_SMTP_USER=... PIGEON_SMTP_PASSWORD=... make test-integration
func TestIntegrationSend(t *testing.T) {
	addr, from, to := os.Getenv("PIGEON_SMTP_ADDR"), os.Getenv("PIGEON_SMTP_FROM"), os.Getenv("PIGEON_SMTP_TO")
	if addr == "" || from == "" || to == "" {
		t.Skip("PIGEON_SMTP_ADDR, PIGEON_SMTP_FROM and PIGEON_SMTP_TO not set")
	}
	var opts []smtp.Option
	if user := os.Getenv("PIGEON_SMTP_USER"); user != "" {
		opts = append(opts, smtp.WithAuth(user, os.Getenv("PIGEON_SMTP_PASSWORD")))
	}
	if os.Getenv("PIGEON_SMTP_IMPLICIT_TLS") != "" {
		opts = append(opts, smtp.WithSecurity(smtp.ImplicitTLS))
	}
	ch := smtp.New(addr, from, []string{to}, opts...)
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
