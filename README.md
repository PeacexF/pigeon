# pigeon

[![CI](https://github.com/PeacexF/pigeon/actions/workflows/ci.yml/badge.svg)](https://github.com/PeacexF/pigeon/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/PeacexF/pigeon.svg)](https://pkg.go.dev/github.com/PeacexF/pigeon)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A notification gate for Go. Your application emits events; pigeon decides which channels hear about them, renders the message, and delivers it with retries, rate limiting and deduplication.

> [!WARNING]
> **Pre-release.** The API may change until `v1.0.0`.

## Install

```sh
go get github.com/PeacexF/pigeon
```

Requires Go 1.27+. The core module has no third-party dependencies.

## Quick start

```go
p, err := pigeon.New(
    pigeon.WithChannel("ops", telegram.New(token, chatID)),
    pigeon.WithChannel("log", pigeon.Writer(os.Stderr)),
    pigeon.WithRoute("alert.*", "ops", "log"),
    pigeon.WithTemplate("alert.*", "🚨 {{.Data.host}}: {{.Data.msg}}"),
)
if err != nil {
    log.Fatal(err) // every config problem, reported at once
}
defer p.Close(ctx) // waits for pending deliveries

id, err := p.Send(ctx, pigeon.Event{
    Type:           "alert.disk",
    Data:           map[string]any{"host": "db1", "msg": "95% full"},
    IdempotencyKey: "disk-db1-2026-10-12",
})
```

Runnable programs: [`examples/basic`](examples/basic) (no network) and [`examples/telegram`](examples/telegram).

## Concepts

| | |
|---|---|
| **Event** | What happened: a dotted `Type`, a `Data` map, an optional `IdempotencyKey`. |
| **Channel** | A named destination, e.g. `"ops"` → one Telegram chat. Anything implementing `Send(ctx, *Message) error`. |
| **Route** | `pattern → channels`. `*` matches anything, dots included: `alert.*`, `*.critical`, `*`. An event goes to the union of every matching route. |
| **Template** | A `text/template` per pattern, optionally per channel (`pigeon.For("ops")`), with an optional `pigeon.Title(...)`. Unmatched events render as their type plus `key: value` lines. |

`Send` fails fast and explicitly:

| Error | When |
|---|---|
| `ErrInvalidEvent` | The event has no `Type`. |
| `ErrNoRoute` | No route matches the type. |
| template error | A template references missing data. |
| `ErrDuplicate` | The `IdempotencyKey` was used within the dedupe window (default 24h). The original event ID is returned with it. |
| `ErrClosed` | The gateway is closed. |
| `ctx.Err()` | The queue was full and `ctx` ended while waiting. |

## Delivery guarantees

- **At-least-once.** If a provider accepts a message but the response is lost, pigeon retries and the message may arrive twice. `Message.ID` stays the same across retries, so channels whose provider supports idempotency keys can use it to drop duplicates on the provider's side.
- **Retries** use exponential backoff with full jitter (default: 5 attempts, 1s base, 5m cap). Channels mark failures `pigeon.Permanent` (never retried) or `pigeon.RetryAfter` (wait at least this long, and pause the whole channel). Anything else is retried.
- **Rate limits** apply per channel. Adapters set defaults from the provider's documented limits (see [Channels](#channels)). Override with `pigeon.RateLimit(n, per)`.
- **Backpressure:** the queue is bounded (`WithQueueSize`, default 1024), and `Send` blocks when it's full.
- **The queue is in memory.** Deliveries still pending when the process exits without `Close` are lost. A durable Postgres queue is planned.

Observe outcomes with `pigeon.WithHooks` (`OnAttempt`, `OnDelivered`, `OnDeadLetter`, ...) or `pigeon.WithLogger(*slog.Logger)`.

## Channels

Every adapter lives in its own package under [`channel/`](channel) and uses only the standard library.

| Channel | Constructor | Title becomes | Formatting | Length limit (cut with `…`) | Provider-side dedupe | Default rate |
|---|---|---|---|---|---|---|
| [Telegram](channel/telegram) | `telegram.New(token, chatID)` | first paragraph | plain text; HTML or MarkdownV2 opt-in | 4096 | — | 1/s |
| [Discord](channel/discord) | `discord.New(webhookURL)` | embed title | Markdown | 2000; embed 256 + 4096 | — | 30/min, plus Discord's bucket headers |
| [Slack](channel/slack) | `slack.New(webhookURL)`, `slack.NewBot(token, channel)` | header block | mrkdwn | 40,000; header 150, section 3000 | — | 1/s |
| [Webhook](channel/webhook) | `webhook.New(url)` | `title` field | JSON payload | none | `Idempotency-Key` header | none |
| [SMTP](channel/smtp) | `smtp.New(addr, from, to)` | subject | plain text, optional HTML part | subject 250 | stable `Message-ID` | none |
| [ntfy](channel/ntfy) | `ntfy.New(topic)` | notification title | plain text; Markdown opt-in | 4096 bytes | — | ntfy.sh: burst 60, then 1 per 5s |
| Any `io.Writer` | `pigeon.Writer(w)` | first line | plain text | none | — | none |
| Your own function | `pigeon.ChannelFunc(f)` | up to you | | | | none |

Each package's documentation covers setup and options. Where the provider renders markup, escape event data in templates with the package's helper, registered through `pigeon.WithTemplateFuncs`: `telegram.EscapeHTML`, `telegram.EscapeMarkdownV2`, `discord.EscapeMarkdown`, `slack.Escape`. Discord doesn't notify `@everyone` or other mentions unless you set `discord.WithMentions()`.

The webhook channel refuses to connect to loopback, private and cloud-metadata addresses unless you set `webhook.WithAllowPrivate()`. It doesn't follow redirects, and it signs requests with `webhook.WithSecret`; receivers check the signature with `webhook.Verify`. See [ADR 4](docs/adr/0004-webhook-ssrf.md).

### Telegram setup

1. Message [@BotFather](https://t.me/BotFather), run `/newbot`, and copy the token.
2. Add the bot to your group or channel (as an admin for channels).
3. Get the chat ID: send a message in the chat, then open `https://api.telegram.org/bot<token>/getUpdates` and read `chat.id`. Group and channel IDs look like `-1001234567890`. Public channels can also use `"@channelname"`.

```go
telegram.New(token, chatID,
    telegram.WithParseMode(telegram.ParseModeHTML), // default: plain text
    telegram.WithSilent(),
)
```

With a parse mode set, escape event data with `telegram.EscapeHTML` or `telegram.EscapeMarkdownV2` via `pigeon.WithTemplateFuncs`.

## Testing your code

`pigeontest.Recorder` is a channel that records messages and can be told to fail:

```go
rec := &pigeontest.Recorder{}
rec.FailNext(2, errors.New("boom"))
p, _ := pigeon.New(pigeon.WithChannel("ops", rec), pigeon.WithRoute("*", "ops"))
p.Send(ctx, pigeon.Event{Type: "x"})
p.Close(ctx)
// rec.Messages(), rec.Attempts() == 3
```

## Writing a channel

Implement `pigeon.Channel` (or wrap a function with `pigeon.ChannelFunc`). See [docs/writing-a-channel.md](docs/writing-a-channel.md).

## Documentation

- [Architecture](docs/architecture.md)
- [Writing a channel](docs/writing-a-channel.md)
- [Design decisions](docs/adr)

## Status

| | |
|---|---|
| Core library + Telegram | available (unreleased) |
| Discord, Slack, webhook, SMTP, ntfy | available (unreleased) |
| Postgres queue, dead-letter replay | planned |
| Prometheus / OpenTelemetry | planned |
| `pigeond` HTTP gateway server | planned |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
