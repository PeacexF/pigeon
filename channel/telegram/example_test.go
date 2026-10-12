package telegram_test

import (
	"context"
	"log"
	"os"
	"text/template"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/telegram"
)

func Example() {
	ctx := context.Background()

	p, err := pigeon.New(
		pigeon.WithChannel("ops", telegram.New(os.Getenv("TELEGRAM_TOKEN"), "-1001234567890")),
		pigeon.WithRoute("alert.*", "ops"),
		pigeon.WithTemplate("alert.*", "🚨 {{.Data.host}}: {{.Data.msg}}"),
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

// With a parse mode, escape event data so it can't break the markup.
func Example_html() {
	ch := telegram.New(os.Getenv("TELEGRAM_TOKEN"), "@my_channel", telegram.WithParseMode(telegram.ParseModeHTML))

	_, _ = pigeon.New(
		pigeon.WithChannel("news", ch),
		pigeon.WithRoute("release.*", "news"),
		pigeon.WithTemplateFuncs(template.FuncMap{"html": telegram.EscapeHTML}),
		pigeon.WithTemplate("release.*", "<b>{{html .Data.version}}</b> is out: {{html .Data.notes}}"),
	)
}
