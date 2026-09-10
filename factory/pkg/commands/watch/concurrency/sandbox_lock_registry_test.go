package concurrency

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSandboxLockRegistry_BasicLifecycle(t *testing.T) {
	r := NewSandboxLockRegistry()

	if r.IsBusy("sb-1") {
		t.Fatalf("expected sb-1 to not be busy initially")
	}

	// 1. Acquire lease
	if !r.TryAcquire("sb-1", "task-1.yaml") {
		t.Fatalf("expected to acquire sb-1 lease successfully")
	}

	if !r.IsBusy("sb-1") {
		t.Errorf("expected sb-1 to be busy")
	}

	if r.IsBusy("sb-2") {
		t.Errorf("expected sb-2 to not be busy")
	}

	// 2. Reject duplicate acquisition
	if r.TryAcquire("sb-1", "task-2.yaml") {
		t.Errorf("expected second acquisition of sb-1 to fail")
	}

	// 3. Acquire second sandbox
	if !r.TryAcquire("sb-2", "task-2.yaml") {
		t.Errorf("expected sb-2 acquisition to succeed")
	}

	if !r.IsBusy("sb-2") {
		t.Errorf("expected sb-2 to be busy")
	}
}

func TestSandboxLockRegistry_ScopedRelease(t *testing.T) {
	r := NewSandboxLockRegistry()

	if !r.TryAcquire("sb-1", "task-1.yaml") {
		t.Fatalf("expected to acquire sb-1")
	}

	// Attempting to release with wrong taskFilename should fail and preserve lease
	if r.Release("sb-1", "task-wrong.yaml") {
		t.Errorf("expected scoped release with mismatched task to return false")
	}
	if !r.IsBusy("sb-1") {
		t.Errorf("sb-1 should still be busy after rejected release")
	}

	// Releasing with matching taskFilename should succeed
	if !r.Release("sb-1", "task-1.yaml") {
		t.Errorf("expected scoped release with matching task to succeed")
	}
	if r.IsBusy("sb-1") {
		t.Errorf("expected sb-1 to no longer be busy after release")
	}

	// Releasing an unheld sandbox should return false
	if r.Release("sb-1", "task-1.yaml") {
		t.Errorf("expected release of unheld sandbox to return false")
	}

	// Unconditional release with empty taskFilename
	if !r.TryAcquire("sb-2", "task-2.yaml") {
		t.Fatalf("expected to acquire sb-2")
	}
	if !r.Release("sb-2", "") {
		t.Errorf("expected unconditional release with empty taskFilename to succeed")
	}
	if r.IsBusy("sb-2") {
		t.Errorf("expected sb-2 to no longer be busy after unconditional release")
	}
}

func TestSandboxLockRegistry_EmptySandboxName(t *testing.T) {
	r := NewSandboxLockRegistry()

	// Empty sandbox name is a no-op that always succeeds and is never busy
	if !r.TryAcquire("", "task-empty.yaml") {
		t.Errorf("expected empty sandbox name TryAcquire to return true")
	}
	if r.IsBusy("") {
		t.Errorf("expected empty sandbox name to not be reported busy")
	}
	if !r.Release("", "task-empty.yaml") {
		t.Errorf("expected empty sandbox name Release to return true")
	}
}

func TestSandboxLockRegistry_ConcurrentStressSameSandbox(t *testing.T) {
	r := NewSandboxLockRegistry()
	sbName := "shared-sb"

	var activeHolders int64
	var maxConcurrentHolders int64
	var successfulAcquisitions int64

	iterations := 200
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := 0; i < iterations; i++ {
		go func(id int) {
			defer wg.Done()
			taskFilename := fmt.Sprintf("task-%d.yaml", id)

			if r.TryAcquire(sbName, taskFilename) {
				atomic.AddInt64(&successfulAcquisitions, 1)
				current := atomic.AddInt64(&activeHolders, 1)

				// Record peak concurrency (must never exceed 1)
				for {
					peak := atomic.LoadInt64(&maxConcurrentHolders)
					if current <= peak || atomic.CompareAndSwapInt64(&maxConcurrentHolders, peak, current) {
						break
					}
				}

				if !r.IsBusy(sbName) {
					t.Errorf("expected %s to be busy while held", sbName)
				}

				// Attempt release with invalid task must fail
				if r.Release(sbName, "imposter-task.yaml") {
					t.Errorf("imposter release succeeded unexpectedly")
				}

				atomic.AddInt64(&activeHolders, -1)
				if !r.Release(sbName, taskFilename) {
					t.Errorf("expected valid release to succeed for %s", taskFilename)
				}
			}
		}(i)
	}

	wg.Wait()

	if maxConcurrentHolders > 1 {
		t.Fatalf("invariant violated: multiple concurrent holders detected (%d)", maxConcurrentHolders)
	}
	if successfulAcquisitions == 0 {
		t.Fatalf("expected at least one successful acquisition during stress test")
	}
	if r.IsBusy(sbName) {
		t.Errorf("expected %s to be idle after all goroutines finish", sbName)
	}
}

func TestSandboxLockRegistry_ConcurrentStressMultipleSandboxes(t *testing.T) {
	r := NewSandboxLockRegistry()
	numSandboxes := 10
	numWorkers := 100

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func(id int) {
			defer wg.Done()
			sbName := fmt.Sprintf("sb-%d", id%numSandboxes)
			taskFilename := fmt.Sprintf("task-%d.yaml", id)

			if r.TryAcquire(sbName, taskFilename) {
				if !r.IsBusy(sbName) {
					t.Errorf("expected %s to be busy", sbName)
				}
				r.Release(sbName, taskFilename)
			}
		}(i)
	}

	wg.Wait()

	for i := 0; i < numSandboxes; i++ {
		sbName := fmt.Sprintf("sb-%d", i)
		if r.IsBusy(sbName) {
			t.Errorf("expected %s to be idle after all workers finish", sbName)
		}
	}
}

func TestSandboxLockRegistry_NilSafety(t *testing.T) {
	var r *SandboxLockRegistry
	if !r.TryAcquire("sb-1", "task-1.yaml") {
		t.Errorf("expected nil registry TryAcquire to return true")
	}
	if r.IsBusy("sb-1") {
		t.Errorf("expected nil registry IsBusy to return false")
	}
	if !r.Release("sb-1", "task-1.yaml") {
		t.Errorf("expected nil registry Release to return true")
	}
}
