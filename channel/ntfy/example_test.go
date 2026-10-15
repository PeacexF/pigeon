package ntfy_test

import (
	"context"
	"log"
	"os"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/ntfy"
)

func Example() {
	ctx := context.Background()

	p, err := pigeon.New(
		pigeon.WithChannel("phone", ntfy.New("my-hard-to-guess-topic", ntfy.WithTags("rotating_light"))),
		pigeon.WithRoute("alert.*", "phone"),
		pigeon.WithTemplate("alert.*", "{{.Data.host}}: {{.Data.msg}}", pigeon.Title("{{.Type}}")),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = p.Close(ctx) }()

	_, _ = p.Send(ctx, pigeon.Event{
		Type: "alert.disk",
		Data: map[string]any{"host": "db1", "msg": "95% full"},
	})
}

// Register one topic twice to give critical events a higher priority.
func Example_priority() {
	server := ntfy.WithBaseURL("https://ntfy.example.com")
	auth := ntfy.WithToken(os.Getenv("NTFY_TOKEN"))

	_, _ = pigeon.New(
		pigeon.WithChannel("ops", ntfy.New("ops", server, auth)),
		pigeon.WithChannel("ops-urgent", ntfy.New("ops", server, auth, ntfy.WithPriority(ntfy.PriorityMax))),
		pigeon.WithRoute("*.warning", "ops"),
		pigeon.WithRoute("*.critical", "ops-urgent"),
	)
}
