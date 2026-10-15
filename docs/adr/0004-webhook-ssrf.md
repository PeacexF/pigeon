# 4. The webhook channel refuses internal addresses at dial time

**Status:** accepted

## Context

`channel/webhook` POSTs to any URL. Once that URL comes from configuration someone else controls, such as a `pigeond` tenant, a settings page, or a config file in a shared repo, the gateway becomes a server-side request forgery (SSRF) tool. Anyone who can set the URL can reach cloud metadata endpoints (`169.254.169.254`), admin interfaces on `localhost`, and services on the private network, and read side effects from the delivery outcome.

Checking the URL when the channel is configured is not enough:

- A hostname can resolve to a public address at validation time and an internal one at send time (DNS rebinding).
- Redirects can point anywhere.
- IPv6 has several ways to embed an IPv4 address (`::ffff:127.0.0.1`, NAT64 `64:ff9b::7f00:1`, 6to4 `2002:7f00:1::`).

## Decision

- The default HTTP client checks the **resolved address of every connection** in the dialer's `Control` hook (`internal/ssrf`). It refuses loopback, private (RFC 1918, ULA), link-local, carrier-grade NAT, unspecified, multicast, reserved, documentation and benchmarking ranges, plus addresses embedded in NAT64, 6to4, Teredo and IPv4-mapped/compatible IPv6.
- **Redirects are not followed.** A 3xx response fails the delivery permanently. Following them would need a re-check on every hop, and would also turn the `POST` into a `GET` on 301/302/303. GitHub and Stripe webhooks don't follow redirects either.
- The default client **ignores proxy environment variables**. Through a proxy, the dialer would check the proxy's address rather than the receiver's.
- A refused connection is **permanent**: retrying can't change where a URL points without a config change.
- `Validate` rejects literal non-public IPs in the URL, so the obvious mistakes fail at startup.
- Opt-outs:
  - `WithAllowPrivate()` turns the check off, for receivers on your own network.
  - `WithHTTPClient(c)` uses the caller's client as is, so the check and the redirect policy become the caller's responsibility. Both are documented.

Outgoing requests can also be authenticated: `WithSecret` adds `Pigeon-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<body>">`, and `webhook.Verify` checks it with a replay window (default 5 minutes). Every request carries `Idempotency-Key: <Message.ID>` so receivers can drop retried duplicates (ADR 1).

## Consequences

- Webhooks to services on the same host or network need `WithAllowPrivate()`. The error says so.
- Ports aren't restricted. A public host's port 22 is still reachable, but only public hosts are.
- A receiver that moved and answers with a redirect needs its URL updated. The permanent error names the status code.
- `pigeond` (M5) can reuse `internal/ssrf` for any other user-supplied URLs.
