package smtp_test

import (
	"context"
	"html/template"
	"log"
	"os"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/channel/smtp"
)

func Example() {
	ctx := context.Background()

	mail := smtp.New("smtp.example.com:587", "Pigeon <alerts@example.com>", []string{"ops@example.com"},
		smtp.WithAuth("alerts@example.com", os.Getenv("SMTP_PASSWORD")),
	)
	p, err := pigeon.New(
		pigeon.WithChannel("mail", mail),
		pigeon.WithRoute("alert.*", "mail"),
		pigeon.WithTemplate("alert.*", "{{.Data.host}}: {{.Data.msg}}", pigeon.Title("[alert] {{.Data.host}}")),
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

// WithHTML adds an HTML version next to the plain text.
func ExampleWithHTML() {
	html := template.Must(template.New("mail").Parse(`<h2>{{.Title}}</h2><p>{{.Text}}</p>`))

	_ = smtp.New("smtp.example.com:465", "alerts@example.com", []string{"ops@example.com"},
		smtp.WithSecurity(smtp.ImplicitTLS),
		smtp.WithAuth("alerts@example.com", os.Getenv("SMTP_PASSWORD")),
		smtp.WithHTML(html),
	)
}
