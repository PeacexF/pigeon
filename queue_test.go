package pigeon_test

import (
	"testing"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/pigeontest"
)

func TestMemQueue(t *testing.T) {
	pigeontest.TestQueue(t, func(*testing.T) pigeon.Queue { return pigeon.NewMemQueue(1024) })
}
