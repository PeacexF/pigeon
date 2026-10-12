# Architecture

## Pipeline

```
               ┌──────────────────────── Send (caller's goroutine) ────────────────────────┐
  Event ──►  validate ──► route ──► dedupe ──► render × channel ──► queue.Put (all-or-nothing)
               │            │          │              │                    │
        ErrInvalidEvent ErrNoRoute ErrDuplicate  template error     blocks when full
               └────────────────────────────────────────────────────────────────────────────┘
                                                                             │
               ┌──────────────────────── workers (WithWorkers) ──────────────▼─────────────┐
               │  queue.Get ──► rate limiter ──► Channel.Send(ctx with attempt timeout)    │
               │                    │                     │                               │
               │          not yet: requeue later    ok ──► OnDelivered                    │
               │                                    Permanent / last attempt ──► OnDeadLetter
               │                                    other ──► backoff (RetryAfter: pause  │
               │                                              channel) ──► requeue later  │
               └───────────────────────────────────────────────────────────────────────────┘
```

Fan-out happens in `Send`: one job per (event, channel). Each channel retries independently, and template errors reach the caller instead of a log line.

## Layout

The root package is the public API only. Everything else is internal and tested on its own.

| Package | Responsibility |
|---|---|
| `pigeon` | `Gateway`, `Event`, `Message`, `Channel`, options, errors |
| `internal/route` | `*` pattern matching and the route table |
| `internal/render` | Template registry, precedence rules, fallback rendering |
| `internal/dedupe` | Idempotency-key window (FIFO, bounded) |
| `internal/memqueue` | Bounded in-memory queue with delayed re-delivery |
| `internal/ratelimit` | Non-blocking token bucket with pause |
| `internal/backoff` | Full-jitter exponential backoff |
| `internal/id` | UUIDv7 event IDs |
| `internal/httpx` | JSON POST, capped reads, status classification, secret redaction |
| `channel/*` | Adapters (stdlib only) |
| `pigeontest` | Test helpers for users |

## Queue

`internal/memqueue` counts **every unfinished job** against capacity: queued, waiting to retry, or in flight. As a result:

- retries never need new space, and workers never block when putting a job back;
- `Put` takes the whole batch or nothing, and concurrent batches are serialized so two partial batches can't deadlock each other;
- `Close` drains: it waits until the count hits zero or the context ends.

Delayed jobs (retries, rate-limited jobs) use timers rather than parked workers, so one slow or paused channel can't stall the others.

## Rate limiting

Each channel registration has its own token bucket (`ratelimit.Limiter`). A worker asks whether a token is available. If not, the job is rescheduled for when one will be, and the worker moves on. A `RetryAfter` error pauses the bucket, so every queued message for that channel waits rather than each one hitting the provider's 429 in turn.

## Shutdown

`Close(ctx)`: stop accepting → wait for the queue to drain or `ctx` to end → cancel in-flight attempts → stop timers → wait for workers → close channels that implement `io.Closer`. If `ctx` ended first, the error reports how many deliveries were dropped.

See [docs/adr](adr) for the reasoning behind these choices.
