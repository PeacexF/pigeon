package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/webhook"
)

func Example() {
	ctx := context.Background()

	p, err := pigeon.New(
		pigeon.WithChannel("billing", webhook.New("https://billing.example.com/hooks/pigeon",
			webhook.WithSecret(os.Getenv("WEBHOOK_SECRET")),
		)),
		pigeon.WithRoute("order.*", "billing"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = p.Close(ctx) }()

	_, _ = p.Send(ctx, pigeon.Event{
		Type: "order.created",
		Data: map[string]any{"order": 42, "total": 9.5},
	})
}

// A receiver checks the signature against the raw body before trusting it.
func ExampleVerify() {
	secret := os.Getenv("WEBHOOK_SECRET")

	http.HandleFunc("POST /hooks/pigeon", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if err := webhook.Verify(secret, r.Header.Get(webhook.HeaderSignature), body, 0); err != nil {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		var p webhook.Payload
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		// p.ID is the same on every retry: use it to skip duplicates.
		log.Printf("%s: %s", p.EventType, p.Text)
		w.WriteHeader(http.StatusNoContent)
	})
}
