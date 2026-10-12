# 2. Adapters classify errors, the core retries

**Status:** accepted

## Context

Retrying a message to a nonexistent Telegram chat wastes requests, delays the dead-letter signal, and can get a bot rate-limited. Not retrying a timeout loses a message that would have gone through a second later. Only the adapter knows the provider's error semantics. The retry machinery (backoff, attempt counting, dead letters) should exist once.

## Decision

- Adapters wrap errors: `pigeon.Permanent(err)` means never retry. `pigeon.RetryAfter(err, d)` means retry no sooner than `d`. Anything else is retryable.
- **Unclassified errors are retryable.** Network errors and timeouts are unclassified by nature, and a wrongly retried permanent error costs a few requests, while a wrongly dropped transient one costs a message.
- Backoff is exponential with **full jitter** (`rand[0, min(cap, base·2ⁿ⁻¹)]`), so many failing deliveries don't retry in lockstep.
- `RetryAfter` also **pauses the whole channel**: a 429 is about the bot or chat, not the one message.
- Errors from the attempt being cut short by shutdown don't count as attempts.

## Consequences

- An adapter that forgets to classify a 4xx retries it up to `MaxAttempts` times. That's annoying but safe, and the adapter test checklist covers it.
- Policies are per gateway with per-channel overrides (`pigeon.Retry`).
