// Command basic shows routing, templates, retries and hooks without any network access.
//
//	go run ./examples/basic
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/PeacexF/pigeon"
)

func main() {
	ctx := context.Background()

	// Fails twice, then succeeds, to show retries.
	calls := 0
	flaky := pigeon.ChannelFunc(func(_ context.Context, msg *pigeon.Message) error {
		calls++
		if calls <= 2 {
			return errors.New("upstream timeout")
		}
		fmt.Printf("pager  <- %s (attempt %d)\n", msg.Text, msg.Attempt)
		return nil
	})

	p, err := pigeon.New(
		pigeon.WithChannel("console", pigeon.Writer(os.Stdout)),
		pigeon.WithChannel("pager", flaky),

		pigeon.WithRoute("order.*", "console"),
		pigeon.WithRoute("alert.*", "console", "pager"),

		pigeon.WithTemplate("order.created", "🛒 order #{{.Data.id}}: {{.Data.total}}"),
		pigeon.WithTemplate("alert.*", "{{.Data.host}} — {{.Data.msg}}", pigeon.Title("🚨 {{.Type}}")),

		pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}),
		pigeon.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		pigeon.WithHooks(pigeon.Hooks{
			OnDeadLetter: func(_ context.Context, msg *pigeon.Message, err error) {
				log.Printf("gave up on %s: %v", msg.ID, err)
			},
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	send := func(e pigeon.Event) {
		if _, err := p.Send(ctx, e); err != nil {
			log.Printf("send %s: %v", e.Type, err)
		}
	}
	send(pigeon.Event{Type: "order.created", Data: map[string]any{"id": 1001, "total": "€42.00"}})
	send(pigeon.Event{Type: "alert.disk", Data: map[string]any{"host": "db1", "msg": "95% full"}})
	send(pigeon.Event{Type: "user.signup"})                                                          // ErrNoRoute
	send(pigeon.Event{Type: "order.created", Data: map[string]any{"id": 1002}, IdempotencyKey: "o"}) // missing .total → template error
	send(pigeon.Event{Type: "order.created", Data: map[string]any{"id": 1, "total": "€1"}, IdempotencyKey: "same"})
	send(pigeon.Event{Type: "order.created", Data: map[string]any{"id": 1, "total": "€1"}, IdempotencyKey: "same"}) // ErrDuplicate

	shutdown, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := p.Close(shutdown); err != nil {
		log.Fatal(err)
	}
}
