package memqueue

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestPutGetDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[int](2)
		if err := q.Put(t.Context(), 1, 2); err != nil {
			t.Fatal(err)
		}
		for want := 1; want <= 2; want++ {
			got, err := q.Get(t.Context())
			if err != nil || got != want {
				t.Fatalf("Get = %d, %v; want %d", got, err, want)
			}
			q.Done()
		}
		q.Close()
		if n, err := q.Wait(t.Context()); n != 0 || err != nil {
			t.Errorf("Wait = %d, %v", n, err)
		}
	})
}

func TestPutBlocksUntilDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[int](1)
		_ = q.Put(t.Context(), 1)
		_, _ = q.Get(t.Context()) // in flight still holds capacity

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := q.Put(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Put on full queue: %v", err)
		}

		done := make(chan error)
		go func() { done <- q.Put(t.Context(), 3) }()
		synctest.Wait()
		q.Done()
		if err := <-done; err != nil {
			t.Errorf("Put after Done: %v", err)
		}
	})
}

func TestPutIsAllOrNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[int](3)
		_ = q.Put(t.Context(), 1, 2)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := q.Put(ctx, 3, 4); err == nil {
			t.Fatal("batch larger than free space was accepted")
		}
		if err := q.Put(t.Context(), 5); err != nil {
			t.Errorf("capacity leaked by the failed batch: %v", err)
		}
		if err := q.Put(t.Context(), 1, 2, 3, 4); err == nil {
			t.Error("batch larger than capacity was accepted")
		}
	})
}

func TestConcurrentBatchesDoNotDeadlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[int](4)
		go func() {
			for {
				if _, err := q.Get(t.Context()); err != nil {
					return
				}
				q.Done()
			}
		}()
		errs := make(chan error)
		for range 10 {
			go func() { errs <- q.Put(t.Context(), 1, 2, 3) }()
		}
		for range 10 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		q.Close()
		_, _ = q.Wait(t.Context())
	})
}

func TestLater(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[string](1)
		_ = q.Put(t.Context(), "job")
		job, _ := q.Get(t.Context())

		start := time.Now()
		q.Later(job, time.Minute)
		got, _ := q.Get(t.Context())
		if got != "job" || time.Since(start) != time.Minute {
			t.Errorf("Later delivered %q after %v", got, time.Since(start))
		}
		q.Done()
	})
}

func TestClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[int](1)
		_ = q.Put(t.Context(), 1)

		blocked := make(chan error)
		go func() { blocked <- q.Put(t.Context(), 2) }()
		synctest.Wait()
		q.Close()
		if err := <-blocked; !errors.Is(err, ErrClosed) {
			t.Errorf("blocked Put after Close: %v", err)
		}
		if err := q.Put(t.Context(), 3); !errors.Is(err, ErrClosed) {
			t.Errorf("Put after Close: %v", err)
		}
		q.Close()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if n, err := q.Wait(ctx); n != 1 || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Wait with an unfinished item = %d, %v", n, err)
		}
	})
}

func TestStopCancelsTimers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := New[int](2)
		_ = q.Put(t.Context(), 1)
		item, _ := q.Get(t.Context())
		q.Later(item, time.Minute)
		q.Stop()
		q.Later(item, time.Second)

		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		if _, err := q.Get(ctx); err == nil {
			t.Error("item became ready after Stop")
		}
	})
}
