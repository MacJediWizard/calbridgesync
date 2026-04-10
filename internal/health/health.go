package health

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// Status represents the health status of a component.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
	StatusDegraded  Status = "degraded"
)

const (
	dbTimeout      = 5 * time.Second
	networkTimeout = 10 * time.Second
)

// Check represents the health check result for a single component.
type Check struct {
	Name    string        `json:"name"`
	Status  Status        `json:"status"`
	Message string        `json:"message,omitempty"`
	Latency time.Duration `json:"latency_ms"`
}

// MarshalJSON implements custom JSON marshaling to convert latency to milliseconds.
func (c Check) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(
		`{"name":%q,"status":%q,"message":%q,"latency_ms":%d}`,
		c.Name, c.Status, c.Message, c.Latency.Milliseconds(),
	)), nil
}

// Report represents the overall health report.
type Report struct {
	Status    Status           `json:"status"`
	Timestamp time.Time        `json:"timestamp"`
	Checks    map[string]Check `json:"checks"`
}

// Checker performs health checks on system components.
type Checker struct {
	db         *db.DB
	oidcIssuer string
	caldavURL  string
	httpClient *http.Client

	mu         sync.RWMutex
	lastReport *Report
}

// NewChecker creates a new health checker.
func NewChecker(database *db.DB, oidcIssuer, caldavURL string) *Checker {
	return &Checker{
		db:         database,
		oidcIssuer: oidcIssuer,
		caldavURL:  caldavURL,
		httpClient: &http.Client{
			Timeout: networkTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			},
		},
	}
}

// Check performs all health checks and returns a report.
func (c *Checker) Check(ctx context.Context) *Report {
	report := &Report{
		Status:    StatusHealthy,
		Timestamp: time.Now().UTC(),
		Checks:    make(map[string]Check),
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	// runCheck launches a health check in a panic-safe goroutine. If the
	// check function panics, the panic is recovered, logged with a stack
	// trace, AND the report entry is populated with a synthetic
	// "Unhealthy" check so the HTTP caller sees a degraded status for
	// that component instead of a missing key (which downstream code
	// might misinterpret as "not checked"). Without this recovery, a
	// panic in one check would bypass wg.Done(), leaving wg.Wait()
	// hung forever and eventually exhausting the HTTP server's
	// goroutine pool — a single malformed /health request would
	// become a DoS vector.
	runCheck := func(name string, fn func(context.Context) Check) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[PANIC] health.check.%s: %v\n%s", name, r, debug.Stack())
					mu.Lock()
					report.Checks[name] = Check{
						Name:    name,
						Status:  StatusUnhealthy,
						Message: fmt.Sprintf("check panicked: %v", r),
					}
					mu.Unlock()
				}
			}()

			check := fn(ctx)
			mu.Lock()
			report.Checks[name] = check
			mu.Unlock()
		}()
	}

	runCheck("database", c.checkDatabase)
	runCheck("oidc", c.checkOIDC)
	if c.caldavURL != "" {
		runCheck("caldav", c.checkCalDAV)
	}

	wg.Wait()

	// Determine overall status
	report.Status = c.determineOverallStatus(report.Checks)

	// Cache the report
	c.mu.Lock()
	c.lastReport = report
	c.mu.Unlock()

	return report
}

// Liveness returns a simple alive check result.
func (c *Checker) Liveness() *Report {
	return &Report{
		Status:    StatusHealthy,
		Timestamp: time.Now().UTC(),
		Checks: map[string]Check{
			"alive": {
				Name:    "alive",
				Status:  StatusHealthy,
				Message: "service is running",
			},
		},
	}
}

// LastReport returns the most recent cached health report.
func (c *Checker) LastReport() *Report {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastReport
}

func (c *Checker) checkDatabase(ctx context.Context) Check {
	check := Check{Name: "database"}
	start := time.Now()

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	// Ping the database
	if err := c.db.Ping(); err != nil {
		check.Status = StatusUnhealthy
		check.Message = fmt.Sprintf("ping failed: %v", err)
		check.Latency = time.Since(start)
		return check
	}

	// Try a simple query
	conn := c.db.Conn()
	row := conn.QueryRowContext(ctx, "SELECT 1")
	var result int
	if err := row.Scan(&result); err != nil {
		check.Status = StatusUnhealthy
		check.Message = fmt.Sprintf("query failed: %v", err)
		check.Latency = time.Since(start)
		return check
	}

	check.Status = StatusHealthy
	check.Message = "database is responsive"
	check.Latency = time.Since(start)
	return check
}

func (c *Checker) checkOIDC(ctx context.Context) Check {
	check := Check{Name: "oidc"}
	start := time.Now()

	if c.oidcIssuer == "" {
		check.Status = StatusDegraded
		check.Message = "OIDC issuer not configured"
		check.Latency = time.Since(start)
		return check
	}

	// Check the discovery endpoint
	discoveryURL := c.oidcIssuer + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		check.Status = StatusUnhealthy
		check.Message = fmt.Sprintf("failed to create request: %v", err)
		check.Latency = time.Since(start)
		return check
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		check.Status = StatusUnhealthy
		check.Message = fmt.Sprintf("discovery request failed: %v", err)
		check.Latency = time.Since(start)
		return check
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		check.Status = StatusUnhealthy
		check.Message = fmt.Sprintf("discovery returned status %d", resp.StatusCode)
		check.Latency = time.Since(start)
		return check
	}

	check.Status = StatusHealthy
	check.Message = "OIDC provider is reachable"
	check.Latency = time.Since(start)
	return check
}

func (c *Checker) checkCalDAV(ctx context.Context) Check {
	check := Check{Name: "caldav"}
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, c.caldavURL, nil)
	if err != nil {
		check.Status = StatusDegraded
		check.Message = fmt.Sprintf("failed to create request: %v", err)
		check.Latency = time.Since(start)
		return check
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		check.Status = StatusDegraded
		check.Message = fmt.Sprintf("request failed: %v", err)
		check.Latency = time.Since(start)
		return check
	}
	defer resp.Body.Close()

	// CalDAV requires authentication, so 401 is expected without credentials
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusUnauthorized {
		check.Status = StatusDegraded
		check.Message = fmt.Sprintf("unexpected status %d", resp.StatusCode)
		check.Latency = time.Since(start)
		return check
	}

	check.Status = StatusHealthy
	check.Message = "CalDAV endpoint is reachable"
	check.Latency = time.Since(start)
	return check
}

func (c *Checker) determineOverallStatus(checks map[string]Check) Status {
	hasUnhealthy := false
	hasDegraded := false

	for _, check := range checks {
		switch check.Status {
		case StatusUnhealthy:
			hasUnhealthy = true
		case StatusDegraded:
			hasDegraded = true
		}
	}

	// Database unhealthy = overall unhealthy
	if dbCheck, ok := checks["database"]; ok && dbCheck.Status == StatusUnhealthy {
		return StatusUnhealthy
	}

	// OIDC unhealthy = overall unhealthy (required for auth)
	if oidcCheck, ok := checks["oidc"]; ok && oidcCheck.Status == StatusUnhealthy {
		return StatusUnhealthy
	}

	if hasUnhealthy {
		return StatusUnhealthy
	}
	if hasDegraded {
		return StatusDegraded
	}
	return StatusHealthy
}
