package pipeline

import "context"

// Semaphore limits the number of concurrent operations using a buffered channel.
// A nil Semaphore imposes no limit — Acquire always succeeds immediately and
// Release is a no-op. Use NewSemaphore to create one; a zero or negative limit
// returns nil (unlimited).
type Semaphore struct {
	ch chan struct{}
}

// NewSemaphore creates a Semaphore that allows up to limit concurrent holders.
// Returns nil when limit <= 0, which represents unlimited concurrency.
func NewSemaphore(limit int) *Semaphore {
	if limit <= 0 {
		return nil
	}
	return &Semaphore{ch: make(chan struct{}, limit)}
}

// Acquire blocks until a slot is available or ctx is cancelled.
// Returns ctx.Err() on cancellation, nil on success.
// Calling Acquire on a nil Semaphore always succeeds immediately.
func (s *Semaphore) Acquire(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees one slot. Must be called once for each successful Acquire.
// Calling Release on a nil Semaphore is a no-op.
func (s *Semaphore) Release() {
	if s == nil {
		return
	}
	<-s.ch
}
