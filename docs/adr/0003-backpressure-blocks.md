# 3. A full queue blocks Send

**Status:** accepted

## Context

When events arrive faster than channels accept them (an alert storm, a provider outage), the queue fills. The options are: grow without bound, reject (`ErrQueueFull`), shed low-priority events, or block the producer.

## Decision

The queue is bounded (`WithQueueSize`, default 1024), and **`Send` blocks until there is room or its `ctx` is done**, then returns `ctx.Err()`.

- Capacity counts queued, retrying *and* in-flight deliveries, so the bound is real memory, not just the waiting line.
- A multi-channel event is enqueued all-or-nothing, so no event is half-delivered because the queue filled midway.

## Why not the alternatives

- **Unbounded:** turns a provider outage into an out-of-memory crash.
- **Fail fast:** every caller then needs its own retry or drop logic. Callers who want fail-fast can pass a short `ctx` deadline and get the same behavior.
- **Priority shedding:** useful, but needs a priority model. It's tracked as an open question, not a v0.1 feature.

## Consequences

- A slow channel can slow down `Send` for every channel once the queue is full. Per-channel caps are a possible future option.
- Callers in request handlers should pass the request's context so a stuck pipeline can't hang them.
