package slack_test

import (
	"context"
	"log"
	"os"
	"text/template"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/slack"
)

func Example() {
	ctx := context.Background()

	p, err := pigeon.New(
		pigeon.WithChannel("ops", slack.New(os.Getenv("SLACK_WEBHOOK_URL"))),
		pigeon.WithRoute("alert.*", "ops"),
		pigeon.WithTemplateFuncs(template.FuncMap{"esc": slack.Escape}),
		pigeon.WithTemplate("alert.*", "*{{esc .Data.host}}*: {{esc .Data.msg}}", pigeon.Title("🚨 {{.Type}}")),
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

// A bot token can post to several channels with one app.
func ExampleNewBot() {
	token := os.Getenv("SLACK_BOT_TOKEN")

	_, _ = pigeon.New(
		pigeon.WithChannel("ops", slack.NewBot(token, "#ops")),
		pigeon.WithChannel("sales", slack.NewBot(token, "C0123456789")),
		pigeon.WithRoute("alert.*", "ops"),
		pigeon.WithRoute("order.*", "sales"),
	)
}
