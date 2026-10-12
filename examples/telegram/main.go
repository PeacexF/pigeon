// Command telegram sends one alert to a Telegram chat.
//
//	TELEGRAM_TOKEN=123:abc TELEGRAM_CHAT_ID=-1001234567890 go run ./examples/telegram
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"text/template"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/telegram"
)

func main() {
	ctx := context.Background()

	ops := telegram.New(
		os.Getenv("TELEGRAM_TOKEN"),
		os.Getenv("TELEGRAM_CHAT_ID"),
		telegram.WithParseMode(telegram.ParseModeHTML),
	)

	var failed bool
	p, err := pigeon.New(
		pigeon.WithChannel("ops", ops),
		pigeon.WithRoute("alert.*", "ops"),
		pigeon.WithTemplateFuncs(template.FuncMap{"html": telegram.EscapeHTML}),
		pigeon.WithTemplate("alert.*",
			"<b>{{html .Data.host}}</b>: {{html .Data.msg}}\n<i>{{.Time.Format \"15:04:05 MST\"}}</i>",
			pigeon.Title("🚨 {{html .Type}}"),
		),
		pigeon.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		pigeon.WithHooks(pigeon.Hooks{
			OnDeadLetter: func(context.Context, *pigeon.Message, error) { failed = true },
		}),
	)
	if err != nil {
		log.Fatal(err) // e.g. missing TELEGRAM_TOKEN
	}

	id, err := p.Send(ctx, pigeon.Event{
		Type: "alert.disk",
		Data: map[string]any{"host": "db1", "msg": "disk 95% full"},
	})
	if err != nil {
		log.Fatal(err)
	}
	slog.Info("queued", "event_id", id)

	shutdown, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := p.Close(shutdown); err != nil {
		log.Fatal(err)
	}
	if failed {
		os.Exit(1)
	}
	log.Print("delivered")
}
