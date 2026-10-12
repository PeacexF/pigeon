package pigeontest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/pigeontest"
)

func TestRecorder(t *testing.T) {
	var r pigeontest.Recorder
	boom := errors.New("boom")
	r.FailNext(2, boom)

	for i, want := range []error{boom, boom, nil} {
		if err := r.Send(context.Background(), &pigeon.Message{Text: "hi"}); !errors.Is(err, want) {
			t.Errorf("Send #%d = %v, want %v", i+1, err, want)
		}
	}
	if r.Attempts() != 3 || len(r.Messages()) != 1 || r.Messages()[0].Text != "hi" {
		t.Errorf("attempts %d, messages %v", r.Attempts(), r.Messages())
	}

	r.Messages()[0].Text = "mutated"
	if r.Messages()[0].Text != "hi" {
		t.Error("Messages exposes internal state")
	}

	r.Reset()
	if r.Attempts() != 0 || len(r.Messages()) != 0 {
		t.Error("Reset left state behind")
	}
}
