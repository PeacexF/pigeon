# pigeon

[![CI](https://github.com/PeacexF/pigeon/actions/workflows/ci.yml/badge.svg)](https://github.com/PeacexF/pigeon/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/PeacexF/pigeon.svg)](https://pkg.go.dev/github.com/PeacexF/pigeon)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A notification gate for Go. Your application emits events; pigeon decides which channels hear about them, renders the message, and delivers it with retries, rate limiting and deduplication.

> [!WARNING]
> **Not ready for use.** pigeon is in early development and has no released version yet. The API below is the design target and will change.

## The idea

```go
p, err := pigeon.New(
    pigeon.WithChannel("ops", telegram.New(token, chatID)),
    pigeon.WithChannel("log", pigeon.Writer(os.Stderr)),
    pigeon.WithRoute("alert.*", "ops", "log"),
    pigeon.WithTemplate("alert.*", "🚨 {{.Data.host}}: {{.Data.msg}}"),
)
if err != nil {
    log.Fatal(err)
}
defer p.Close(ctx)

id, err := p.Send(ctx, pigeon.Event{
    Type:           "alert.disk",
    Data:           map[string]any{"host": "db1", "msg": "95% full"},
    IdempotencyKey: "disk-db1-2026-10-12",
})
```

- **Events, not messages.** Apps say *what happened*; routes decide *who hears about it*.
- **Any destination.** A channel is a one-method interface. Telegram first; Discord, Slack, webhooks, email and more to follow.
- **Reliable by default.** Retries with backoff and jitter, permanent-vs-retryable errors, per-channel rate limits, idempotency keys, dead-letter hooks.
- **Zero dependencies.** The core module uses only the standard library. Integrations that need third-party packages (Postgres queue, Prometheus, ...) live in separate modules.
- **Durable when you need it.** Start in-memory; switch to a Postgres-backed queue without changing calling code.

## Status

| | |
|---|---|
| Core library + Telegram | 🚧 in progress |
| Discord, Slack, webhook, SMTP, ntfy | planned |
| Postgres queue, dead-letter replay | planned |
| Prometheus / OpenTelemetry | planned |
| `pigeond` HTTP gateway server | planned |

## Requirements

Go 1.27 or newer.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
