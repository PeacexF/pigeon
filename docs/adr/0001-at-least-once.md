# 1. At-least-once delivery

**Status:** accepted

## Context

pigeon delivers through external APIs (Telegram, email, webhooks) that pigeon doesn't control. A request can succeed on the provider's side while the response is lost to a timeout, a reset connection, or a crash. From pigeon's side, that looks exactly like a failure.

## Decision

pigeon guarantees **at-least-once** delivery and makes duplicates rare and avoidable, not impossible:

- **Idempotency keys** (`Event.IdempotencyKey`) stop the *caller* from creating duplicates, for example on a retried HTTP request or a re-run cron job. A repeat within the window returns `ErrDuplicate` and the original event ID.
- **Stable message IDs** (`Message.ID`, unique per event and channel, unchanged across retries) let adapters pass an idempotency key to providers that support one (Matrix `txnId`, PagerDuty `dedup_key`, `Idempotency-Key` headers).
- Retries happen only on failures that might succeed later (see ADR 2).

## Consequences

- Recipients may occasionally see a message twice. For notifications, a duplicate is far cheaper than a silent loss.
- Exactly-once is not claimed anywhere in the docs.
- The in-memory queue loses pending deliveries if the process dies, so "at-least-once" holds only while the process runs. A durable queue (Postgres, planned) extends it across restarts.
