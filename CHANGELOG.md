# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the version is `v0.x`, minor releases may contain breaking changes; each one is called out below.

## [Unreleased]

### Added

- `Gateway` with `New`, `Send` and `Close`: route events to channels, render them with templates, and deliver them in the background.
- Routing with `*` patterns (`WithRoute`); `Send` returns `ErrNoRoute` for unrouted events.
- `text/template` rendering (`WithTemplate`, `Title`, `For`, `WithTemplateFuncs`) with a default for events without a template.
- Retries with exponential backoff and full jitter (`RetryPolicy`, `WithRetry`, per-channel `Retry`), error classification with `Permanent` and `RetryAfter`.
- Per-channel rate limiting (`RateLimit`, `NoRateLimit`, adapter defaults via `DefaultRate`); `RetryAfter` pauses the channel.
- Idempotency keys: duplicates within `WithDedupeWindow` return `ErrDuplicate` and the original event ID.
- Bounded in-memory queue (`WithQueueSize`); `Send` blocks when full. `Close` drains pending deliveries.
- Observability through `Hooks` and `WithLogger`.
- Built-in channels `Writer` and `ChannelFunc`.
- `channel/telegram`: Bot API adapter with parse modes, escaping helpers, silent messages, forum topics, link-preview control, 4096-character truncation and 429 handling.
- `pigeontest.Recorder` for testing code that uses pigeon.
- Examples, architecture docs, channel-writing guide and ADRs.
- Go module `github.com/PeacexF/pigeon` (Go 1.27+).
- Development tooling: `Makefile` with repo-local pinned tools, `golangci-lint` config, GitHub Actions CI (fmt, tidy, vet, lint, race tests, govulncheck).
- Issue templates for bugs, feature requests and new channel requests.
