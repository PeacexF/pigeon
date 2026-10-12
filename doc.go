// Package pigeon is a notification gate. Applications send typed events;
// pigeon routes each one to the channels that should hear about it (Telegram,
// webhooks, email, ...), renders the message from a template, and delivers it
// in the background with retries, rate limiting and deduplication.
//
//	p, err := pigeon.New(
//		pigeon.WithChannel("ops", telegram.New(token, chatID)),
//		pigeon.WithChannel("log", pigeon.Writer(os.Stderr)),
//		pigeon.WithRoute("alert.*", "ops", "log"),
//		pigeon.WithTemplate("alert.*", "🚨 {{.Data.host}}: {{.Data.msg}}"),
//	)
//	if err != nil { ... }
//	defer p.Close(ctx)
//
//	id, err := p.Send(ctx, pigeon.Event{
//		Type: "alert.disk",
//		Data: map[string]any{"host": "db1", "msg": "95% full"},
//	})
//
// # Delivery
//
// Send returns once deliveries are queued. Workers then call each channel's
// Send. Errors marked with Permanent go straight to Hooks.OnDeadLetter. Other
// errors are retried with exponential backoff and full jitter (see
// RetryPolicy). An error from RetryAfter delays the retry and pauses the
// whole channel. Every channel has its own rate limit (see RateLimit and
// Rate).
//
// Delivery is at-least-once: a message can arrive twice if a provider
// accepts it but the response is lost. Message.ID is stable across retries,
// so channels whose provider supports idempotency keys can use it to drop
// duplicates on the provider's side. The queue is in memory, so deliveries
// still pending when the process exits without Close are lost.
//
// # Writing a channel
//
// Implement Channel, or wrap a function with ChannelFunc. See the Channel
// documentation for the optional interfaces New recognizes.
package pigeon
