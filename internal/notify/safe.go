package notify

import (
	"log"
	"runtime/debug"
)

// recoverPanic logs a panic with its stack trace. Call it as a deferred
// function at the top of any goroutine to prevent a runtime panic from
// crashing the daemon.
//
// Mirrors the pattern in internal/scheduler/safe.go (PR #37 / Issue #37).
// Duplicated across packages rather than shared via a new "internal/safe"
// package because the helper is 5 lines of code — duplication cost is
// negligible and avoids a new dependency edge.
//
// Ordering matters when this is used alongside cleanup defers:
//
//	go func() {
//	    defer func() {
//	        // cleanup runs second (LIFO), so it sees delivered=false on panic
//	        n.mu.Lock()
//	        delete(n.inFlightAlerts, key)
//	        if delivered { ... }
//	        n.mu.Unlock()
//	    }()
//	    defer recoverPanic("notify.name")  // runs first, catches panic
//	    delivered = n.sendWithPrefs(...)
//	}()
//
// The cleanup defer is declared FIRST so it runs LAST. recoverPanic is
// declared SECOND so it runs FIRST. That way the cleanup sees the
// correct post-panic state: delivered stays false, in-flight is cleared,
// cooldown is NOT recorded. If the order were reversed, a panic would
// propagate out of the cleanup defer and crash the daemon — defeating
// the whole point of the recovery.
func recoverPanic(name string) {
	if r := recover(); r != nil {
		log.Printf("[PANIC] %s: %v\n%s", name, r, debug.Stack())
	}
}
