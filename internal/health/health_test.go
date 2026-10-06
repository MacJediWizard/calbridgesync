package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLiveness(t *testing.T) {
	checker := &Checker{}

	t.Run("returns healthy status", func(t *testing.T) {
		report := checker.Liveness()

		if report == nil {
			t.Fatal("expected non-nil report")
		}

		if report.Status != StatusHealthy {
			t.Errorf("expected status %q, got %q", StatusHealthy, report.Status)
		}

		if report.Timestamp.IsZero() {
			t.Error("expected non-zero timestamp")
		}

		aliveCheck, ok := report.Checks["alive"]
		if !ok {
			t.Fatal("expected 'alive' check in report")
		}

		if aliveCheck.Status != StatusHealthy {
			t.Errorf("expected alive check to be healthy, got %q", aliveCheck.Status)
		}

		if aliveCheck.Name != "alive" {
			t.Errorf("expected check name 'alive', got %q", aliveCheck.Name)
		}
	})
}

func TestDetermineOverallStatus(t *testing.T) {
	checker := &Checker{}

	testCases := []struct {
		name     string
		checks   map[string]Check
		expected Status
	}{
		{
			name: "all healthy",
			checks: map[string]Check{
				"database": {Status: StatusHealthy},
				"oidc":     {Status: StatusHealthy},
				"caldav":   {Status: StatusHealthy},
			},
			expected: StatusHealthy,
		},
		{
			name: "database unhealthy makes overall unhealthy",
			checks: map[string]Check{
				"database": {Status: StatusUnhealthy},
				"oidc":     {Status: StatusHealthy},
			},
			expected: StatusUnhealthy,
		},
		{
			name: "oidc unhealthy makes overall unhealthy",
			checks: map[string]Check{
				"database": {Status: StatusHealthy},
				"oidc":     {Status: StatusUnhealthy},
			},
			expected: StatusUnhealthy,
		},
		{
			name: "caldav degraded makes overall degraded",
			checks: map[string]Check{
				"database": {Status: StatusHealthy},
				"oidc":     {Status: StatusHealthy},
				"caldav":   {Status: StatusDegraded},
			},
			expected: StatusDegraded,
		},
		{
			name: "any unhealthy makes overall unhealthy",
			checks: map[string]Check{
				"database": {Status: StatusHealthy},
				"oidc":     {Status: StatusHealthy},
				"other":    {Status: StatusUnhealthy},
			},
			expected: StatusUnhealthy,
		},
		{
			name:     "empty checks is healthy",
			checks:   map[string]Check{},
			expected: StatusHealthy,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := checker.determineOverallStatus(tc.checks)
			if result != tc.expected {
				t.Errorf("expected status %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestStatusConstants(t *testing.T) {
	t.Run("status constants are correct", func(t *testing.T) {
		if StatusHealthy != "healthy" {
			t.Errorf("expected StatusHealthy to be 'healthy', got %q", StatusHealthy)
		}
		if StatusUnhealthy != "unhealthy" {
			t.Errorf("expected StatusUnhealthy to be 'unhealthy', got %q", StatusUnhealthy)
		}
		if StatusDegraded != "degraded" {
			t.Errorf("expected StatusDegraded to be 'degraded', got %q", StatusDegraded)
		}
	})
}

func TestCheckMarshalJSON(t *testing.T) {
	t.Run("marshals latency as milliseconds", func(t *testing.T) {
		check := Check{
			Name:    "test",
			Status:  StatusHealthy,
			Message: "test message",
			Latency: 150 * time.Millisecond,
		}

		data, err := json.Marshal(check)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		latency, ok := result["latency_ms"].(float64)
		if !ok {
			t.Fatal("expected latency_ms field")
		}

		if int(latency) != 150 {
			t.Errorf("expected latency_ms 150, got %v", latency)
		}
	})

	t.Run("includes all fields in JSON", func(t *testing.T) {
		check := Check{
			Name:    "database",
			Status:  StatusHealthy,
			Message: "connected",
			Latency: 50 * time.Millisecond,
		}

		data, err := json.Marshal(check)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(data, &result)

		if result["name"] != "database" {
			t.Errorf("expected name 'database', got %v", result["name"])
		}
		if result["status"] != "healthy" {
			t.Errorf("expected status 'healthy', got %v", result["status"])
		}
		if result["message"] != "connected" {
			t.Errorf("expected message 'connected', got %v", result["message"])
		}
	})
}

func TestNewChecker(t *testing.T) {
	t.Run("creates checker with provided values", func(t *testing.T) {
		// Note: We can't easily test with a real DB here, so we just verify creation
		checker := NewChecker(nil, "https://auth.example.com", "https://caldav.example.com")

		if checker == nil {
			t.Fatal("expected non-nil checker")
		}

		if checker.oidcIssuer != "https://auth.example.com" {
			t.Errorf("expected oidcIssuer to be set")
		}

		if checker.caldavURL != "https://caldav.example.com" {
			t.Errorf("expected caldavURL to be set")
		}

		if checker.httpClient == nil {
			t.Error("expected httpClient to be created")
		}
	})

	t.Run("creates checker with empty caldav URL", func(t *testing.T) {
		checker := NewChecker(nil, "https://auth.example.com", "")

		if checker == nil {
			t.Fatal("expected non-nil checker")
		}

		if checker.caldavURL != "" {
			t.Errorf("expected empty caldavURL")
		}
	})

	t.Run("creates checker with empty oidc issuer", func(t *testing.T) {
		checker := NewChecker(nil, "", "https://caldav.example.com")

		if checker == nil {
			t.Fatal("expected non-nil checker")
		}

		if checker.oidcIssuer != "" {
			t.Errorf("expected empty oidcIssuer")
		}
	})
}

func TestTimeoutConstants(t *testing.T) {
	t.Run("timeout constants have expected values", func(t *testing.T) {
		if dbTimeout != 5*time.Second {
			t.Errorf("expected dbTimeout to be 5s, got %v", dbTimeout)
		}
		if networkTimeout != 10*time.Second {
			t.Errorf("expected networkTimeout to be 10s, got %v", networkTimeout)
		}
	})
}

func TestCheckOIDC(t *testing.T) {
	t.Run("returns degraded when issuer not configured", func(t *testing.T) {
		checker := &Checker{
			oidcIssuer: "",
			httpClient: http.DefaultClient,
		}

		ctx := context.Background()
		check := checker.checkOIDC(ctx)

		if check.Status != StatusDegraded {
			t.Errorf("expected status %q, got %q", StatusDegraded, check.Status)
		}
		if check.Name != "oidc" {
			t.Errorf("expected name 'oidc', got %q", check.Name)
		}
		if check.Message != "OIDC issuer not configured" {
			t.Errorf("unexpected message: %q", check.Message)
		}
	})

	t.Run("returns healthy when discovery endpoint responds 200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/.well-known/openid-configuration" {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"issuer": "https://example.com"}`))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		checker := &Checker{
			oidcIssuer: server.URL,
			httpClient: server.Client(),
		}

		ctx := context.Background()
		check := checker.checkOIDC(ctx)

		if check.Status != StatusHealthy {
			t.Errorf("expected status %q, got %q", StatusHealthy, check.Status)
		}
		if check.Message != "OIDC provider is reachable" {
			t.Errorf("unexpected message: %q", check.Message)
		}
		if check.Latency <= 0 {
			t.Error("expected positive latency")
		}
	})

	t.Run("returns unhealthy when discovery endpoint returns non-200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		checker := &Checker{
			oidcIssuer: server.URL,
			httpClient: server.Client(),
		}

		ctx := context.Background()
		check := checker.checkOIDC(ctx)

		if check.Status != StatusUnhealthy {
			t.Errorf("expected status %q, got %q", StatusUnhealthy, check.Status)
		}
	})

	t.Run("returns unhealthy when connection fails", func(t *testing.T) {
		checker := &Checker{
			oidcIssuer: "https://nonexistent.invalid.localhost:99999",
			httpClient: &http.Client{Timeout: 100 * time.Millisecond},
		}

		ctx := context.Background()
		check := checker.checkOIDC(ctx)

		if check.Status != StatusUnhealthy {
			t.Errorf("expected status %q, got %q", StatusUnhealthy, check.Status)
		}
	})
}

func TestCheckCalDAV(t *testing.T) {
	t.Run("returns healthy when OPTIONS returns 200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				w.Header().Set("DAV", "1, 2, calendar-access")
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer server.Close()

		checker := &Checker{
			caldavURL:  server.URL,
			httpClient: server.Client(),
		}

		ctx := context.Background()
		check := checker.checkCalDAV(ctx)

		if check.Status != StatusHealthy {
			t.Errorf("expected status %q, got %q", StatusHealthy, check.Status)
		}
		if check.Name != "caldav" {
			t.Errorf("expected name 'caldav', got %q", check.Name)
		}
		if check.Message != "CalDAV endpoint is reachable" {
			t.Errorf("unexpected message: %q", check.Message)
		}
	})

	t.Run("returns healthy when OPTIONS returns 204", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		defer server.Close()

		checker := &Checker{
			caldavURL:  server.URL,
			httpClient: server.Client(),
		}

		ctx := context.Background()
		check := checker.checkCalDAV(ctx)

		if check.Status != StatusHealthy {
			t.Errorf("expected status %q, got %q", StatusHealthy, check.Status)
		}
	})

	t.Run("returns healthy when OPTIONS returns 401 (expected without auth)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		checker := &Checker{
			caldavURL:  server.URL,
			httpClient: server.Client(),
		}

		ctx := context.Background()
		check := checker.checkCalDAV(ctx)

		if check.Status != StatusHealthy {
			t.Errorf("expected status %q for 401, got %q", StatusHealthy, check.Status)
		}
	})

	t.Run("returns degraded when OPTIONS returns unexpected status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		checker := &Checker{
			caldavURL:  server.URL,
			httpClient: server.Client(),
		}

		ctx := context.Background()
		check := checker.checkCalDAV(ctx)

		if check.Status != StatusDegraded {
			t.Errorf("expected status %q, got %q", StatusDegraded, check.Status)
		}
	})

	t.Run("returns degraded when connection fails", func(t *testing.T) {
		checker := &Checker{
			caldavURL:  "https://nonexistent.invalid.localhost:99999",
			httpClient: &http.Client{Timeout: 100 * time.Millisecond},
		}

		ctx := context.Background()
		check := checker.checkCalDAV(ctx)

		if check.Status != StatusDegraded {
			t.Errorf("expected status %q, got %q", StatusDegraded, check.Status)
		}
	})
}

func TestReportStruct(t *testing.T) {
	t.Run("report has expected fields", func(t *testing.T) {
		report := &Report{
			Status:    StatusHealthy,
			Timestamp: time.Now().UTC(),
			Checks: map[string]Check{
				"test": {
					Name:    "test",
					Status:  StatusHealthy,
					Message: "ok",
					Latency: 10 * time.Millisecond,
				},
			},
		}

		if report.Status != StatusHealthy {
			t.Error("Status field not working")
		}
		if report.Timestamp.IsZero() {
			t.Error("Timestamp field not working")
		}
		if len(report.Checks) != 1 {
			t.Error("Checks field not working")
		}
	})
}

func TestCheckStruct(t *testing.T) {
	t.Run("check has expected fields", func(t *testing.T) {
		check := Check{
			Name:    "database",
			Status:  StatusHealthy,
			Message: "connected",
			Latency: 5 * time.Millisecond,
		}

		if check.Name != "database" {
			t.Error("Name field not working")
		}
		if check.Status != StatusHealthy {
			t.Error("Status field not working")
		}
		if check.Message != "connected" {
			t.Error("Message field not working")
		}
		if check.Latency != 5*time.Millisecond {
			t.Error("Latency field not working")
		}
	})
}

func TestCheckerStruct(t *testing.T) {
	t.Run("checker has expected fields", func(t *testing.T) {
		checker := &Checker{
			oidcIssuer: "https://auth.example.com",
			caldavURL:  "https://caldav.example.com",
			httpClient: http.DefaultClient,
		}

		if checker.oidcIssuer != "https://auth.example.com" {
			t.Error("oidcIssuer field not working")
		}
		if checker.caldavURL != "https://caldav.example.com" {
			t.Error("caldavURL field not working")
		}
		if checker.httpClient == nil {
			t.Error("httpClient field not working")
		}
	})
}

func TestCheckMarshalJSONEdgeCases(t *testing.T) {
	t.Run("marshals zero latency", func(t *testing.T) {
		check := Check{
			Name:    "test",
			Status:  StatusHealthy,
			Message: "",
			Latency: 0,
		}

		data, err := json.Marshal(check)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		latency, ok := result["latency_ms"].(float64)
		if !ok {
			t.Fatal("expected latency_ms field")
		}

		if int(latency) != 0 {
			t.Errorf("expected latency_ms 0, got %v", latency)
		}
	})

	t.Run("marshals empty message", func(t *testing.T) {
		check := Check{
			Name:    "test",
			Status:  StatusHealthy,
			Message: "",
			Latency: 10 * time.Millisecond,
		}

		data, err := json.Marshal(check)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(data, &result)

		if result["message"] != "" {
			t.Errorf("expected empty message, got %v", result["message"])
		}
	})

	t.Run("marshals special characters in message", func(t *testing.T) {
		check := Check{
			Name:    "test",
			Status:  StatusUnhealthy,
			Message: "error: connection \"failed\" with <timeout>",
			Latency: 10 * time.Millisecond,
		}

		data, err := json.Marshal(check)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		// Verify it's valid JSON
		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
	})

	t.Run("marshals large latency", func(t *testing.T) {
		check := Check{
			Name:    "test",
			Status:  StatusDegraded,
			Message: "slow response",
			Latency: 5 * time.Second,
		}

		data, err := json.Marshal(check)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(data, &result)

		latency, _ := result["latency_ms"].(float64)
		if int(latency) != 5000 {
			t.Errorf("expected latency_ms 5000, got %v", latency)
		}
	})
}

func TestDetermineOverallStatusEdgeCases(t *testing.T) {
	checker := &Checker{}

	t.Run("only database check present and healthy", func(t *testing.T) {
		checks := map[string]Check{
			"database": {Status: StatusHealthy},
		}
		result := checker.determineOverallStatus(checks)
		if result != StatusHealthy {
			t.Errorf("expected %q, got %q", StatusHealthy, result)
		}
	})

	t.Run("only oidc check present and healthy", func(t *testing.T) {
		checks := map[string]Check{
			"oidc": {Status: StatusHealthy},
		}
		result := checker.determineOverallStatus(checks)
		if result != StatusHealthy {
			t.Errorf("expected %q, got %q", StatusHealthy, result)
		}
	})

	t.Run("database healthy but oidc degraded", func(t *testing.T) {
		checks := map[string]Check{
			"database": {Status: StatusHealthy},
			"oidc":     {Status: StatusDegraded},
		}
		result := checker.determineOverallStatus(checks)
		if result != StatusDegraded {
			t.Errorf("expected %q, got %q", StatusDegraded, result)
		}
	})

	t.Run("multiple degraded checks", func(t *testing.T) {
		checks := map[string]Check{
			"database": {Status: StatusHealthy},
			"oidc":     {Status: StatusDegraded},
			"caldav":   {Status: StatusDegraded},
		}
		result := checker.determineOverallStatus(checks)
		if result != StatusDegraded {
			t.Errorf("expected %q, got %q", StatusDegraded, result)
		}
	})

	t.Run("unknown check unhealthy makes overall unhealthy", func(t *testing.T) {
		checks := map[string]Check{
			"database": {Status: StatusHealthy},
			"oidc":     {Status: StatusHealthy},
			"custom":   {Status: StatusUnhealthy},
		}
		result := checker.determineOverallStatus(checks)
		if result != StatusUnhealthy {
			t.Errorf("expected %q, got %q", StatusUnhealthy, result)
		}
	})
}
