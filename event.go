package pigeon

import "time"

// Event is something that happened in your application.
type Event struct {
	// ID is generated if empty.
	ID string

	// Type is required and is what routes and templates match against, e.g. "order.created".
	Type string

	// Data is the template input. It is a map rather than any so that it
	// renders the same after a round trip through a durable queue.
	Data map[string]any

	// IdempotencyKey, if set, makes Send reject repeats within the dedupe window with ErrDuplicate.
	IdempotencyKey string

	// Time defaults to the time of Send.
	Time time.Time
}

// Message is one rendered delivery of an Event to one channel.
type Message struct {
	// ID is unique per event and channel, and stable across retries,
	// so channels can use it as a provider-side idempotency key.
	ID string

	EventID   string
	EventType string
	Channel   string

	// Title is empty unless the matching template sets one.
	Title string
	Text  string

	// Data is the event's data, shared between messages: treat it as read-only.
	Data map[string]any

	// Attempt is 1 on the first delivery attempt.
	Attempt int
}
