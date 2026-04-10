package scheduler

import (
	"log"
	"runtime/debug"
)

// recoverPanic logs a panic with its stack trace. Call it as a deferred
// function at the top of any goroutine — before any other deferred work
// that must still run — to prevent a runtime panic from crashing the
// daemon or silently killing a long-running background service.
//
// Usage:
//
//	func (s *Scheduler) runJob(job *Job) {
//	    defer s.wg.Done()
//	    defer recoverPanic("scheduler.runJob")
//	    // ... body ...
//	}
//
// Go runs deferred functions in LIFO order, so the recoverPanic defer
// should be placed AFTER the wg.Done defer — that way recoverPanic runs
// first (catching the panic) and then wg.Done runs (advancing the
// WaitGroup), so the caller's wg.Wait() isn't left hanging forever.
//
// The name parameter identifies the goroutine in the log output so
// operators can find the source of a crash quickly. Use a dotted
// "package.function" convention to match the runtime stack format.
func recoverPanic(name string) {
	if r := recover(); r != nil {
		log.Printf("[PANIC] %s: %v\n%s", name, r, debug.Stack())
	}
}
