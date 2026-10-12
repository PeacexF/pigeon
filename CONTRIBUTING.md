# Contributing

> Thanks for your interest in contributing to **pigeon**.

Contributions of all kinds are welcome: bug fixes, new channels, documentation, tests, ideas.

## Getting Started

You need **Go 1.27+** and `make`.

```sh
git clone https://github.com/PeacexF/pigeon.git
cd pigeon
make tools   # installs pinned golangci-lint and govulncheck into ./bin
make check   # what CI runs: fmt, tidy, vet, lint, tests with -race
```

Run `make help` to see every target. The most useful ones:

| Target | What it does |
|---|---|
| `make fmt` | Format all code |
| `make check` | Everything CI checks (run before opening a PR) |
| `make test` | Root module tests with the race detector |
| `make test-all` | Tests for every module in the repo |
| `make test-integration` | Tests against real services. Needs credentials in env vars, and is never run in CI for PRs |
| `make cover` | Coverage report |
| `make vuln` | `govulncheck` on every module |

## Project Layout

```
pigeon/            core library: stdlib only, zero third-party dependencies
├── channel/       built-in channel adapters (telegram, ...): stdlib only
├── pigeontest/    helpers for testing code that uses pigeon
├── internal/      shared plumbing, not public API
├── contrib/       integrations with third-party deps: each is its own Go module
├── cmd/pigeond/   the HTTP gateway server: its own Go module
└── docs/
```

**Dependency rule:** the root module must not import any third-party package. If an integration needs one (a database driver, a vendor SDK, a metrics library), it goes in `contrib/<name>/` with its own `go.mod`.

## Adding a Channel

Open a **New channel** issue first so we can agree on scope. A channel adapter must:

1. Implement `pigeon.Channel` and be constructible in one line (`telegram.New(token, chatID, opts...)`).
2. Classify errors: `pigeon.Permanent` for failures that won't succeed on retry (bad credentials, unknown recipient), `pigeon.RetryAfter` for rate limits, plain errors otherwise.
3. Never include secrets (tokens, secret URLs) in errors or logs.
4. Honor `ctx` on every network call and cap response body reads.
5. Be tested against `httptest.Server`. Unit tests never touch the network. Real-service tests go behind `//go:build integration`.
6. Ship a package doc and a runnable `Example`.

A full guide will live in `docs/writing-a-channel.md`.

## Code Style

- `make fmt` and `make lint` must pass. The linter config is in `.golangci.yml`.
- Every exported identifier has a doc comment.
- Prefer table-driven tests. Use `testing/synctest` for anything involving time (retries, backoff, rate limits) instead of real sleeps.
- Keep the public API small. New exported names need a reason.

## Commits and Pull Requests

- Keep PRs focused: one change, with tests and docs for that change.
- Write commit subjects in the imperative mood, prefixed with the affected area when useful:
  `telegram: retry on 429 using retry_after`, `core: return ErrNoRoute for unrouted events`.
- Explain **what changed and why** in the PR description.
- User-visible changes get a line in [CHANGELOG.md](CHANGELOG.md) under *Unreleased*.
- For larger changes, open an issue first so the approach can be discussed before implementation.

Thanks for helping improve the project.
