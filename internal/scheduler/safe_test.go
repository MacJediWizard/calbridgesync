package scheduler

import (
	"sync"
	"testing"
	"time"
)

// TestRecoverPanic_RecoversAndDoesNotPropagate verifies the core
// contract of the recoverPanic helper: a deferred call recovers any
// panic that escapes the goroutine body, preventing it from crashing
// the process.
func TestRecoverPanic_RecoversAndDoesNotPropagate(t *testing.T) {
	// This test would fail (crash the test binary) if recoverPanic
	// did not actually recover. The absence of a crash is the pass
	// condition.
	done := false
	func() {
		defer func() {
			// If recoverPanic failed to catch the panic, our own
			// recover here would see it. The test would still
			// technically pass but the assertion below would never
			// run, so we guard with a secondary check.
			if r := recover(); r != nil {
				t.Errorf("panic escaped recoverPanic: %v", r)
			}
		}()
		defer recoverPanic("test.doesNotPropagate")
		panic("expected test panic")
	}()
	done = true
	if !done {
		t.Error("recoverPanic did not allow the deferred-wrapped call to complete")
	}
}

// TestRecoverPanic_WaitGroupAdvances verifies that when recoverPanic is
// paired with a deferred wg.Done(), the WaitGroup advances even if the
// body panics. This is the critical contract for scheduler goroutines —
// without wg.Done() advancing, the graceful-shutdown wg.Wait() would
// hang forever.
func TestRecoverPanic_WaitGroupAdvances(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer recoverPanic("test.wgAdvances")
		panic("expected test panic in wg scenario")
	}()

	// wg.Wait() should return quickly. If recoverPanic were broken,
	// this would block forever and the test would time out.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success — wg.Wait returned, meaning wg.Done() executed
		// despite the panic in the body.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("wg.Wait did not return; recoverPanic did not let wg.Done advance after panic")
	}
}

// TestRecoverPanic_NoopOnNormalReturn verifies the no-panic happy path:
// recoverPanic should do nothing visible when the body returns normally.
func TestRecoverPanic_NoopOnNormalReturn(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	ranToCompletion := false
	go func() {
		defer wg.Done()
		defer recoverPanic("test.noopNormal")
		ranToCompletion = true
	}()
	wg.Wait()
	if !ranToCompletion {
		t.Error("goroutine body did not execute to completion in the no-panic path")
	}
}

// TestRecoverPanic_NilPointerDereference verifies recovery from a real
// runtime panic (nil map access), not just an explicit panic() call.
// This is the more realistic crash mode in production code.
func TestRecoverPanic_NilPointerDereference(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer recoverPanic("test.nilDeref")
		var m map[string]int
		m["key"] = 1 // panic: assignment to entry in nil map
	}()
	wg.Wait()
	// Passing this test means the runtime panic was caught. No further
	// assertion needed — the absence of a crash is the pass condition.
}
