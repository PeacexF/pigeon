package pigeon_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/pigeontest"
)

func newGateway(t *testing.T, opts ...pigeon.Option) *pigeon.Gateway {
	t.Helper()
	g, err := pigeon.New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func mustSend(t *testing.T, g *pigeon.Gateway, e pigeon.Event) string {
	t.Helper()
	id, err := g.Send(t.Context(), e)
	if err != nil {
		t.Fatalf("Send(%q): %v", e.Type, err)
	}
	return id
}

func mustClose(t *testing.T, g *pigeon.Gateway) {
	t.Helper()
	if err := g.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// timeline records when each delivery attempt happened.
type timeline struct {
	mu    sync.Mutex
	times []time.Time
}

func (tl *timeline) channel(fail func(attempt int) error) pigeon.ChannelFunc {
	return func(context.Context, *pigeon.Message) error {
		tl.mu.Lock()
		tl.times = append(tl.times, time.Now())
		n := len(tl.times)
		tl.mu.Unlock()
		if fail != nil {
			return fail(n)
		}
		return nil
	}
}

func (tl *timeline) gaps() []time.Duration {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	var out []time.Duration
	for i := 1; i < len(tl.times); i++ {
		out = append(out, tl.times[i].Sub(tl.times[i-1]))
	}
	return out
}

func TestSendDelivers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		g := newGateway(t,
			pigeon.WithChannel("ops", rec),
			pigeon.WithRoute("alert.*", "ops"),
			pigeon.WithTemplate("alert.*", "{{.Data.host}}: {{.Data.msg}}", pigeon.Title("{{.Type}} on {{.Channel}}")),
		)
		data := map[string]any{"host": "db1", "msg": "95% full"}
		id := mustSend(t, g, pigeon.Event{Type: "alert.disk", Data: data})
		mustClose(t, g)

		want := []pigeon.Message{{
			ID:        id + ":ops",
			EventID:   id,
			EventType: "alert.disk",
			Channel:   "ops",
			Title:     "alert.disk on ops",
			Text:      "db1: 95% full",
			Data:      data,
			Attempt:   1,
		}}
		if got := rec.Messages(); !reflect.DeepEqual(got, want) {
			t.Errorf("messages:\n got %+v\nwant %+v", got, want)
		}
	})
}

func TestSendFansOutOncePerChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := &pigeontest.Recorder{}, &pigeontest.Recorder{}
		g := newGateway(t,
			pigeon.WithChannel("a", a),
			pigeon.WithChannel("b", b),
			pigeon.WithRoute("order.*", "a", "b"),
			pigeon.WithRoute("order.created", "a"),
		)
		mustSend(t, g, pigeon.Event{Type: "order.created"})
		mustClose(t, g)

		if n := len(a.Messages()); n != 1 {
			t.Errorf("a got %d messages, want 1", n)
		}
		if n := len(b.Messages()); n != 1 {
			t.Errorf("b got %d messages, want 1", n)
		}
	})
}

func TestSendUsesProvidedIDAndTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		g := newGateway(t,
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
			pigeon.WithTemplate("*", "{{.ID}} {{.Time.Year}}"),
		)
		ts := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
		id := mustSend(t, g, pigeon.Event{ID: "evt-1", Type: "x", Time: ts})
		mustClose(t, g)

		if id != "evt-1" {
			t.Errorf("id = %q, want evt-1", id)
		}
		if got := rec.Messages()[0].Text; got != "evt-1 2030" {
			t.Errorf("text = %q", got)
		}
	})
}

func TestSendDefaultTemplate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		g := newGateway(t, pigeon.WithChannel("c", rec), pigeon.WithRoute("*", "c"))
		mustSend(t, g, pigeon.Event{Type: "user.signup", Data: map[string]any{"plan": "pro", "email": "a@b.c"}})
		mustClose(t, g)

		if got, want := rec.Messages()[0].Text, "user.signup\nemail: a@b.c\nplan: pro"; got != want {
			t.Errorf("text = %q, want %q", got, want)
		}
	})
}

func TestSendChannelScopedTemplate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := &pigeontest.Recorder{}, &pigeontest.Recorder{}
		g := newGateway(t,
			pigeon.WithChannel("a", a),
			pigeon.WithChannel("b", b),
			pigeon.WithRoute("*", "a", "b"),
			pigeon.WithTemplate("x", "global"),
			pigeon.WithTemplate("*", "for b", pigeon.For("b")),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		if got := a.Messages()[0].Text; got != "global" {
			t.Errorf("a text = %q", got)
		}
		if got := b.Messages()[0].Text; got != "for b" {
			t.Errorf("b text = %q", got)
		}
	})
}

func TestSendTemplateFuncs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		g := newGateway(t,
			pigeon.WithTemplate("*", "{{shout .Data.msg}}"),
			pigeon.WithTemplateFuncs(map[string]any{"shout": strings.ToUpper}),
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
		)
		mustSend(t, g, pigeon.Event{Type: "x", Data: map[string]any{"msg": "hi"}})
		mustClose(t, g)

		if got := rec.Messages()[0].Text; got != "HI" {
			t.Errorf("text = %q", got)
		}
	})
}

func TestSendRejects(t *testing.T) {
	rec := &pigeontest.Recorder{}
	g := newGateway(t,
		pigeon.WithChannel("c", rec),
		pigeon.WithRoute("alert.*", "c"),
		pigeon.WithTemplate("alert.*", "{{.Data.host}}"),
	)
	defer mustClose(t, g)

	tests := []struct {
		name    string
		event   pigeon.Event
		wantErr error
		wantMsg string
	}{
		{"empty type", pigeon.Event{}, pigeon.ErrInvalidEvent, "type is required"},
		{"no route", pigeon.Event{Type: "order.created"}, pigeon.ErrNoRoute, `"order.created"`},
		{"missing template key", pigeon.Event{Type: "alert.disk"}, nil, `render "alert.disk" for channel "c"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := g.Send(t.Context(), tt.event)
			if err == nil {
				t.Fatalf("Send succeeded with id %q", id)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %q, want it to contain %q", err, tt.wantMsg)
			}
			if id != "" {
				t.Errorf("id = %q, want empty", id)
			}
		})
	}
	if n := rec.Attempts(); n != 0 {
		t.Errorf("channel called %d times", n)
	}
}

func TestSendDuplicate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		var dupes atomic.Int32
		g := newGateway(t,
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
			pigeon.WithDedupeWindow(time.Hour),
			pigeon.WithHooks(pigeon.Hooks{OnDuplicate: func(context.Context, pigeon.Event, string) { dupes.Add(1) }}),
		)
		first := mustSend(t, g, pigeon.Event{Type: "x", IdempotencyKey: "k"})

		id, err := g.Send(t.Context(), pigeon.Event{Type: "x", IdempotencyKey: "k"})
		if !errors.Is(err, pigeon.ErrDuplicate) {
			t.Fatalf("err = %v, want ErrDuplicate", err)
		}
		if id != first {
			t.Errorf("duplicate returned id %q, want original %q", id, first)
		}

		time.Sleep(time.Hour)
		if again := mustSend(t, g, pigeon.Event{Type: "x", IdempotencyKey: "k"}); again == first {
			t.Error("key reuse after the window returned the old id")
		}
		mustClose(t, g)

		if n := len(rec.Messages()); n != 2 {
			t.Errorf("delivered %d messages, want 2", n)
		}
		if n := dupes.Load(); n != 1 {
			t.Errorf("OnDuplicate called %d times, want 1", n)
		}
	})
}

func TestRetryUntilSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		rec.FailNext(2, errors.New("boom"))
		g := newGateway(t, pigeon.WithChannel("c", rec), pigeon.WithRoute("*", "c"))
		id := mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		if n := rec.Attempts(); n != 3 {
			t.Errorf("attempts = %d, want 3", n)
		}
		msgs := rec.Messages()
		if len(msgs) != 1 || msgs[0].Attempt != 3 || msgs[0].ID != id+":c" {
			t.Errorf("messages = %+v", msgs)
		}
	})
}

func TestPermanentErrorIsNotRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		rec.FailNext(10, pigeon.Permanent(errors.New("chat not found")))
		var dead []error
		g := newGateway(t,
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
			pigeon.WithHooks(pigeon.Hooks{OnDeadLetter: func(_ context.Context, _ *pigeon.Message, err error) { dead = append(dead, err) }}),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		if n := rec.Attempts(); n != 1 {
			t.Errorf("attempts = %d, want 1", n)
		}
		if len(dead) != 1 || !pigeon.IsPermanent(dead[0]) {
			t.Errorf("dead letters = %v", dead)
		}
	})
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		rec.FailNext(10, errors.New("boom"))
		var dead atomic.Int32
		g := newGateway(t,
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
			pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: time.Second}),
			pigeon.WithHooks(pigeon.Hooks{OnDeadLetter: func(context.Context, *pigeon.Message, error) { dead.Add(1) }}),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		if n := rec.Attempts(); n != 3 {
			t.Errorf("attempts = %d, want 3", n)
		}
		if n := dead.Load(); n != 1 {
			t.Errorf("dead letters = %d, want 1", n)
		}
	})
}

func TestPerChannelRetryOverride(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		rec.FailNext(10, errors.New("boom"))
		g := newGateway(t,
			pigeon.WithChannel("c", rec, pigeon.Retry(pigeon.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Second, MaxDelay: time.Second})),
			pigeon.WithRoute("*", "c"),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		if n := rec.Attempts(); n != 1 {
			t.Errorf("attempts = %d, want 1", n)
		}
	})
}

func TestRetryAfterDelaysAndPausesChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tl timeline
		ch := tl.channel(func(attempt int) error {
			if attempt == 1 {
				return pigeon.RetryAfter(errors.New("429"), 10*time.Second)
			}
			return nil
		})
		g := newGateway(t,
			pigeon.WithChannel("c", ch),
			pigeon.WithRoute("*", "c"),
			pigeon.WithWorkers(1),
			pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		)
		mustSend(t, g, pigeon.Event{Type: "first"})
		mustSend(t, g, pigeon.Event{Type: "second"})
		mustClose(t, g)

		gaps := tl.gaps()
		if len(gaps) != 2 {
			t.Fatalf("got %d attempts, want 3", len(gaps)+1)
		}
		if gaps[0] < 10*time.Second {
			t.Errorf("first retry after %v, want >= 10s (the channel is paused for every message)", gaps[0])
		}
	})
}

func TestRateLimitSpacesDeliveries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tl timeline
		g := newGateway(t,
			pigeon.WithChannel("c", tl.channel(nil), pigeon.RateLimit(1, time.Second)),
			pigeon.WithRoute("*", "c"),
		)
		for range 3 {
			mustSend(t, g, pigeon.Event{Type: "x"})
		}
		mustClose(t, g)

		for i, gap := range tl.gaps() {
			if gap < time.Second-time.Millisecond {
				t.Errorf("gap %d = %v, want ~1s", i, gap)
			}
		}
	})
}

type ratedChannel struct {
	pigeon.ChannelFunc
}

func (ratedChannel) DefaultRate() pigeon.Rate { return pigeon.Rate{N: 1, Per: time.Hour} }

func TestDefaultRate(t *testing.T) {
	tests := []struct {
		name    string
		opts    []pigeon.ChannelOption
		wantGap time.Duration
	}{
		{"adapter default applies", nil, time.Hour},
		{"NoRateLimit removes it", []pigeon.ChannelOption{pigeon.NoRateLimit()}, 0},
		{"RateLimit overrides it", []pigeon.ChannelOption{pigeon.RateLimit(1, time.Minute)}, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var tl timeline
				g := newGateway(t,
					pigeon.WithChannel("c", ratedChannel{tl.channel(nil)}, tt.opts...),
					pigeon.WithRoute("*", "c"),
				)
				mustSend(t, g, pigeon.Event{Type: "x"})
				mustSend(t, g, pigeon.Event{Type: "x"})
				mustClose(t, g)

				gaps := tl.gaps()
				if len(gaps) != 1 {
					t.Fatalf("got %d deliveries, want 2", len(gaps)+1)
				}
				if d := gaps[0] - tt.wantGap; d < -time.Millisecond || d > time.Millisecond {
					t.Errorf("gap = %v, want %v", gaps[0], tt.wantGap)
				}
			})
		})
	}
}

func TestSendBlocksWhenQueueFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		block := pigeon.ChannelFunc(func(context.Context, *pigeon.Message) error {
			<-release
			return nil
		})
		g := newGateway(t,
			pigeon.WithChannel("c", block),
			pigeon.WithRoute("*", "c"),
			pigeon.WithQueueSize(1),
			pigeon.WithWorkers(1),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := g.Send(ctx, pigeon.Event{Type: "x"}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Send on a full queue: err = %v, want DeadlineExceeded", err)
		}

		close(release)
		mustClose(t, g)
	})
}

func TestDuplicateKeyIsReleasedWhenSendFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		block := pigeon.ChannelFunc(func(context.Context, *pigeon.Message) error {
			<-release
			return nil
		})
		g := newGateway(t,
			pigeon.WithChannel("c", block),
			pigeon.WithRoute("*", "c"),
			pigeon.WithQueueSize(1),
			pigeon.WithWorkers(1),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := g.Send(ctx, pigeon.Event{Type: "x", IdempotencyKey: "k"}); err == nil {
			t.Fatal("Send on a full queue succeeded")
		}

		close(release)
		if _, err := g.Send(t.Context(), pigeon.Event{Type: "x", IdempotencyKey: "k"}); err != nil {
			t.Errorf("retrying a key whose send failed: %v", err)
		}
		mustClose(t, g)
	})
}

func TestAttemptTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hang := pigeon.ChannelFunc(func(ctx context.Context, _ *pigeon.Message) error {
			<-ctx.Done()
			return ctx.Err()
		})
		var dead error
		g := newGateway(t,
			pigeon.WithChannel("c", hang),
			pigeon.WithRoute("*", "c"),
			pigeon.WithAttemptTimeout(time.Second),
			pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Second, MaxDelay: time.Second}),
			pigeon.WithHooks(pigeon.Hooks{OnDeadLetter: func(_ context.Context, _ *pigeon.Message, err error) { dead = err }}),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		if !errors.Is(dead, context.DeadlineExceeded) {
			t.Errorf("dead letter err = %v, want DeadlineExceeded", dead)
		}
	})
}

func TestCloseDropsWhatCannotFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		rec.FailNext(100, errors.New("down"))
		g := newGateway(t,
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
			pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 100, BaseDelay: time.Hour, MaxDelay: time.Hour}),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustSend(t, g, pigeon.Event{Type: "y"})

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		err := g.Close(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close: err = %v, want DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), "2 deliveries dropped") {
			t.Errorf("Close: err = %q, want the dropped count", err)
		}
	})
}

func TestClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGateway(t, pigeon.WithChannel("c", &pigeontest.Recorder{}), pigeon.WithRoute("*", "c"))
		mustClose(t, g)

		if _, err := g.Send(t.Context(), pigeon.Event{Type: "x"}); !errors.Is(err, pigeon.ErrClosed) {
			t.Errorf("Send after Close: err = %v, want ErrClosed", err)
		}
		if err := g.Close(t.Context()); !errors.Is(err, pigeon.ErrClosed) {
			t.Errorf("second Close: err = %v, want ErrClosed", err)
		}
	})
}

type closingChannel struct {
	pigeontest.Recorder
	closed int
	err    error
}

func (c *closingChannel) Close() error {
	c.closed++
	return c.err
}

func TestCloseClosesChannels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ok := &closingChannel{}
		bad := &closingChannel{err: errors.New("flush failed")}
		g := newGateway(t,
			pigeon.WithChannel("ok", ok),
			pigeon.WithChannel("bad", bad),
			pigeon.WithRoute("*", "ok", "bad"),
		)
		err := g.Close(t.Context())
		if err == nil || !strings.Contains(err.Error(), `close channel "bad": flush failed`) {
			t.Errorf("Close: err = %v", err)
		}
		if ok.closed != 1 || bad.closed != 1 {
			t.Errorf("Close calls: ok=%d bad=%d, want 1 each", ok.closed, bad.closed)
		}
	})
}

func TestHooks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &pigeontest.Recorder{}
		rec.FailNext(1, errors.New("boom"))

		var mu sync.Mutex
		var events []string
		log := func(s string) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, s)
		}
		g := newGateway(t,
			pigeon.WithChannel("c", rec),
			pigeon.WithRoute("*", "c"),
			pigeon.WithHooks(pigeon.Hooks{
				OnEnqueue: func(_ context.Context, e pigeon.Event, channels []string) {
					log("enqueue " + e.Type + " " + strings.Join(channels, ","))
				},
				OnAttempt: func(_ context.Context, m *pigeon.Message, err error, _ time.Duration) {
					log("attempt " + m.Channel + " " + map[bool]string{true: "ok", false: "fail"}[err == nil])
				},
				OnDelivered: func(_ context.Context, m *pigeon.Message) { log("delivered " + m.Channel) },
			}),
		)
		mustSend(t, g, pigeon.Event{Type: "x"})
		mustClose(t, g)

		want := []string{"enqueue x c", "attempt c fail", "attempt c ok", "delivered c"}
		if !reflect.DeepEqual(events, want) {
			t.Errorf("hooks:\n got %q\nwant %q", events, want)
		}
	})
}

func TestWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		g := newGateway(t,
			pigeon.WithChannel("log", pigeon.Writer(&buf)),
			pigeon.WithRoute("*", "log"),
			pigeon.WithTemplate("plain", "hello"),
			pigeon.WithTemplate("titled", "body", pigeon.Title("head")),
			pigeon.WithWorkers(1),
		)
		mustSend(t, g, pigeon.Event{Type: "plain"})
		synctest.Wait()
		mustSend(t, g, pigeon.Event{Type: "titled"})
		mustClose(t, g)

		if got, want := buf.String(), "[plain] hello\n[titled] head\nbody\n"; got != want {
			t.Errorf("output = %q, want %q", got, want)
		}
	})
}

type invalidChannel struct{ pigeontest.Recorder }

func (*invalidChannel) Validate() error { return errors.New("missing token") }

func TestNewValidates(t *testing.T) {
	rec := &pigeontest.Recorder{}
	base := []pigeon.Option{pigeon.WithChannel("c", rec), pigeon.WithRoute("*", "c")}
	with := func(opts ...pigeon.Option) []pigeon.Option {
		return append(append([]pigeon.Option{}, base...), opts...)
	}
	badRetry := pigeon.RetryPolicy{MaxAttempts: 0, BaseDelay: time.Second, MaxDelay: time.Second}

	tests := []struct {
		name string
		opts []pigeon.Option
		want string
	}{
		{"no channels", nil, "no channels configured"},
		{"no routes", []pigeon.Option{pigeon.WithChannel("c", rec)}, "no routes configured"},
		{"unknown route channel", with(pigeon.WithRoute("x", "nope")), `route "x": unknown channel "nope"`},
		{"empty route pattern", with(pigeon.WithRoute("", "c")), "route with empty pattern"},
		{"route without channels", with(pigeon.WithRoute("x")), `route "x" has no channels`},
		{"duplicate channel", with(pigeon.WithChannel("c", rec)), `channel "c" registered twice`},
		{"bad channel name", with(pigeon.WithChannel("a b", rec)), `channel "a b": name must`},
		{"nil channel", with(pigeon.WithChannel("n", nil)), `channel "n": nil Channel`},
		{"nil ChannelFunc", with(pigeon.WithChannel("f", pigeon.ChannelFunc(nil))), `channel "f": nil ChannelFunc`},
		{"nil writer", with(pigeon.WithChannel("w", pigeon.Writer(nil))), `channel "w": nil io.Writer`},
		{"channel Validate", with(pigeon.WithChannel("v", &invalidChannel{})), `channel "v": missing token`},
		{"bad rate limit", with(pigeon.WithChannel("r", rec, pigeon.RateLimit(0, time.Second))), "rate limit needs"},
		{"bad channel retry", with(pigeon.WithChannel("r", rec, pigeon.Retry(badRetry))), "MaxAttempts"},
		{"bad template", with(pigeon.WithTemplate("x", "{{.Broken")), `template "x"`},
		{"bad title template", with(pigeon.WithTemplate("x", "ok", pigeon.Title("{{"))), `template "x"`},
		{"empty template pattern", with(pigeon.WithTemplate("", "ok")), "template with empty pattern"},
		{"template unknown channel", with(pigeon.WithTemplate("x", "ok", pigeon.For("nope"))), `template "x": unknown channel "nope"`},
		{"bad retry", with(pigeon.WithRetry(badRetry)), "MaxAttempts must be at least 1"},
		{"retry base delay", with(pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 1})), "BaseDelay must be positive"},
		{"retry max delay", with(pigeon.WithRetry(pigeon.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Second})), "MaxDelay must be at least BaseDelay"},
		{"workers", with(pigeon.WithWorkers(0)), "workers must be at least 1"},
		{"queue size", with(pigeon.WithQueueSize(0)), "queue size must be at least 1"},
		{"queue smaller than fan-out", with(pigeon.WithChannel("d", rec), pigeon.WithQueueSize(1)), "smaller than the 2 channels"},
		{"dedupe window", with(pigeon.WithDedupeWindow(0)), "dedupe window must be positive"},
		{"attempt timeout", with(pigeon.WithAttemptTimeout(0)), "attempt timeout must be positive"},
		{"nil logger", with(pigeon.WithLogger(nil)), "nil logger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := pigeon.New(tt.opts...)
			if err == nil {
				_ = g.Close(t.Context())
				t.Fatal("New succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestNewSkipsKnockOnErrors(t *testing.T) {
	_, err := pigeon.New(
		pigeon.WithChannel("v", &invalidChannel{}),
		pigeon.WithRoute("*", "v"),
		pigeon.WithTemplate("*", "x", pigeon.For("v")),
	)
	if err == nil || strings.Contains(err.Error(), "unknown channel") {
		t.Errorf("err = %v, want only the channel's own error", err)
	}
}

func TestNewReportsEveryError(t *testing.T) {
	_, err := pigeon.New(pigeon.WithWorkers(0), pigeon.WithQueueSize(0))
	for _, want := range []string{"workers", "queue size", "no channels", "no routes"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}
