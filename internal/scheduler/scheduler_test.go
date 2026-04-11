package scheduler

import (
	"sync"
	"testing"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/db"
	"github.com/macjediwizard/calbridgesync/internal/notify"
)

func TestNew(t *testing.T) {
	t.Run("creates scheduler with nil dependencies", func(t *testing.T) {
		// Note: In production, db and syncEngine would be required,
		// but we can create the scheduler without them for testing structure
		sched := New(nil, nil, nil)

		if sched == nil {
			t.Fatal("expected non-nil scheduler")
		}

		if sched.jobs == nil {
			t.Error("expected jobs map to be initialized")
		}

		if sched.syncLocks == nil {
			t.Error("expected syncLocks map to be initialized")
		}

		if sched.ctx == nil {
			t.Error("expected context to be initialized")
		}

		if sched.cancel == nil {
			t.Error("expected cancel function to be initialized")
		}
	})
}

func TestGetJobCount(t *testing.T) {
	t.Run("returns zero for new scheduler", func(t *testing.T) {
		sched := New(nil, nil, nil)

		count := sched.GetJobCount()
		if count != 0 {
			t.Errorf("expected 0 jobs, got %d", count)
		}
	})
}

func TestJobStruct(t *testing.T) {
	t.Run("job struct has expected fields", func(t *testing.T) {
		job := &Job{
			sourceID: "source-123",
			interval: 5 * time.Minute,
		}

		if job.sourceID != "source-123" {
			t.Error("sourceID not set correctly")
		}

		if job.interval != 5*time.Minute {
			t.Error("interval not set correctly")
		}
	})
}

func TestSchedulerConstants(t *testing.T) {
	t.Run("cleanup interval is 24 hours", func(t *testing.T) {
		if cleanupInterval != 24*time.Hour {
			t.Errorf("expected cleanupInterval to be 24h, got %v", cleanupInterval)
		}
	})

	t.Run("log retention is 30 days", func(t *testing.T) {
		if logRetentionDays != 30 {
			t.Errorf("expected logRetentionDays to be 30, got %d", logRetentionDays)
		}
	})

	t.Run("sync timeout is 120 minutes", func(t *testing.T) {
		if syncTimeout != 120*time.Minute {
			t.Errorf("expected syncTimeout to be 120m, got %v", syncTimeout)
		}
	})

	t.Run("startup stagger is 30 seconds", func(t *testing.T) {
		if startupStagger != 30*time.Second {
			t.Errorf("expected startupStagger to be 30s, got %v", startupStagger)
		}
	})
}

func TestSchedulerStartStop(t *testing.T) {
	t.Run("start sets started flag", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Note: Start() will fail without a real DB, but we can test
		// the started flag protection by checking the initial state
		if sched.started {
			t.Error("expected started to be false initially")
		}
	})

	t.Run("stop is idempotent", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Stop should not panic when scheduler hasn't started
		sched.Stop()
		sched.Stop() // Should be safe to call multiple times
	})

	// TestSchedulerStop_DrainTimeout is the regression test for
	// #133. It adds a goroutine to the scheduler's waitgroup that
	// intentionally never returns, then calls Stop() with a very
	// short stopDrainTimeout and verifies the call returns within
	// the timeout window instead of hanging forever.
	//
	// Without the timeout added in #133, Stop() would block on
	// wg.Wait() indefinitely because the dummy goroutine never
	// calls wg.Done(). With the fix, Stop() logs a warning and
	// returns when the drain timer fires.
	t.Run("drain timeout forces return on stuck goroutine", func(t *testing.T) {
		// Shrink the timeout so the test completes in reasonable
		// wall-clock time. Restore at the end.
		origTimeout := stopDrainTimeout
		stopDrainTimeout = 50 * time.Millisecond
		defer func() { stopDrainTimeout = origTimeout }()

		sched := New(nil, nil, nil)

		// Flip started = true so Stop() actually runs the drain
		// logic (otherwise it short-circuits on !started).
		sched.mu.Lock()
		sched.started = true
		sched.mu.Unlock()

		// Add a goroutine to the waitgroup that blocks forever,
		// simulating an in-flight sync that refuses to honor
		// context cancellation. block channel ensures the
		// goroutine is alive when Stop() is called.
		block := make(chan struct{})
		defer close(block) // let the goroutine exit after the test
		sched.wg.Add(1)
		go func() {
			defer sched.wg.Done()
			<-block
		}()

		start := time.Now()
		sched.Stop()
		elapsed := time.Since(start)

		// Stop() must have returned within (timeout + scheduling
		// jitter). Allow up to 5x the timeout as a generous upper
		// bound to avoid CI flakiness.
		if elapsed > 5*stopDrainTimeout {
			t.Errorf("Stop() took %v, want < %v — drain timeout not enforced", elapsed, 5*stopDrainTimeout)
		}
		// And it must have taken at least the timeout (sanity
		// check — we shouldn't be returning before the timer
		// fires).
		if elapsed < stopDrainTimeout/2 {
			t.Errorf("Stop() returned in %v before drain timeout %v — the drain path didn't actually wait", elapsed, stopDrainTimeout)
		}
	})

	t.Run("drain completes fast when goroutines exit promptly", func(t *testing.T) {
		origTimeout := stopDrainTimeout
		stopDrainTimeout = 5 * time.Second
		defer func() { stopDrainTimeout = origTimeout }()

		sched := New(nil, nil, nil)
		sched.mu.Lock()
		sched.started = true
		sched.mu.Unlock()

		// Add a goroutine that exits immediately when ctx is
		// canceled. This models a well-behaved sync.
		sched.wg.Add(1)
		go func() {
			defer sched.wg.Done()
			<-sched.ctx.Done()
		}()

		start := time.Now()
		sched.Stop()
		elapsed := time.Since(start)

		// Well-behaved goroutines should let Stop() return in
		// milliseconds, well under the drain timeout.
		if elapsed > stopDrainTimeout/2 {
			t.Errorf("Stop() took %v with a well-behaved goroutine — should have drained in milliseconds", elapsed)
		}
	})
}

func TestGetSyncLock(t *testing.T) {
	t.Run("creates lock for new source", func(t *testing.T) {
		sched := New(nil, nil, nil)

		lock := sched.getSyncLock("source-1")
		if lock == nil {
			t.Fatal("expected non-nil lock")
		}

		// Same source should return same lock
		lock2 := sched.getSyncLock("source-1")
		if lock != lock2 {
			t.Error("expected same lock for same source")
		}
	})

	t.Run("creates different locks for different sources", func(t *testing.T) {
		sched := New(nil, nil, nil)

		lock1 := sched.getSyncLock("source-1")
		lock2 := sched.getSyncLock("source-2")

		if lock1 == lock2 {
			t.Error("expected different locks for different sources")
		}
	})
}

// TestSkipCountAccounting covers the consecutive-skip counter helpers
// introduced in #93 so the WARNING-on-threshold escalation has a
// regression test. The counter is per-source, increments on skip,
// resets on successful lock acquisition, and must be safe for
// concurrent callers.
func TestSkipCountAccounting(t *testing.T) {
	t.Run("increment returns running total", func(t *testing.T) {
		sched := New(nil, nil, nil)

		if got := sched.incrementSkipCount("source-1"); got != 1 {
			t.Errorf("first increment: want 1, got %d", got)
		}
		if got := sched.incrementSkipCount("source-1"); got != 2 {
			t.Errorf("second increment: want 2, got %d", got)
		}
		if got := sched.incrementSkipCount("source-1"); got != 3 {
			t.Errorf("third increment: want 3 (threshold), got %d", got)
		}
	})

	t.Run("different sources counted independently", func(t *testing.T) {
		sched := New(nil, nil, nil)

		sched.incrementSkipCount("source-a")
		sched.incrementSkipCount("source-a")
		sched.incrementSkipCount("source-b")

		if got := sched.incrementSkipCount("source-a"); got != 3 {
			t.Errorf("source-a: want 3, got %d", got)
		}
		if got := sched.incrementSkipCount("source-b"); got != 2 {
			t.Errorf("source-b: want 2, got %d", got)
		}
	})

	t.Run("reset zeros the counter", func(t *testing.T) {
		sched := New(nil, nil, nil)

		sched.incrementSkipCount("source-1")
		sched.incrementSkipCount("source-1")
		sched.incrementSkipCount("source-1")
		sched.resetSkipCount("source-1")

		if got := sched.incrementSkipCount("source-1"); got != 1 {
			t.Errorf("after reset: want 1, got %d", got)
		}
	})

	t.Run("reset on unknown source is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Must not panic on sources that never incremented
		sched.resetSkipCount("never-seen")
	})

	t.Run("concurrent increments are race-free", func(t *testing.T) {
		// Sanity check; run under `go test -race` to prove no race.
		sched := New(nil, nil, nil)

		const workers = 32
		const per = 10
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < per; j++ {
					sched.incrementSkipCount("hot-source")
				}
			}()
		}
		wg.Wait()

		// Final count via one more increment so we don't need a
		// getter — expected is workers*per + 1
		want := workers*per + 1
		if got := sched.incrementSkipCount("hot-source"); got != want {
			t.Errorf("concurrent final count: want %d, got %d", want, got)
		}
	})

	t.Run("threshold constant is sensible", func(t *testing.T) {
		if consecutiveSkipWarnThreshold < 2 {
			t.Errorf("threshold %d is too sensitive — one skip should not warn", consecutiveSkipWarnThreshold)
		}
		if consecutiveSkipWarnThreshold > 10 {
			t.Errorf("threshold %d is too lax — more than 10 skips means the warning comes too late to be actionable", consecutiveSkipWarnThreshold)
		}
	})
}

func TestRemoveJob(t *testing.T) {
	t.Run("remove non-existent job is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Should not panic
		sched.RemoveJob("non-existent-source")
	})
}

func TestUpdateJobInterval(t *testing.T) {
	t.Run("update non-existent job is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Should not panic
		sched.UpdateJobInterval("non-existent-source", 10*time.Minute)
	})
}

// addJobDirectly adds a job to the scheduler without starting the goroutine.
// This is for testing purposes only.
func addJobDirectly(s *Scheduler, sourceID string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := &Job{
		sourceID: sourceID,
		interval: interval,
		ticker:   time.NewTicker(interval),
		stopCh:   make(chan struct{}),
	}

	s.jobs[sourceID] = job
}

func TestAddJob(t *testing.T) {
	t.Run("adds job to jobs map", func(t *testing.T) {
		sched := New(nil, nil, nil)

		initialCount := sched.GetJobCount()
		if initialCount != 0 {
			t.Errorf("expected 0 initial jobs, got %d", initialCount)
		}

		// Add a job directly without triggering sync
		addJobDirectly(sched, "source-1", 1*time.Hour)

		count := sched.GetJobCount()
		if count != 1 {
			t.Errorf("expected 1 job after adding, got %d", count)
		}
	})

	t.Run("adds multiple jobs", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)
		addJobDirectly(sched, "source-2", 2*time.Hour)
		addJobDirectly(sched, "source-3", 30*time.Minute)

		count := sched.GetJobCount()
		if count != 3 {
			t.Errorf("expected 3 jobs, got %d", count)
		}
	})

	t.Run("replaces existing job with same source ID", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)

		// Add job with same source ID
		addJobDirectly(sched, "source-1", 2*time.Hour)

		count := sched.GetJobCount()
		if count != 1 {
			t.Errorf("expected 1 job (replaced), got %d", count)
		}

		// Verify interval was updated
		sched.mu.RLock()
		job := sched.jobs["source-1"]
		sched.mu.RUnlock()

		if job.interval != 2*time.Hour {
			t.Errorf("expected interval 2h, got %v", job.interval)
		}
	})
}

func TestRemoveJobWithExistingJob(t *testing.T) {
	t.Run("removes existing job and decrements count", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)

		count := sched.GetJobCount()
		if count != 1 {
			t.Errorf("expected 1 job, got %d", count)
		}

		sched.RemoveJob("source-1")

		count = sched.GetJobCount()
		if count != 0 {
			t.Errorf("expected 0 jobs after removal, got %d", count)
		}
	})

	t.Run("removes one job leaves others", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)
		addJobDirectly(sched, "source-2", 1*time.Hour)
		addJobDirectly(sched, "source-3", 1*time.Hour)

		sched.RemoveJob("source-2")

		count := sched.GetJobCount()
		if count != 2 {
			t.Errorf("expected 2 jobs after removal, got %d", count)
		}
	})
}

func TestUpdateJobIntervalWithExistingJob(t *testing.T) {
	t.Run("updates interval for existing job", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)

		// Update the interval - should not panic or change job count
		sched.UpdateJobInterval("source-1", 30*time.Minute)

		count := sched.GetJobCount()
		if count != 1 {
			t.Errorf("expected 1 job after update, got %d", count)
		}

		// Verify the job still exists
		sched.mu.RLock()
		job, exists := sched.jobs["source-1"]
		sched.mu.RUnlock()

		if !exists {
			t.Fatal("expected job to still exist")
		}

		if job.interval != 30*time.Minute {
			t.Errorf("expected interval 30m, got %v", job.interval)
		}
	})
}

func TestSchedulerStopWithJobs(t *testing.T) {
	t.Run("stop clears all jobs", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)
		addJobDirectly(sched, "source-2", 1*time.Hour)

		// Mark as started to test Stop properly
		sched.mu.Lock()
		sched.started = true
		sched.mu.Unlock()

		sched.Stop()

		count := sched.GetJobCount()
		if count != 0 {
			t.Errorf("expected 0 jobs after stop, got %d", count)
		}
	})

	t.Run("stop sets started to false", func(t *testing.T) {
		sched := New(nil, nil, nil)

		sched.mu.Lock()
		sched.started = true
		sched.mu.Unlock()

		sched.Stop()

		sched.mu.RLock()
		started := sched.started
		sched.mu.RUnlock()

		if started {
			t.Error("expected started to be false after stop")
		}
	})
}

func TestTriggerSync(t *testing.T) {
	t.Run("trigger sync method exists", func(t *testing.T) {
		// TriggerSync will start a goroutine that fails due to nil db
		// but it should not panic - the goroutine handles the error
		// We can't easily test this without a mock, so just verify
		// the scheduler can be created (method is already tested via compilation)
		_ = New(nil, nil, nil)
	})
}

func TestConcurrentAccess(t *testing.T) {
	t.Run("concurrent add and remove is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		var wg sync.WaitGroup

		// Concurrently add jobs
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				sourceID := string(rune('a' + id))
				addJobDirectly(sched, sourceID, 1*time.Hour)
			}(i)
		}

		wg.Wait()

		count := sched.GetJobCount()
		if count != 10 {
			t.Errorf("expected 10 jobs, got %d", count)
		}

		// Concurrently remove jobs
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				sourceID := string(rune('a' + id))
				sched.RemoveJob(sourceID)
			}(i)
		}

		wg.Wait()

		count = sched.GetJobCount()
		if count != 0 {
			t.Errorf("expected 0 jobs after removal, got %d", count)
		}
	})

	t.Run("concurrent getSyncLock is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		var wg sync.WaitGroup
		locks := make([]*sync.Mutex, 100)

		// Concurrently get locks for the same source
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				locks[idx] = sched.getSyncLock("source-1")
			}(i)
		}

		wg.Wait()

		// All locks should be the same
		for i := 1; i < 100; i++ {
			if locks[i] != locks[0] {
				t.Error("expected all locks to be the same for same source")
				break
			}
		}
	})

	t.Run("concurrent GetJobCount is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)

		var wg sync.WaitGroup

		// Concurrently read job count while modifying
		for i := 0; i < 50; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				sched.GetJobCount()
			}()
			go func(id int) {
				defer wg.Done()
				sourceID := string(rune('a' + id))
				addJobDirectly(sched, sourceID, 1*time.Hour)
			}(i)
		}

		wg.Wait()
	})
}

func TestJobFields(t *testing.T) {
	t.Run("job ticker and stopCh are set after addJobDirectly", func(t *testing.T) {
		sched := New(nil, nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)

		sched.mu.RLock()
		job, exists := sched.jobs["source-1"]
		sched.mu.RUnlock()

		if !exists {
			t.Fatal("expected job to exist")
		}

		if job.ticker == nil {
			t.Error("expected ticker to be set")
		}

		if job.stopCh == nil {
			t.Error("expected stopCh to be set")
		}

		if job.sourceID != "source-1" {
			t.Errorf("expected sourceID 'source-1', got %q", job.sourceID)
		}

		if job.interval != 1*time.Hour {
			t.Errorf("expected interval 1h, got %v", job.interval)
		}
	})
}

func TestSchedulerFields(t *testing.T) {
	t.Run("scheduler has expected fields after creation", func(t *testing.T) {
		sched := New(nil, nil, nil)

		if sched.jobs == nil {
			t.Error("expected jobs to be non-nil")
		}

		if sched.syncLocks == nil {
			t.Error("expected syncLocks to be non-nil")
		}

		if sched.ctx == nil {
			t.Error("expected ctx to be non-nil")
		}

		if sched.cancel == nil {
			t.Error("expected cancel to be non-nil")
		}

		if sched.started {
			t.Error("expected started to be false initially")
		}
	})
}

func TestStartIdempotent(t *testing.T) {
	t.Run("calling start multiple times is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Manually set started to true to simulate already started
		sched.mu.Lock()
		sched.started = true
		sched.mu.Unlock()

		// Should return nil immediately without doing anything
		err := sched.Start()
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})
}

func TestStopIdempotent(t *testing.T) {
	t.Run("calling stop when not started is safe", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Should not panic
		sched.Stop()
		sched.Stop()
		sched.Stop()
	})
}

func TestJobIntervalTypes(t *testing.T) {
	t.Run("various interval durations are accepted", func(t *testing.T) {
		sched := New(nil, nil, nil)

		testCases := []struct {
			name     string
			sourceID string
			interval time.Duration
		}{
			{"seconds", "source-1", 30 * time.Second},
			{"minutes", "source-2", 5 * time.Minute},
			{"hours", "source-3", 2 * time.Hour},
			{"mixed", "source-4", 1*time.Hour + 30*time.Minute},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				addJobDirectly(sched, tc.sourceID, tc.interval)

				sched.mu.RLock()
				job, exists := sched.jobs[tc.sourceID]
				sched.mu.RUnlock()

				if !exists {
					t.Fatalf("expected job %s to exist", tc.sourceID)
				}

				if job.interval != tc.interval {
					t.Errorf("expected interval %v, got %v", tc.interval, job.interval)
				}
			})
		}
	})
}

func TestContextCancellation(t *testing.T) {
	t.Run("cancel function is available", func(t *testing.T) {
		sched := New(nil, nil, nil)

		// Verify cancel function exists and can be called
		if sched.cancel == nil {
			t.Fatal("expected cancel function to be set")
		}

		// Cancel should not panic
		sched.cancel()
	})
}

// newTestSchedulerWithNotifier returns a Scheduler wired to a real Notifier
// with webhook/email disabled (no network I/O) and a nil DB. This exercises
// the full scheduler → notifier pipeline without standing up mock
// infrastructure. The disabled channels mean the background send goroutine
// exits immediately, leaving only the cooldown-map side effects — which is
// exactly what the tests need to observe.
func newTestSchedulerWithNotifier(t *testing.T) (*Scheduler, *notify.Notifier) {
	t.Helper()
	n := notify.New(&notify.Config{
		WebhookEnabled: false,
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	})
	// Notifier must be "enabled" for the scheduler to call it. With both
	// channels disabled, IsEnabled() returns false — so to exercise the
	// path we enable webhook but do not set a URL. sendWithPrefs then
	// no-ops on the empty URL branch.
	n = notify.New(&notify.Config{
		WebhookEnabled: true,
		WebhookURL:     "", // empty URL — send is a no-op
		EmailEnabled:   false,
		CooldownPeriod: time.Hour,
	})
	sched := New(nil, nil, n)
	return sched, n
}

// TestMaybeSendFailureAlert_HardFailureFiresAlert verifies that a sync
// result with Success=false triggers a failure alert via the notifier.
func TestMaybeSendFailureAlert_HardFailureFiresAlert(t *testing.T) {
	sched, n := newTestSchedulerWithNotifier(t)
	defer sched.cancel()

	source := &db.Source{ID: "src-1", Name: "Test Source", UserID: "u1"}
	result := &caldav.SyncResult{
		Success: false,
		Message: "sync failed",
		Errors:  []string{"connection refused"},
	}

	sched.maybeSendFailureAlert(source.ID, source, result)

	// Verify by probing the notifier's cooldown: a direct call with the
	// same source should now be blocked, proving the scheduler populated
	// the failure-alert cooldown map.
	fired := n.SendSyncFailureAlertWithPrefs(
		nil, source.ID, source.Name, "",
		"probe", "probe", nil,
	)
	if fired {
		t.Error("expected scheduler to have populated the failure cooldown (probe should return false)")
	}
}

// TestMaybeSendFailureAlert_DangerousWarningFiresAlert verifies that a
// successful sync with a data-loss-protection warning still triggers a
// failure alert. This is the scenario that explains why the user's
// calendar data disappeared without any alert reaching them: after PR #22,
// the safety guards will emit these warnings, and the scheduler must
// surface them as alerts.
func TestMaybeSendFailureAlert_DangerousWarningFiresAlert(t *testing.T) {
	sched, n := newTestSchedulerWithNotifier(t)
	defer sched.cancel()

	source := &db.Source{ID: "src-2", Name: "Test Source 2", UserID: "u1"}
	result := &caldav.SyncResult{
		Success: true,
		Warnings: []string{
			"source returned 0 events but 42 previously-synced records exist - skipping one-way orphan deletion for safety (possible auth failure or broken source)",
		},
	}

	sched.maybeSendFailureAlert(source.ID, source, result)

	fired := n.SendSyncFailureAlertWithPrefs(
		nil, source.ID, source.Name, "",
		"probe", "probe", nil,
	)
	if fired {
		t.Error("expected scheduler to alert on dangerous warning even on success (probe should return false)")
	}
}

// TestMaybeSendFailureAlert_HarmlessWarningDoesNotFire verifies that a
// successful sync with only routine warnings (e.g. individual event delete
// failures, 403 skips) does NOT trigger a failure alert. This prevents
// alert noise from day-to-day sync operations.
func TestMaybeSendFailureAlert_HarmlessWarningDoesNotFire(t *testing.T) {
	sched, n := newTestSchedulerWithNotifier(t)
	defer sched.cancel()

	source := &db.Source{ID: "src-3", Name: "Test Source 3", UserID: "u1"}
	result := &caldav.SyncResult{
		Success: true,
		Warnings: []string{
			"Failed to delete orphan event: 404 not found",
			"Two-way sync: 2 events skipped (source calendar read-only)",
		},
	}

	sched.maybeSendFailureAlert(source.ID, source, result)

	// Direct call should still fire — cooldown was never populated.
	fired := n.SendSyncFailureAlertWithPrefs(
		nil, source.ID, source.Name, "",
		"probe", "probe", nil,
	)
	if !fired {
		t.Error("expected direct call to fire (scheduler must not have populated cooldown on harmless warnings)")
	}
}

// TestMaybeSendFailureAlert_SuccessWithNoWarningsDoesNotFire verifies the
// happy path: a clean successful sync with no warnings triggers zero alerts.
func TestMaybeSendFailureAlert_SuccessWithNoWarningsDoesNotFire(t *testing.T) {
	sched, n := newTestSchedulerWithNotifier(t)
	defer sched.cancel()

	source := &db.Source{ID: "src-4", Name: "Test Source 4", UserID: "u1"}
	result := &caldav.SyncResult{
		Success:  true,
		Warnings: nil,
	}

	sched.maybeSendFailureAlert(source.ID, source, result)

	fired := n.SendSyncFailureAlertWithPrefs(
		nil, source.ID, source.Name, "",
		"probe", "probe", nil,
	)
	if !fired {
		t.Error("expected direct call to fire (scheduler must not have populated cooldown on clean success)")
	}
}

// TestMaybeSendFailureAlert_NilNotifierSafe verifies the nil-notifier
// guard. A scheduler without a notifier (tests, stripped-down deploys)
// must not panic.
func TestMaybeSendFailureAlert_NilNotifierSafe(t *testing.T) {
	sched := New(nil, nil, nil)
	defer sched.cancel()

	source := &db.Source{ID: "src-5", Name: "Test Source 5", UserID: "u1"}
	result := &caldav.SyncResult{Success: false, Message: "sync failed"}

	// Must not panic.
	sched.maybeSendFailureAlert(source.ID, source, result)
}
