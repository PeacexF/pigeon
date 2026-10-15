package discord_test

import (
	"context"
	"log"
	"os"
	"text/template"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/discord"
)

func Example() {
	ctx := context.Background()

	p, err := pigeon.New(
		pigeon.WithChannel("ops", discord.New(os.Getenv("DISCORD_WEBHOOK_URL"), discord.WithUsername("pigeon"))),
		pigeon.WithRoute("alert.*", "ops"),
		pigeon.WithTemplateFuncs(template.FuncMap{"md": discord.EscapeMarkdown}),
		pigeon.WithTemplate("alert.*", "**{{md .Data.host}}**: {{md .Data.msg}}"),
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

// A title turns the message into a colored embed.
func Example_embed() {
	_, _ = pigeon.New(
		pigeon.WithChannel("ops", discord.New(os.Getenv("DISCORD_WEBHOOK_URL"), discord.WithColor(0xE74C3C))),
		pigeon.WithRoute("*", "ops"),
		pigeon.WithTemplate("*", "{{.Data.msg}}", pigeon.Title("🚨 {{.Type}}")),
	)
}
