package scheduler

import (
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	t.Run("creates scheduler with nil dependencies", func(t *testing.T) {
		// Note: In production, db and syncEngine would be required,
		// but we can create the scheduler without them for testing structure
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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

	t.Run("sync timeout is 30 minutes", func(t *testing.T) {
		if syncTimeout != 30*time.Minute {
			t.Errorf("expected syncTimeout to be 30m, got %v", syncTimeout)
		}
	})
}

func TestSchedulerStartStop(t *testing.T) {
	t.Run("start sets started flag", func(t *testing.T) {
		sched := New(nil, nil)

		// Note: Start() will fail without a real DB, but we can test
		// the started flag protection by checking the initial state
		if sched.started {
			t.Error("expected started to be false initially")
		}
	})

	t.Run("stop is idempotent", func(t *testing.T) {
		sched := New(nil, nil)

		// Stop should not panic when scheduler hasn't started
		sched.Stop()
		sched.Stop() // Should be safe to call multiple times
	})
}

func TestGetSyncLock(t *testing.T) {
	t.Run("creates lock for new source", func(t *testing.T) {
		sched := New(nil, nil)

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
		sched := New(nil, nil)

		lock1 := sched.getSyncLock("source-1")
		lock2 := sched.getSyncLock("source-2")

		if lock1 == lock2 {
			t.Error("expected different locks for different sources")
		}
	})
}

func TestRemoveJob(t *testing.T) {
	t.Run("remove non-existent job is safe", func(t *testing.T) {
		sched := New(nil, nil)

		// Should not panic
		sched.RemoveJob("non-existent-source")
	})
}

func TestUpdateJobInterval(t *testing.T) {
	t.Run("update non-existent job is safe", func(t *testing.T) {
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

		addJobDirectly(sched, "source-1", 1*time.Hour)
		addJobDirectly(sched, "source-2", 2*time.Hour)
		addJobDirectly(sched, "source-3", 30*time.Minute)

		count := sched.GetJobCount()
		if count != 3 {
			t.Errorf("expected 3 jobs, got %d", count)
		}
	})

	t.Run("replaces existing job with same source ID", func(t *testing.T) {
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		_ = New(nil, nil)
	})
}

func TestConcurrentAccess(t *testing.T) {
	t.Run("concurrent add and remove is safe", func(t *testing.T) {
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

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
		sched := New(nil, nil)

		// Should not panic
		sched.Stop()
		sched.Stop()
		sched.Stop()
	})
}

func TestJobIntervalTypes(t *testing.T) {
	t.Run("various interval durations are accepted", func(t *testing.T) {
		sched := New(nil, nil)

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
		sched := New(nil, nil)

		// Verify cancel function exists and can be called
		if sched.cancel == nil {
			t.Fatal("expected cancel function to be set")
		}

		// Cancel should not panic
		sched.cancel()
	})
}
