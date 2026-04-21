package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewSemaphore_validLimit(t *testing.T) {
	sem := NewSemaphore(3)
	if sem == nil {
		t.Fatal("expected non-nil semaphore for limit > 0")
	}
}

func TestNewSemaphore_zeroReturnsNil(t *testing.T) {
	sem := NewSemaphore(0)
	if sem != nil {
		t.Fatal("expected nil semaphore for limit 0")
	}
}

func TestNewSemaphore_negativeReturnsNil(t *testing.T) {
	sem := NewSemaphore(-1)
	if sem != nil {
		t.Fatal("expected nil semaphore for negative limit")
	}
}

func TestSemaphore_AcquireRelease(t *testing.T) {
	sem := NewSemaphore(2)

	// Acquire twice within limit.
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	// Release one and acquire again.
	sem.Release()
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("third acquire after release: %v", err)
	}

	// Clean up.
	sem.Release()
	sem.Release()
}

func TestSemaphore_AcquireBlocksWhenFull(t *testing.T) {
	sem := NewSemaphore(1)
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire should block until release.
	acquired := make(chan struct{})
	go func() {
		if err := sem.Acquire(context.Background()); err != nil {
			t.Errorf("blocked acquire: %v", err)
			return
		}
		close(acquired)
	}()

	// Give the goroutine a moment to start blocking.
	select {
	case <-acquired:
		t.Fatal("second acquire should not succeed before release")
	case <-time.After(50 * time.Millisecond):
		// Expected: still blocked.
	}

	sem.Release()

	select {
	case <-acquired:
		// Expected: now acquired.
	case <-time.After(time.Second):
		t.Fatal("second acquire did not unblock after release")
	}

	sem.Release()
}

func TestSemaphore_AcquireRespectsContext(t *testing.T) {
	sem := NewSemaphore(1)
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := sem.Acquire(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	sem.Release()
}

func TestSemaphore_AcquireDeadlineExceeded(t *testing.T) {
	sem := NewSemaphore(1)
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := sem.Acquire(ctx)
	if err == nil {
		t.Fatal("expected error from deadline exceeded")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}

	sem.Release()
}

func TestSemaphore_ConcurrencyLimit(t *testing.T) {
	limit := 3
	sem := NewSemaphore(limit)
	totalWorkers := 10

	var maxConcurrent atomic.Int32
	var currentConcurrent atomic.Int32
	var wg sync.WaitGroup

	for workerIdx := 0; workerIdx < totalWorkers; workerIdx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := sem.Acquire(context.Background()); err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer sem.Release()

			current := currentConcurrent.Add(1)
			// Track max concurrency.
			for {
				prev := maxConcurrent.Load()
				if current <= prev || maxConcurrent.CompareAndSwap(prev, current) {
					break
				}
			}

			// Simulate some work.
			time.Sleep(5 * time.Millisecond)
			currentConcurrent.Add(-1)
		}()
	}

	wg.Wait()

	observed := int(maxConcurrent.Load())
	if observed > limit {
		t.Fatalf("max concurrent %d exceeded limit %d", observed, limit)
	}
	if observed == 0 {
		t.Fatal("max concurrent was 0 — no work was done")
	}
}

func TestNewSemaphore_NilAcquireIsNoop(t *testing.T) {
	var sem *Semaphore
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatalf("nil semaphore Acquire should succeed: %v", err)
	}
}

func TestNewSemaphore_NilReleaseIsNoop(t *testing.T) {
	var sem *Semaphore
	// Should not panic.
	sem.Release()
}
