package pigeon_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
)

func TestPermanent(t *testing.T) {
	if pigeon.Permanent(nil) != nil {
		t.Error("Permanent(nil) != nil")
	}
	base := errors.New("bad chat")
	err := fmt.Errorf("telegram: %w", pigeon.Permanent(base))
	if !pigeon.IsPermanent(err) {
		t.Error("IsPermanent lost through wrapping")
	}
	if !errors.Is(err, base) {
		t.Error("Permanent hides the wrapped error from errors.Is")
	}
	if err.Error() != "telegram: bad chat" {
		t.Errorf("message = %q", err)
	}
	if pigeon.IsPermanent(base) {
		t.Error("plain error reported as permanent")
	}
}

func TestRetryAfter(t *testing.T) {
	if pigeon.RetryAfter(nil, time.Second) != nil {
		t.Error("RetryAfter(nil) != nil")
	}
	err := fmt.Errorf("wrapped: %w", pigeon.RetryAfter(context.DeadlineExceeded, 3*time.Second))
	d, ok := pigeon.RetryAfterDelay(err)
	if !ok || d != 3*time.Second {
		t.Errorf("RetryAfterDelay = %v, %v", d, ok)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("RetryAfter hides the wrapped error from errors.Is")
	}
	if pigeon.IsPermanent(err) {
		t.Error("RetryAfter error reported as permanent")
	}
	if _, ok := pigeon.RetryAfterDelay(errors.New("x")); ok {
		t.Error("plain error has a retry delay")
	}
}
