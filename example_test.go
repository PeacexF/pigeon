package pigeon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/PeacexF/pigeon"
)

func Example() {
	ctx := context.Background()

	p, err := pigeon.New(
		pigeon.WithChannel("stdout", pigeon.Writer(os.Stdout)),
		pigeon.WithRoute("order.*", "stdout"),
		pigeon.WithTemplate("order.*", "Order #{{.Data.id}} from {{.Data.customer}}"),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	if _, err := p.Send(ctx, pigeon.Event{
		Type: "order.created",
		Data: map[string]any{"id": 42, "customer": "Ada"},
	}); err != nil {
		fmt.Println(err)
	}

	// Close waits for pending deliveries.
	if err := p.Close(ctx); err != nil {
		fmt.Println(err)
	}
	// Output: [order.created] Order #42 from Ada
}

func ExampleChannelFunc() {
	ctx := context.Background()

	audit := pigeon.ChannelFunc(func(_ context.Context, msg *pigeon.Message) error {
		fmt.Printf("audit %s: %s\n", msg.EventType, msg.Text)
		return nil
	})

	p, _ := pigeon.New(
		pigeon.WithChannel("audit", audit),
		pigeon.WithRoute("*", "audit"),
	)
	_, _ = p.Send(ctx, pigeon.Event{Type: "user.deleted", Data: map[string]any{"id": 7}})
	_ = p.Close(ctx)
	// Output:
	// audit user.deleted: user.deleted
	// id: 7
}

func ExampleGateway_Send_duplicate() {
	ctx := context.Background()
	p, _ := pigeon.New(
		pigeon.WithChannel("log", pigeon.Writer(os.Stdout)),
		pigeon.WithRoute("*", "log"),
	)

	first, _ := p.Send(ctx, pigeon.Event{Type: "invoice.paid", IdempotencyKey: "inv-1001"})
	again, err := p.Send(ctx, pigeon.Event{Type: "invoice.paid", IdempotencyKey: "inv-1001"})
	_ = p.Close(ctx)

	fmt.Println(errors.Is(err, pigeon.ErrDuplicate), again == first)
	// Output:
	// [invoice.paid] invoice.paid
	// true true
}

// Channels classify provider failures so pigeon knows whether and when to retry.
func ExamplePermanent() {
	classify := func(status int) error {
		err := fmt.Errorf("provider returned %d", status)
		switch {
		case status == 429:
			return pigeon.RetryAfter(err, 30*time.Second)
		case status >= 500:
			return err
		default:
			return pigeon.Permanent(err)
		}
	}
	for _, status := range []int{404, 429, 503} {
		err := classify(status)
		delay, _ := pigeon.RetryAfterDelay(err)
		fmt.Println(status, "permanent:", pigeon.IsPermanent(err), "retry after:", delay)
	}
	// Output:
	// 404 permanent: true retry after: 0s
	// 429 permanent: false retry after: 30s
	// 503 permanent: false retry after: 0s
}
