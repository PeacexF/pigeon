# Writing a channel

A channel is anything with this method:

```go
type Channel interface {
    Send(ctx context.Context, msg *pigeon.Message) error
}
```

For one-offs, wrap a function: `pigeon.ChannelFunc(func(ctx, msg) error { ... })`.

## The contract

1. **One-line construction.** `mychan.New(required, args, opts...)` returns a ready channel. Use functional options for the rest, including `WithHTTPClient` and `WithBaseURL` so tests and self-hosted deployments can redirect it.
2. **Classify every failure.**
   - `pigeon.Permanent(err)`: retrying can't help (bad credentials, unknown recipient, malformed request, most 4xx).
   - `pigeon.RetryAfter(err, d)`: the provider said when to come back (429 with a retry hint). This also pauses the channel.
   - Plain error: retried with backoff (timeouts, 5xx, network errors).
3. **Never leak secrets.** Tokens often live in URLs, and `net/http` errors include the URL. In-tree adapters use `httpx.Post` / `httpx.PostJSON(..., secrets...)` or `httpx.Redact`.
4. **Honor `ctx`** on every network call. pigeon sets a per-attempt deadline and cancels it on shutdown.
5. **Cap response reads.** `httpx.Post` reads at most 1 MiB.
6. **Handle length limits predictably.** Truncate with `…` (or return `Permanent`) and document which.
7. **Use `msg.ID`** as the provider's idempotency key when the API has one. It is stable across retries.
8. **Treat `msg.Data` as read-only.** It is shared between channels.
9. **Be safe for concurrent use.** Several workers may call `Send` at once.

## Optional interfaces

| Method | Effect |
|---|---|
| `Validate() error` | Checked by `pigeon.New`, so misconfiguration fails at startup, not at the first send. |
| `DefaultRate() pigeon.Rate` | The provider's limit. Users override it with `pigeon.RateLimit` or remove it with `pigeon.NoRateLimit`. |
| `Close() error` | Called by `Gateway.Close`. |

## Tests

- Unit tests run against `httptest.Server`. No real network.
- Cover: request shape, success, 429 with retry hint, permanent 4xx, 5xx, network error (token not in message), cancelled context.
- A real-service test goes in `integration_test.go` behind `//go:build integration`, reads credentials from env vars, and skips when they're missing. Run it with `make test-integration`.
- Add a package doc and a runnable `Example`.

[`channel/telegram`](../channel/telegram) is the reference implementation, and [`channel/ntfy`](../channel/ntfy) is the smallest. In-tree adapters share `internal/httpx` (requests, classification, redaction) and `internal/truncate` (length limits), and test against `internal/fakehttp`.

## Dependencies

In-tree adapters (`channel/*`) use only the standard library. An adapter that needs a vendor SDK lives in `contrib/<name>/` with its own `go.mod`.
