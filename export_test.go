package pigeon

// NewMemQueue exposes the default queue to the external tests.
func NewMemQueue(size int) Queue { return newMemQueue(size) }
