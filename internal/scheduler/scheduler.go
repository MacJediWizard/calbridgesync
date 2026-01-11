package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/db"
)

const (
	cleanupInterval  = 24 * time.Hour
	logRetentionDays = 30
	syncTimeout      = 10 * time.Minute // Maximum time for a single sync operation
)

// Job represents a scheduled sync job.
type Job struct {
	sourceID string
	interval time.Duration
	ticker   *time.Ticker
	stopCh   chan struct{}
}

// Scheduler manages background sync jobs.
type Scheduler struct {
	db         *db.DB
	syncEngine *caldav.SyncEngine

	mu        sync.RWMutex
	jobs      map[string]*Job
	syncLocks map[string]*sync.Mutex // Per-source locks to prevent concurrent syncs
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool
}

// New creates a new scheduler.
func New(database *db.DB, syncEngine *caldav.SyncEngine) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db:         database,
		syncEngine: syncEngine,
		jobs:       make(map[string]*Job),
		syncLocks:  make(map[string]*sync.Mutex),
		ctx:        ctx,
		cancel:     cancel,
	}
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

	// Load all enabled sources
	sources, err := s.db.GetEnabledSources()
	if err != nil {
		return err
	}

	// Start a job for each enabled source
	for _, source := range sources {
		interval := time.Duration(source.SyncInterval) * time.Second
		s.AddJob(source.ID, interval)
	}

	// Start cleanup goroutine
	s.wg.Add(1)
	go s.cleanupRoutine()

	log.Printf("Scheduler started with %d jobs", len(sources))
	return nil
}

// Stop gracefully shuts down all jobs.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	s.mu.Unlock()

	// Cancel context to stop all jobs
	s.cancel()

	// Stop all job tickers
	s.mu.Lock()
	for _, job := range s.jobs {
		close(job.stopCh)
		job.ticker.Stop()
	}
	s.jobs = make(map[string]*Job)
	s.mu.Unlock()

	// Wait for all goroutines to finish
	s.wg.Wait()
	log.Println("Scheduler stopped")
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
	job := &Job{
		sourceID: sourceID,
		interval: interval,
		ticker:   time.NewTicker(interval),
		stopCh:   make(chan struct{}),
	}

	s.jobs[sourceID] = job

	// Start job goroutine
	s.wg.Add(1)
	go s.runJob(job)

	log.Printf("Added sync job for source %s with interval %v", sourceID, interval)
}

// RemoveJob removes a sync job.
func (s *Scheduler) RemoveJob(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.jobs[sourceID]; exists {
		close(job.stopCh)
		job.ticker.Stop()
		delete(s.jobs, sourceID)
		log.Printf("Removed sync job for source %s", sourceID)
	}
}

// UpdateJobInterval updates the interval for an existing job.
func (s *Scheduler) UpdateJobInterval(sourceID string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.jobs[sourceID]; exists {
		// Stop old ticker and create new one
		job.ticker.Stop()
		job.interval = interval
		job.ticker = time.NewTicker(interval)
		log.Printf("Updated sync interval for source %s to %v", sourceID, interval)
	}
}

// TriggerSync manually triggers a sync for a source.
func (s *Scheduler) TriggerSync(sourceID string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
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

	// Run immediately on start
	s.executeSync(job.sourceID)

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-job.stopCh:
			return
		case <-job.ticker.C:
			s.executeSync(job.sourceID)
		}
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
func (s *Scheduler) executeSync(sourceID string) {
	// Get per-source lock to prevent concurrent syncs
	lock := s.getSyncLock(sourceID)

	// Try to acquire lock without blocking - skip if another sync is in progress
	if !lock.TryLock() {
		log.Printf("Skipping sync for source %s - another sync is already in progress", sourceID)
		return
	}
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
	} else {
		log.Printf("Sync failed for source %s: %s", source.Name, result.Message)
	}
}

// cleanupRoutine runs periodic cleanup of old sync logs.
func (s *Scheduler) cleanupRoutine() {
	defer s.wg.Done()

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.cleanupOldLogs()
		}
	}
}

// cleanupOldLogs deletes sync logs older than retention period.
func (s *Scheduler) cleanupOldLogs() {
	cutoff := time.Now().AddDate(0, 0, -logRetentionDays)
	deleted, err := s.db.CleanOldSyncLogs(cutoff)
	if err != nil {
		log.Printf("Failed to clean old sync logs: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("Cleaned %d old sync logs", deleted)
	}
}
