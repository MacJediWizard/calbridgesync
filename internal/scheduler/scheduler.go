package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/db"
	"github.com/macjediwizard/calbridgesync/internal/notify"
)

const (
	cleanupInterval         = 24 * time.Hour
	defaultLogRetentionDays = 30
	syncTimeout             = 120 * time.Minute // Maximum time for a single sync operation (2 hours for slow iCloud with multiple calendars)
	healthLogInterval       = 5 * time.Minute   // Interval for scheduler health logging
	staleMultiplier         = 2                 // Source is stale if last sync > staleMultiplier * interval
	startupStagger          = 30 * time.Second  // Delay between starting each source's first sync

	// Liveness watchdog constants (Issue #43). The watchdog detects
	// routines that have crashed (caught by PR #38 panic recovery but
	// whose goroutine then exited) OR that have hung (infinite loop,
	// deadlock, stuck CalDAV call).
	watchdogInterval = 1 * time.Minute

	// Per-routine stale thresholds. A routine whose last heartbeat is
	// older than its threshold is considered dead/hung by the watchdog.
	// Thresholds are set to ~2x the routine's tick interval plus slack
	// so normal ticking doesn't trigger false positives.
	watchdogStaleDetectionThreshold = 3 * time.Minute  // tick = 1 min
	watchdogCleanupThreshold        = 26 * time.Hour   // tick = 24 hr
	watchdogHealthLogThreshold      = 12 * time.Minute // tick = 5 min
	watchdogWatchdogSelfThreshold   = 3 * time.Minute  // tick = 1 min, covers watchdog itself
	watchdogJobSlack                = 60 * time.Second // added to 2x job interval for per-job heartbeat threshold
)

// Routine name constants for the liveness heartbeat map. Using constants
// instead of string literals so typos are compile errors and the watchdog
// check knows every name it's supposed to track.
const (
	routineStaleDetection = "scheduler.staleDetectionRoutine"
	routineCleanup        = "scheduler.cleanupRoutine"
	routineHealthLog      = "scheduler.healthLogRoutine"
	routineWatchdog       = "scheduler.watchdogRoutine"
)

// routineJobName returns the heartbeat key for a per-job sync loop.
// Each source has its own heartbeat slot keyed by source ID so the
// watchdog can identify exactly which job's loop has stopped.
func routineJobName(sourceID string) string {
	return "scheduler.runJob." + sourceID
}

// Job represents a scheduled sync job.
type Job struct {
	sourceID   string
	interval   time.Duration
	ticker     *time.Ticker
	stopCh     chan struct{}
	nextSyncAt time.Time
}

// consecutiveSkipWarnThreshold is the number of consecutive
// executeSync skips on the same source that triggers a WARNING log
// line. Each skip happens because the previous sync for that source
// is still holding its per-source mutex — either legitimately slow
// or hung. The liveness watchdog (#43) catches fully frozen
// goroutines on a longer horizon; this counter catches the
// slow-but-not-frozen case earlier so operators have a chance to
// intervene (widen sync_interval, check CalDAV server health)
// before the watchdog alert fires. Tunable via future env var if
// needed; 3 is an empirical default — one skip is noise, three in
// a row means the sync interval is too tight or something is
// genuinely stuck. (#93)
const consecutiveSkipWarnThreshold = 3

// Scheduler manages background sync jobs.
type Scheduler struct {
	db         *db.DB
	syncEngine *caldav.SyncEngine
	notifier   *notify.Notifier

	mu        sync.RWMutex
	jobs      map[string]*Job
	syncLocks map[string]*sync.Mutex // Per-source locks to prevent concurrent syncs
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool

	// heartbeats tracks the last time each long-running goroutine made
	// progress (Issue #43). The watchdog reads this map periodically
	// and flags routines whose last heartbeat is older than their
	// configured threshold. Separate mutex from mu to avoid contention
	// on the hot path of job lookups.
	heartbeatsMu sync.RWMutex
	heartbeats   map[string]time.Time

	// skipCounts tracks consecutive executeSync skips per source so
	// the scheduler can escalate from a normal-volume log line to a
	// WARNING after consecutiveSkipWarnThreshold in a row. Reset to
	// zero every time a source successfully acquires its sync lock.
	// Separate mutex from mu to keep the hot path of job lookups
	// uncontended. (#93)
	skipCountsMu sync.Mutex
	skipCounts   map[string]int

	// logRetentionDays is the number of days to keep sync logs before
	// the daily cleanup routine purges them. Configurable via
	// SYNC_LOG_RETENTION_DAYS env var; defaults to 30. (#136)
	logRetentionDays int

	// authFailCounts tracks consecutive authentication failures per
	// source so the scheduler can alert when credentials may have
	// expired. Reset on successful sync. (#136)
	authFailCountsMu sync.Mutex
	authFailCounts   map[string]int
}

// New creates a new scheduler. logRetentionDays controls how many
// days of sync logs the daily cleanup routine keeps. Pass 0 to use
// the default (30 days).
func New(database *db.DB, syncEngine *caldav.SyncEngine, notifier *notify.Notifier, logRetentionDays ...int) *Scheduler {
	retention := defaultLogRetentionDays
	if len(logRetentionDays) > 0 && logRetentionDays[0] > 0 {
		retention = logRetentionDays[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db:               database,
		syncEngine:       syncEngine,
		notifier:         notifier,
		logRetentionDays: retention,
		jobs:             make(map[string]*Job),
		syncLocks:        make(map[string]*sync.Mutex),
		ctx:              ctx,
		cancel:           cancel,
		heartbeats:       make(map[string]time.Time),
		skipCounts:       make(map[string]int),
		authFailCounts:   make(map[string]int),
	}
}

// incrementSkipCount bumps the consecutive skip count for a source
// and returns the new value. Safe for concurrent callers. (#93)
func (s *Scheduler) incrementSkipCount(sourceID string) int {
	s.skipCountsMu.Lock()
	defer s.skipCountsMu.Unlock()
	s.skipCounts[sourceID]++
	return s.skipCounts[sourceID]
}

// resetSkipCount zeros the consecutive skip count for a source.
// Called when the source successfully acquires its sync lock so a
// single-cycle slowdown doesn't leave a stale counter behind. Safe
// for concurrent callers. (#93)
func (s *Scheduler) resetSkipCount(sourceID string) {
	s.skipCountsMu.Lock()
	defer s.skipCountsMu.Unlock()
	delete(s.skipCounts, sourceID)
}

// authFailureAlertThreshold is how many consecutive auth failures
// must occur before the scheduler sends a credential-expiry alert.
// Fires exactly once at the threshold; subsequent failures are
// logged but don't re-alert (the regular sync failure alert
// covers those). Reset on any successful sync. (#136)
const authFailureAlertThreshold = 3

// incrementAuthFailCount bumps the auth failure counter and returns
// the new value. Safe for concurrent callers.
func (s *Scheduler) incrementAuthFailCount(sourceID string) int {
	s.authFailCountsMu.Lock()
	defer s.authFailCountsMu.Unlock()
	s.authFailCounts[sourceID]++
	return s.authFailCounts[sourceID]
}

// resetAuthFailCount zeros the auth failure counter for a source.
// Called on every successful sync.
func (s *Scheduler) resetAuthFailCount(sourceID string) {
	s.authFailCountsMu.Lock()
	defer s.authFailCountsMu.Unlock()
	delete(s.authFailCounts, sourceID)
}

// isAuthError returns true if the sync result looks like it failed
// due to an authentication or authorization problem. Uses substring
// matching against the error strings because CalDAV servers report
// auth failures inconsistently.
func (s *Scheduler) isAuthError(result *caldav.SyncResult) bool {
	check := func(text string) bool {
		lower := strings.ToLower(text)
		return strings.Contains(lower, "401") ||
			strings.Contains(lower, "403") ||
			strings.Contains(lower, "unauthorized") ||
			strings.Contains(lower, "forbidden") ||
			strings.Contains(lower, "authentication failed") ||
			strings.Contains(lower, "access denied") ||
			strings.Contains(lower, "invalid credentials")
	}
	if check(result.Message) {
		return true
	}
	for _, e := range result.Errors {
		if check(e) {
			return true
		}
	}
	return false
}

// sendCredentialExpiryAlert fires a one-shot alert when a source
// hits authFailureAlertThreshold consecutive auth failures. Uses
// the existing notifier with a distinctive message so operators
// can tell "credentials expired" apart from "server down."
func (s *Scheduler) sendCredentialExpiryAlert(source *db.Source, consecutiveFailures int) {
	if s.notifier == nil {
		return
	}
	userEmail := ""
	if user, err := s.db.GetUserByID(source.UserID); err == nil {
		userEmail = user.Email
	}
	userPrefs := s.getUserAlertPrefs(source.UserID)
	msg := fmt.Sprintf("Credentials may be expired for source '%s' — %d consecutive authentication failures. Re-enter credentials in the web UI.",
		source.Name, consecutiveFailures)
	s.notifier.SendSyncFailureAlertWithPrefs(
		s.ctx, source.ID, source.Name, userEmail,
		msg, "Check if the app password or OAuth token was revoked.", userPrefs,
	)
}

// Start loads all enabled sources and starts their sync jobs.
func (s *Scheduler) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()

	// Reset any "running" statuses from previous interrupted runs
	if count, err := s.db.ResetRunningSyncStatuses(); err != nil {
		log.Printf("Warning: failed to reset running sync statuses: %v", err)
	} else if count > 0 {
		log.Printf("Reset %d interrupted sync(s) from previous run", count)
	}

	// Load all enabled sources
	sources, err := s.db.GetEnabledSources()
	if err != nil {
		return err
	}

	// Start jobs with staggered initial sync to avoid resource contention
	for i, source := range sources {
		interval := time.Duration(source.SyncInterval) * time.Second
		stagger := time.Duration(i) * startupStagger
		s.AddJobWithDelay(source.ID, interval, stagger)
	}

	// Start cleanup goroutine
	s.wg.Add(1)
	go s.cleanupRoutine()

	// Start health logging goroutine
	s.wg.Add(1)
	go s.healthLogRoutine()

	// Start stale detection goroutine
	s.wg.Add(1)
	go s.staleDetectionRoutine()

	// Start liveness watchdog goroutine (Issue #43). Runs AFTER the
	// routines it monitors so their heartbeats have time to populate.
	s.wg.Add(1)
	go s.watchdogRoutine()

	log.Printf("Scheduler started with %d jobs", len(sources))
	return nil
}

// heartbeat records that the named routine has made progress. Called
// at the top of each tick iteration of long-running routines so the
// watchdog can detect routines that have crashed or hung.
func (s *Scheduler) heartbeat(name string) {
	s.heartbeatsMu.Lock()
	s.heartbeats[name] = time.Now()
	s.heartbeatsMu.Unlock()
}

// lastHeartbeat returns the timestamp of the last heartbeat for the
// named routine, or the zero time if the routine has never beat.
// Exported only via tests (lower-case name).
func (s *Scheduler) lastHeartbeat(name string) time.Time {
	s.heartbeatsMu.RLock()
	defer s.heartbeatsMu.RUnlock()
	return s.heartbeats[name]
}

// expectedHeartbeatThreshold returns the max age for a routine's heartbeat
// before it is considered dead/hung by the watchdog. Per-job sync loops
// use 2x their configured interval plus a slack constant; other routines
// use their own constants.
func (s *Scheduler) expectedHeartbeatThreshold(name string) time.Duration {
	switch name {
	case routineStaleDetection:
		return watchdogStaleDetectionThreshold
	case routineCleanup:
		return watchdogCleanupThreshold
	case routineHealthLog:
		return watchdogHealthLogThreshold
	case routineWatchdog:
		return watchdogWatchdogSelfThreshold
	}
	// Per-job sync loops: look up the job's interval.
	if strings.HasPrefix(name, "scheduler.runJob.") {
		sourceID := strings.TrimPrefix(name, "scheduler.runJob.")
		s.mu.RLock()
		job, ok := s.jobs[sourceID]
		s.mu.RUnlock()
		if ok {
			return 2*job.interval + watchdogJobSlack
		}
	}
	// Unknown routine — conservative default
	return 5 * time.Minute
}

// watchdogRoutine periodically checks that long-running scheduler
// goroutines are still ticking. If a routine's last heartbeat is older
// than its configured threshold, the watchdog logs a warning and
// (if a notifier is configured) fires a failure alert so the operator
// knows the daemon's background work has degraded.
//
// This closes the observability gap that PR #38's panic recovery
// cannot cover: a routine that panics is caught and its goroutine
// exits cleanly, but the scheduler keeps running with one fewer
// routine. Without this watchdog, a crashed staleDetectionRoutine
// would cause stale detection to silently stop working indefinitely.
//
// The watchdog also catches the non-panic failure mode: a routine
// that is stuck in an infinite loop, deadlocked on a mutex, or
// blocked on a misbehaving CalDAV call that never returns.
func (s *Scheduler) watchdogRoutine() {
	defer s.wg.Done()
	defer recoverPanic(routineWatchdog)

	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	// Beat once at startup so checkLiveness doesn't immediately
	// flag the watchdog itself as stale on the very first tick.
	s.heartbeat(routineWatchdog)

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.heartbeat(routineWatchdog)
			s.checkLiveness()
		}
	}
}

// checkLiveness scans the heartbeat map and emits a warning + alert
// for any routine whose last heartbeat is older than its configured
// threshold. Only tracked routines (those present in the heartbeats
// map) are checked — routines that never registered a heartbeat are
// assumed to not exist yet (e.g., the scheduler just started and the
// routine hasn't run its first tick).
func (s *Scheduler) checkLiveness() {
	now := time.Now()

	s.heartbeatsMu.RLock()
	// Copy the entries so we don't hold the lock across the alert call.
	snapshot := make(map[string]time.Time, len(s.heartbeats))
	for k, v := range s.heartbeats {
		snapshot[k] = v
	}
	s.heartbeatsMu.RUnlock()

	for name, lastBeat := range snapshot {
		threshold := s.expectedHeartbeatThreshold(name)
		age := now.Sub(lastBeat)
		if age <= threshold {
			continue
		}

		log.Printf("[WATCHDOG] routine %q last heartbeat %v ago (threshold %v) — may be crashed or hung",
			name, age.Round(time.Second), threshold)

		// Fire a failure alert so the operator sees this in their
		// alert channel. Uses the per-user-prefs failure alert API
		// (PR #24) with a synthetic sourceID derived from the routine
		// name so the notifier's per-source cooldown + in-flight
		// dedup (PR #34) apply per routine, not across all watchdog
		// events. This is the semantic upgrade from the original
		// PR #50 implementation which used SendStaleAlert (now
		// deleted in Issue #59) — a stuck routine is a failure, not
		// a "stale data" condition.
		if s.notifier != nil && s.notifier.IsEnabled() {
			watchdogSourceID := "watchdog:" + name
			message := fmt.Sprintf("Scheduler routine %q is not responsive", name)
			details := fmt.Sprintf("Last heartbeat was %v ago (threshold %v). Routine may have crashed or hung.",
				age.Round(time.Second), threshold)
			s.notifier.SendSyncFailureAlertWithPrefs(
				s.ctx, watchdogSourceID, name, "",
				message, details, nil,
			)
		}
	}
}

// stopDrainTimeout is the maximum time Stop will wait for in-flight
// sync goroutines to drain after context cancellation. Exceeding it
// logs a warning and returns anyway so the caller (typically main's
// signal handler) is not blocked indefinitely on a stuck sync.
//
// Chosen smaller than the main.go shutdownTimeout (30s) so the
// scheduler drain completes before the HTTP server's graceful
// shutdown timer starts. 20s gives in-flight CalDAV operations
// one or two more seconds of grace after s.cancel() propagates,
// then surrenders — the alternative (no timeout) could block
// main for up to syncTimeout = 2 hours if a sync ignores
// context cancellation. (#133)
//
// Declared as var (not const) so tests can override with a very
// short duration to exercise the timeout path in reasonable wall-
// clock time. Production code never writes this value.
var stopDrainTimeout = 20 * time.Second

// Stop gracefully shuts down all jobs. Bounded by stopDrainTimeout
// so the caller is not blocked indefinitely if an in-flight sync
// fails to honor context cancellation.
//
// The process: cancel the scheduler's root context (which cascades
// into every in-flight SyncSource via derived contexts), close job
// stop channels, then wait for goroutines to return. Any goroutine
// still running after stopDrainTimeout is left to its own devices
// — the process is about to exit anyway, and the OS will reclaim
// its resources.
//
// Before #133 this function called s.wg.Wait() with no timeout.
// With syncTimeout = 2 hours, a single stuck sync could block
// Stop() for that entire window, causing Kubernetes / Docker to
// SIGKILL the pod (typical grace periods are 30s) and losing any
// in-flight state that a proper drain would have preserved.
// Bounding the wait is the operational fix: log loudly, surrender,
// let the process exit cleanly within the orchestrator's budget.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	s.mu.Unlock()

	// Cancel context to stop all jobs. This cascades into every
	// in-flight SyncSource via the context chain in executeSync
	// (ctx, cancel := context.WithTimeout(s.ctx, syncTimeout))
	// so HTTP operations in flight should unblock within their
	// current request's remaining timeout.
	s.cancel()

	// Stop all job tickers
	s.mu.Lock()
	for _, job := range s.jobs {
		close(job.stopCh)
		job.ticker.Stop()
	}
	s.jobs = make(map[string]*Job)
	s.mu.Unlock()

	// Wait for all goroutines to finish, bounded by
	// stopDrainTimeout so we can never block the caller longer
	// than the outer shutdown grace period. (#133)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("Scheduler stopped")
	case <-time.After(stopDrainTimeout):
		log.Printf("WARNING: scheduler Stop() drain timeout (%v) exceeded — forcing shutdown with in-flight goroutines still running. Check sync_timeout settings or investigate stuck CalDAV operations.", stopDrainTimeout)
	}
}

// AddJob adds or replaces a sync job for a source.
func (s *Scheduler) AddJob(sourceID string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing job if any
	if existingJob, exists := s.jobs[sourceID]; exists {
		close(existingJob.stopCh)
		existingJob.ticker.Stop()
	}

	// Create new job
	// nextSyncAt is set to now since job runs immediately, then updated after sync completes
	job := &Job{
		sourceID:   sourceID,
		interval:   interval,
		ticker:     time.NewTicker(interval),
		stopCh:     make(chan struct{}),
		nextSyncAt: time.Now(), // Will be updated after first sync
	}

	s.jobs[sourceID] = job

	// Start job goroutine
	s.wg.Add(1)
	go s.runJob(job)

	log.Printf("Added sync job for source %s with interval %v", sourceID, interval)
}

// AddJobWithDelay adds a sync job with a delayed initial sync.
// This is used to stagger sync starts and avoid resource contention.
func (s *Scheduler) AddJobWithDelay(sourceID string, interval time.Duration, initialDelay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing job if any
	if existingJob, exists := s.jobs[sourceID]; exists {
		close(existingJob.stopCh)
		existingJob.ticker.Stop()
	}

	// Create new job with delayed start
	job := &Job{
		sourceID:   sourceID,
		interval:   interval,
		ticker:     time.NewTicker(interval),
		stopCh:     make(chan struct{}),
		nextSyncAt: time.Now().Add(initialDelay),
	}

	s.jobs[sourceID] = job

	// Start job goroutine with initial delay
	s.wg.Add(1)
	go s.runJobWithDelay(job, initialDelay)

	log.Printf("Added sync job for source %s with interval %v (starting in %v)", sourceID, interval, initialDelay)
}

// RemoveJob removes a sync job and cleans up associated resources.
func (s *Scheduler) RemoveJob(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.jobs[sourceID]; exists {
		close(job.stopCh)
		job.ticker.Stop()
		delete(s.jobs, sourceID)
		delete(s.syncLocks, sourceID) // Clean up sync lock to prevent memory leak
		log.Printf("Removed sync job for source %s", sourceID)
	}

	// Clear alert state in notifier if configured. Both stale and failure
	// cooldowns must be cleared so a newly-added source with a reused ID
	// (unlikely but possible) starts with a clean slate.
	if s.notifier != nil {
		s.notifier.ClearStaleState(sourceID)
		s.notifier.ClearFailureAlertState(sourceID)
	}
}

// UpdateJobInterval updates the interval for an existing job by stopping and restarting it.
func (s *Scheduler) UpdateJobInterval(sourceID string, interval time.Duration) {
	s.mu.Lock()

	existingJob, exists := s.jobs[sourceID]
	if !exists {
		s.mu.Unlock()
		log.Printf("Updated sync interval for source %s to %v", sourceID, interval)
		return
	}

	// Stop the existing job goroutine and ticker
	close(existingJob.stopCh)
	existingJob.ticker.Stop()
	delete(s.jobs, sourceID)

	// Create new job with updated interval
	job := &Job{
		sourceID:   sourceID,
		interval:   interval,
		ticker:     time.NewTicker(interval),
		stopCh:     make(chan struct{}),
		nextSyncAt: time.Now().Add(interval), // First tick after interval
	}

	s.jobs[sourceID] = job
	s.mu.Unlock()

	// Start job goroutine (don't run immediately - next tick will be at interval from now)
	s.wg.Add(1)
	go s.runJobFromTicker(job)

	log.Printf("Updated sync interval for source %s to %v", sourceID, interval)
}

// TriggerSync manually triggers a sync for a source.
func (s *Scheduler) TriggerSync(sourceID string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer recoverPanic("scheduler.TriggerSync")
		s.executeSync(sourceID)
	}()
}

// GetJobCount returns the number of active jobs.
func (s *Scheduler) GetJobCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}

// runJob runs the sync job loop.
func (s *Scheduler) runJob(job *Job) {
	defer s.wg.Done()
	defer recoverPanic("scheduler.runJob")
	defer s.clearJobHeartbeat(job.sourceID)

	// Initial heartbeat so the watchdog sees the job as alive even
	// if the first executeSync takes longer than the interval.
	s.heartbeat(routineJobName(job.sourceID))

	// Run immediately on start
	s.executeSync(job.sourceID)
	s.updateNextSyncAt(job.sourceID)

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-job.stopCh:
			return
		case <-job.ticker.C:
			s.heartbeat(routineJobName(job.sourceID))
			s.executeSync(job.sourceID)
			s.updateNextSyncAt(job.sourceID)
		}
	}
}

// runJobFromTicker runs the sync job loop, starting from the next ticker tick.
// Used when updating interval - does NOT run immediately.
func (s *Scheduler) runJobFromTicker(job *Job) {
	defer s.wg.Done()
	defer recoverPanic("scheduler.runJobFromTicker")
	defer s.clearJobHeartbeat(job.sourceID)

	s.heartbeat(routineJobName(job.sourceID))

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-job.stopCh:
			return
		case <-job.ticker.C:
			s.heartbeat(routineJobName(job.sourceID))
			s.executeSync(job.sourceID)
			s.updateNextSyncAt(job.sourceID)
		}
	}
}

// runJobWithDelay runs the sync job loop with an initial delay.
func (s *Scheduler) runJobWithDelay(job *Job, initialDelay time.Duration) {
	defer s.wg.Done()
	defer recoverPanic("scheduler.runJobWithDelay")
	defer s.clearJobHeartbeat(job.sourceID)

	s.heartbeat(routineJobName(job.sourceID))

	// Wait for initial delay before first sync
	if initialDelay > 0 {
		select {
		case <-s.ctx.Done():
			return
		case <-job.stopCh:
			return
		case <-time.After(initialDelay):
			// Continue to first sync
		}
	}

	// Run first sync
	s.heartbeat(routineJobName(job.sourceID))
	s.executeSync(job.sourceID)
	s.updateNextSyncAt(job.sourceID)

	// Continue with regular interval
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-job.stopCh:
			return
		case <-job.ticker.C:
			s.heartbeat(routineJobName(job.sourceID))
			s.executeSync(job.sourceID)
			s.updateNextSyncAt(job.sourceID)
		}
	}
}

// clearJobHeartbeat removes the heartbeat entry for a job when its
// goroutine exits (via stopCh close or ctx cancel). Without this, a
// legitimately-stopped job would leave a stale heartbeat and the
// watchdog would eventually flag it as dead and fire a false alert.
func (s *Scheduler) clearJobHeartbeat(sourceID string) {
	s.heartbeatsMu.Lock()
	defer s.heartbeatsMu.Unlock()
	delete(s.heartbeats, routineJobName(sourceID))
}

// updateNextSyncAt updates the next sync time for a job after execution.
func (s *Scheduler) updateNextSyncAt(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.jobs[sourceID]; exists {
		job.nextSyncAt = time.Now().Add(job.interval)
	}
}

// getSyncLock returns the mutex for a source, creating one if needed.
func (s *Scheduler) getSyncLock(sourceID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()

	if lock, exists := s.syncLocks[sourceID]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	s.syncLocks[sourceID] = lock
	return lock
}

// executeSync runs the sync for a source.
//
// Has its own defer recoverPanic even though every caller already
// wraps executeSync in its own recoverPanic (runJob, runJobFromTicker,
// runJobWithDelay, TriggerSync). The caller's recover is enough to
// keep the process alive, but it is NOT enough to keep the caller's
// sync-loop alive: Go's panic propagation unwinds past the enclosing
// `for { select { ... executeSync(...) } }` loop, so a panic in
// sync.go silently kills the entire scheduler loop for that source
// until the daemon restarts. Users silently stop getting syncs.
//
// With an inline recover, a panic inside SyncSource becomes a single
// loud log line, executeSync returns normally, and the caller's
// loop continues on the next tick. If the panic is caused by a
// persistent condition (bad config, libical bug, etc.) every tick
// will repeat the panic — that's intentional noise; it's easier for
// operators to fix a persistently-crashing sync than to notice that
// one source stopped syncing entirely. (#121)
func (s *Scheduler) executeSync(sourceID string) {
	defer recoverPanic(fmt.Sprintf("scheduler.executeSync[%s]", sourceID))

	// Get per-source lock to prevent concurrent syncs
	lock := s.getSyncLock(sourceID)

	// Try to acquire lock without blocking - skip if another sync is in
	// progress. Repeated skips on the same source indicate that a sync
	// cycle is taking longer than the scheduler interval — either a
	// hang the liveness watchdog will catch, or a legitimately slow
	// sync (large calendar, network latency, CalDAV server throttling)
	// that the operator should know about. Track per-source consecutive
	// skip counts and escalate to a WARNING at the threshold so
	// operators have a visible signal before the watchdog fires. (#93)
	if !lock.TryLock() {
		skips := s.incrementSkipCount(sourceID)
		if skips >= consecutiveSkipWarnThreshold {
			log.Printf("WARNING: sync for source %s has been skipped %d consecutive times — previous cycle is still running. Check for hung CalDAV calls or widen sync_interval.", sourceID, skips)
		} else {
			log.Printf("Skipping sync for source %s - another sync is already in progress (consecutive skips: %d)", sourceID, skips)
		}
		return
	}
	s.resetSkipCount(sourceID)
	defer lock.Unlock()

	// Get the source
	source, err := s.db.GetSourceByID(sourceID)
	if err != nil {
		log.Printf("Failed to get source %s: %v", sourceID, err)
		return
	}

	// Skip if disabled
	if !source.Enabled {
		return
	}

	log.Printf("Starting sync for source %s (%s)", source.Name, sourceID)

	// Create a timeout context for this sync operation
	ctx, cancel := context.WithTimeout(s.ctx, syncTimeout)
	defer cancel()

	// Execute sync with timeout context
	result := s.syncEngine.SyncSource(ctx, source)

	if result.Success {
		log.Printf("Sync completed for source %s: %d created, %d updated, %d deleted, %d duplicates removed in %v",
			source.Name, result.Created, result.Updated, result.Deleted, result.DuplicatesRemoved, result.Duration)

		// Reset auth failure counter on success
		s.resetAuthFailCount(sourceID)

		// Send recovery notification if source was previously stale
		if s.notifier != nil && s.notifier.IsEnabled() {
			// Look up user email for per-user notifications
			userEmail := ""
			if user, err := s.db.GetUserByID(source.UserID); err == nil {
				userEmail = user.Email
			}

			// Look up user alert preferences
			userPrefs := s.getUserAlertPrefs(source.UserID)
			s.notifier.SendRecoveryAlertWithPrefs(s.ctx, sourceID, source.Name, userEmail, userPrefs)
		}
	} else {
		log.Printf("Sync failed for source %s: %s", source.Name, result.Message)

		// Track consecutive auth failures. If the sync failed
		// because of authentication (401/403), increment the
		// counter. At the threshold, send an alert so the operator
		// knows credentials may have expired. (#136)
		if s.isAuthError(result) {
			count := s.incrementAuthFailCount(sourceID)
			if count == authFailureAlertThreshold {
				s.sendCredentialExpiryAlert(source, count)
			}
		}
	}

	// Fire a failure alert when appropriate. This covers two cases:
	//   1. The sync itself returned Success=false (hard failure).
	//   2. The sync succeeded but a data-loss protection guard was
	//      triggered, producing a "dangerous" warning in result.Warnings.
	//      These warnings come from planOrphanDeletion in caldav/sync.go
	//      (PR #22 / issue #21) and indicate that the sync refused to
	//      delete events because the inputs looked unsafe. The user MUST
	//      know about these — before PR #22 they would have silently
	//      deleted data; after PR #22 they silently preserve data, but
	//      the underlying problem (broken source, auth expired, etc.)
	//      still needs user attention.
	s.maybeSendFailureAlert(sourceID, source, result)

	// ICS adaptive polling (#146): if the content hash changed,
	// reset to the original interval. If unchanged, double it
	// (up to 8x). This reduces unnecessary polling when an ICS
	// feed is static for long periods.
	if result.Success && result.ContentHash != "" && source.SourceType == db.SourceTypeICS {
		currentInterval := source.SyncInterval
		if source.AdaptiveInterval > 0 {
			currentInterval = source.AdaptiveInterval
		}

		if source.LastContentHash == result.ContentHash {
			// Content unchanged — double the interval (cap at 8x original)
			maxInterval := source.SyncInterval * 8
			newInterval := currentInterval * 2
			if newInterval > maxInterval {
				newInterval = maxInterval
			}
			if newInterval != currentInterval {
				log.Printf("ICS adaptive polling: %s content unchanged, extending interval %ds → %ds",
					source.Name, currentInterval, newInterval)
			}
			if err := s.db.UpdateSourceAdaptiveState(sourceID, result.ContentHash, newInterval); err != nil {
				log.Printf("Failed to update adaptive state: %v", err)
			}
		} else {
			// Content changed — reset to original interval
			if currentInterval != source.SyncInterval {
				log.Printf("ICS adaptive polling: %s content changed, resetting interval to %ds",
					source.Name, source.SyncInterval)
			}
			if err := s.db.UpdateSourceAdaptiveState(sourceID, result.ContentHash, 0); err != nil {
				log.Printf("Failed to update adaptive state: %v", err)
			}
		}
	}
}

// maybeSendFailureAlert inspects a sync result and fires a failure alert if
// the sync failed or if any data-loss protection guard was triggered.
// It respects the notifier's cooldown window — called on every sync, the
// per-source cooldown map prevents alert storms on a persistently broken
// source.
func (s *Scheduler) maybeSendFailureAlert(sourceID string, source *db.Source, result *caldav.SyncResult) {
	if s.notifier == nil || !s.notifier.IsEnabled() {
		return
	}

	var (
		shouldAlert  bool
		alertMessage string
		alertDetails string
	)

	if !result.Success {
		shouldAlert = true
		alertMessage = fmt.Sprintf("Sync failed for source '%s'", source.Name)
		if len(result.Errors) > 0 {
			alertDetails = strings.Join(result.Errors, "\n")
		} else {
			alertDetails = result.Message
		}
	} else {
		// Successful sync — check warnings for data-loss protection signals.
		var dangerous []string
		for _, w := range result.Warnings {
			if notify.IsDangerousWarning(w) {
				dangerous = append(dangerous, w)
			}
		}
		if len(dangerous) > 0 {
			shouldAlert = true
			alertMessage = fmt.Sprintf("Data-loss protection triggered for source '%s'", source.Name)
			alertDetails = strings.Join(dangerous, "\n")
		}
	}

	if !shouldAlert {
		return
	}

	// Look up user email for per-user notifications. nil-safe so tests
	// can exercise this path with a no-DB scheduler; production always
	// has a real db.
	userEmail := ""
	if s.db != nil {
		if user, err := s.db.GetUserByID(source.UserID); err == nil {
			userEmail = user.Email
		}
	}
	userPrefs := s.getUserAlertPrefs(source.UserID)

	s.notifier.SendSyncFailureAlertWithPrefs(
		s.ctx, sourceID, source.Name, userEmail,
		alertMessage, alertDetails, userPrefs,
	)
}

// cleanupRoutine runs periodic cleanup of old sync logs.
func (s *Scheduler) cleanupRoutine() {
	defer s.wg.Done()
	defer recoverPanic("scheduler.cleanupRoutine")

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	s.heartbeat(routineCleanup)

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.heartbeat(routineCleanup)
			s.cleanupOldLogs()
		}
	}
}

// cleanupOldLogs deletes sync logs older than retention period.
func (s *Scheduler) cleanupOldLogs() {
	cutoff := time.Now().AddDate(0, 0, -s.logRetentionDays)
	deleted, err := s.db.CleanOldSyncLogs(cutoff)
	if err != nil {
		log.Printf("Failed to clean old sync logs: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("Cleaned %d old sync logs", deleted)
	}
}

// healthLogRoutine periodically logs scheduler health information.
func (s *Scheduler) healthLogRoutine() {
	defer s.wg.Done()
	defer recoverPanic("scheduler.healthLogRoutine")

	ticker := time.NewTicker(healthLogInterval)
	defer ticker.Stop()

	s.heartbeat(routineHealthLog)

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.heartbeat(routineHealthLog)
			s.logHealth()
		}
	}
}

// logHealth logs current scheduler health status.
func (s *Scheduler) logHealth() {
	s.mu.RLock()
	jobCount := len(s.jobs)
	s.mu.RUnlock()

	log.Printf("[Scheduler Health] Active jobs: %d", jobCount)
}

// staleDetectionRoutine periodically checks for stale sources and logs warnings.
func (s *Scheduler) staleDetectionRoutine() {
	defer s.wg.Done()
	defer recoverPanic("scheduler.staleDetectionRoutine")

	// Check every minute for stale sources
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Initial heartbeat so the watchdog doesn't flag us as stale
	// before our first tick fires.
	s.heartbeat(routineStaleDetection)

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.heartbeat(routineStaleDetection)
			s.checkStaleSources()
		}
	}
}

// checkStaleSources checks for sources that haven't synced in 2x their interval.
func (s *Scheduler) checkStaleSources() {
	s.mu.RLock()
	sourceIDs := make([]string, 0, len(s.jobs))
	intervals := make(map[string]time.Duration)
	for id, job := range s.jobs {
		sourceIDs = append(sourceIDs, id)
		intervals[id] = job.interval
	}
	s.mu.RUnlock()

	now := time.Now()
	for _, sourceID := range sourceIDs {
		source, err := s.db.GetSourceByID(sourceID)
		if err != nil {
			continue
		}

		if !source.Enabled {
			continue
		}

		interval := intervals[sourceID]
		staleThreshold := interval * staleMultiplier

		var timeSinceSync time.Duration
		isStale := false

		if source.LastSyncAt != nil {
			timeSinceSync = now.Sub(*source.LastSyncAt)
			isStale = timeSinceSync > staleThreshold
		} else {
			// Never synced - check how long it's been since creation
			timeSinceSync = now.Sub(source.CreatedAt)
			isStale = timeSinceSync > staleThreshold
		}

		if isStale {
			log.Printf("[STALE WARNING] Source '%s' (ID: %s) hasn't synced in %v (threshold: %v, interval: %v)",
				source.Name, sourceID, timeSinceSync.Round(time.Minute), staleThreshold, interval)

			// Send notification if notifier is configured
			if s.notifier != nil && s.notifier.IsEnabled() {
				// Look up user email for per-user notifications
				userEmail := ""
				if user, err := s.db.GetUserByID(source.UserID); err == nil {
					userEmail = user.Email
				}

				// Look up user alert preferences
				userPrefs := s.getUserAlertPrefs(source.UserID)
				s.notifier.SendStaleAlertWithPrefs(s.ctx, sourceID, source.Name, userEmail, timeSinceSync, staleThreshold, userPrefs)
			}
		}
	}
}

// GetNextSyncAt returns the next scheduled sync time for a source.
// Returns zero time if job doesn't exist.
func (s *Scheduler) GetNextSyncAt(sourceID string) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if job, exists := s.jobs[sourceID]; exists {
		return job.nextSyncAt
	}
	return time.Time{}
}

// IsSourceStale checks if a source is considered stale (hasn't synced in 2x interval).
func (s *Scheduler) IsSourceStale(source *db.Source) bool {
	if !source.Enabled {
		return false
	}

	interval := time.Duration(source.SyncInterval) * time.Second
	staleThreshold := interval * staleMultiplier
	now := time.Now()

	if source.LastSyncAt != nil {
		return now.Sub(*source.LastSyncAt) > staleThreshold
	}

	// Never synced - check how long since creation
	return now.Sub(source.CreatedAt) > staleThreshold
}

// getUserAlertPrefs retrieves user alert preferences and converts them to notify.UserPreferences.
// Returns nil if no preferences are set or if there's an error.
func (s *Scheduler) getUserAlertPrefs(userID string) *notify.UserPreferences {
	if s.db == nil {
		return nil
	}

	dbPrefs, err := s.db.GetUserAlertPreferences(userID)
	if err != nil || dbPrefs == nil {
		return nil
	}

	return &notify.UserPreferences{
		EmailEnabled:    dbPrefs.EmailEnabled,
		WebhookEnabled:  dbPrefs.WebhookEnabled,
		WebhookURL:      dbPrefs.WebhookURL,
		CooldownMinutes: dbPrefs.CooldownMinutes,
	}
}
