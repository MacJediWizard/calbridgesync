package notify

import (
	"context"
	"net"
	"time"
)

// init overrides the package-level webhookDialContext so tests
// that spin up httptest.NewServer instances (which bind to
// 127.0.0.1) can successfully POST to them. Without this, the
// production-safe safeDialContext would reject every connection
// to loopback and every webhook-send test case would fail with
// "blocked destination: 127.0.0.1 (loopback)". (#117)
//
// Living in a _test.go file means this override is ONLY compiled
// into the test binary and has zero effect on production builds.
// The production safeDialContext stays intact.
//
// The permissive dialer is a normal net.Dialer — no IP class
// check, just a 5-second timeout so misbehaving tests fail fast
// instead of hanging.
func init() {
	webhookDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		return dialer.DialContext(ctx, network, addr)
	}
}
