// Package pigeon is a notification gate: applications send typed events,
// pigeon routes them to channels (Telegram, Discord, email, webhooks, ...),
// renders the message from a template, and delivers it with retries,
// rate limiting and deduplication.
//
// pigeon is under active development and has no stable API yet.
package pigeon
