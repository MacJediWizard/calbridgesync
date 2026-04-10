package scheduler

import (
	"testing"
	"time"
)

// TestHeartbeat_RecordsTimestamp verifies the core heartbeat map
// contract: calling heartbeat(name) stores a timestamp approximately
// equal to time.Now(), retrievable via lastHeartbeat(name).
func TestHeartbeat_RecordsTimestamp(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	before := time.Now()
	s.heartbeat("test.routine")
	after := time.Now()

	recorded := s.lastHeartbeat("test.routine")
	if recorded.Before(before) || recorded.After(after) {
		t.Errorf("heartbeat timestamp %v not in [%v, %v]", recorded, before, after)
	}
}

// TestHeartbeat_UnknownRoutineReturnsZero verifies that
// lastHeartbeat returns the zero time for a routine that has never
// registered a heartbeat. The zero time is used downstream to
// distinguish "no data" from "very old data".
func TestHeartbeat_UnknownRoutineReturnsZero(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	last := s.lastHeartbeat("never.beat")
	if !last.IsZero() {
		t.Errorf("expected zero time for unknown routine, got %v", last)
	}
}

// TestExpectedHeartbeatThreshold_BuiltinRoutines verifies the
// threshold lookup for the known long-running routines.
func TestExpectedHeartbeatThreshold_BuiltinRoutines(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	tests := []struct {
		name string
		want time.Duration
	}{
		{routineStaleDetection, watchdogStaleDetectionThreshold},
		{routineCleanup, watchdogCleanupThreshold},
		{routineHealthLog, watchdogHealthLogThreshold},
		{routineWatchdog, watchdogWatchdogSelfThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.expectedHeartbeatThreshold(tt.name)
			if got != tt.want {
				t.Errorf("expectedHeartbeatThreshold(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestExpectedHeartbeatThreshold_PerJobUsesJobInterval verifies that
// the threshold for a per-job sync loop is computed from the job's
// configured interval. This prevents a job with a 10-hour interval
// from being flagged as stale just because its heartbeat is older
// than the default 3-minute stale-detection threshold.
func TestExpectedHeartbeatThreshold_PerJobUsesJobInterval(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	// Inject a job with a known interval.
	interval := 10 * time.Minute
	s.mu.Lock()
	s.jobs["src1"] = &Job{
		sourceID: "src1",
		interval: interval,
	}
	s.mu.Unlock()

	got := s.expectedHeartbeatThreshold(routineJobName("src1"))
	want := 2*interval + watchdogJobSlack
	if got != want {
		t.Errorf("per-job threshold = %v, want %v", got, want)
	}
}

// TestExpectedHeartbeatThreshold_UnknownJobFallsBackToDefault
// verifies that looking up a job that isn't in the scheduler's jobs
// map returns a safe conservative default instead of zero. Zero
// would cause every heartbeat to immediately look stale.
func TestExpectedHeartbeatThreshold_UnknownJobFallsBackToDefault(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	got := s.expectedHeartbeatThreshold("scheduler.runJob.unknown")
	if got <= 0 {
		t.Errorf("expected positive default threshold, got %v", got)
	}
}

// TestCheckLiveness_NoEntriesDoesNotPanic verifies the empty-state
// edge case. If no routine has heartbeat yet (e.g., scheduler just
// started and first ticks haven't fired), checkLiveness should be a
// no-op.
func TestCheckLiveness_NoEntriesDoesNotPanic(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	// Should return without panic
	s.checkLiveness()
}

// TestCheckLiveness_FreshHeartbeatsNotFlagged verifies the happy
// path: when all heartbeats are recent, checkLiveness logs nothing
// and fires no alert. We verify indirectly by confirming no stale
// heartbeats remain in an alert-bearing state.
func TestCheckLiveness_FreshHeartbeatsNotFlagged(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	s.heartbeat(routineStaleDetection)
	s.heartbeat(routineCleanup)
	s.heartbeat(routineHealthLog)

	// checkLiveness walks the map and emits warnings for stale
	// entries. With all fresh entries, the scan should complete
	// quickly and fire no alerts. We can't directly observe the
	// absence of a log line, but we can confirm the function
	// doesn't hang or panic.
	s.checkLiveness()
}

// TestCheckLiveness_StaleHeartbeatIsDetected verifies that a
// heartbeat older than its threshold is detected. We manipulate the
// heartbeat map directly to install a timestamp far in the past.
// Without a mockable notifier we can't observe the alert firing
// from this test level, but we can verify the stale-detection
// logic runs without error and leaves the heartbeat map unchanged
// (checkLiveness only reads, doesn't write).
func TestCheckLiveness_StaleHeartbeatIsDetected(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	// Install a stale heartbeat: older than the stale-detection
	// routine's threshold.
	stale := time.Now().Add(-2 * watchdogStaleDetectionThreshold)
	s.heartbeatsMu.Lock()
	s.heartbeats[routineStaleDetection] = stale
	s.heartbeatsMu.Unlock()

	// Should log a [WATCHDOG] warning but not panic. No notifier
	// is configured, so the alert-firing branch is skipped.
	s.checkLiveness()

	// Heartbeat must still be in the map (checkLiveness is read-only).
	if !s.lastHeartbeat(routineStaleDetection).Equal(stale) {
		t.Error("checkLiveness unexpectedly mutated the heartbeat map")
	}
}

// TestClearJobHeartbeat_RemovesEntry verifies that when a job
// goroutine exits (via stopCh close or ctx cancel), its heartbeat
// entry is removed from the map. Without this, a legitimately
// stopped job would leave a stale heartbeat and the watchdog would
// eventually flag it as dead and fire a false alert.
func TestClearJobHeartbeat_RemovesEntry(t *testing.T) {
	s := New(nil, nil, nil)
	defer s.cancel()

	sourceID := "src-to-remove"
	name := routineJobName(sourceID)

	s.heartbeat(name)
	if s.lastHeartbeat(name).IsZero() {
		t.Fatal("heartbeat should be recorded")
	}

	s.clearJobHeartbeat(sourceID)
	if !s.lastHeartbeat(name).IsZero() {
		t.Error("clearJobHeartbeat should remove the entry")
	}
}

// TestRoutineJobNameRoundTrip verifies the naming convention used
// by routineJobName so that expectedHeartbeatThreshold's "scheduler.runJob."
// prefix check works correctly for any source ID.
func TestRoutineJobNameRoundTrip(t *testing.T) {
	tests := []string{
		"simple-id",
		"uuid-1234-5678",
		"id.with.dots",
		"",
	}
	for _, sourceID := range tests {
		t.Run(sourceID, func(t *testing.T) {
			name := routineJobName(sourceID)
			if name == "" {
				t.Errorf("routineJobName(%q) returned empty", sourceID)
			}
			// Must be recognizable by the expectedHeartbeatThreshold
			// prefix check. Empty sourceID is a weird edge case but
			// shouldn't produce a false match on the per-job branch
			// without a corresponding job entry.
			if len(sourceID) > 0 {
				if got := name; got != "scheduler.runJob."+sourceID {
					t.Errorf("routineJobName(%q) = %q, want %q", sourceID, got, "scheduler.runJob."+sourceID)
				}
			}
		})
	}
}
