package caldav

import (
	"context"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// TestSetRequestTimeout verifies CALDAV_REQUEST_TIMEOUT reaches every
// CalDAV/ICS HTTP client constructor instead of the hardcoded 300s. (#239)
func TestSetRequestTimeout(t *testing.T) {
	t.Cleanup(func() { SetRequestTimeout(defaultTimeout) })

	clientTimeouts := func(t *testing.T) (basic, oauth, ics time.Duration) {
		t.Helper()
		c, err := NewClient("https://caldav.example.com/", "u", "p")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		oc, err := NewOAuthClient(context.Background(), "https://apidata.googleusercontent.com/caldav/v2/",
			&oauth2.Config{}, &oauth2.Token{RefreshToken: "r"})
		if err != nil {
			t.Fatalf("NewOAuthClient: %v", err)
		}
		ic, err := NewICSClient("https://example.com/feed.ics", "", "")
		if err != nil {
			t.Fatalf("NewICSClient: %v", err)
		}
		return c.httpClient.Timeout, oc.httpClient.Timeout, ic.httpClient.Timeout
	}

	SetRequestTimeout(defaultTimeout)
	if b, o, i := clientTimeouts(t); b != defaultTimeout || o != defaultTimeout || i != defaultTimeout {
		t.Fatalf("default: got basic=%v oauth=%v ics=%v, want %v", b, o, i, defaultTimeout)
	}

	SetRequestTimeout(42 * time.Second)
	if b, o, i := clientTimeouts(t); b != 42*time.Second || o != 42*time.Second || i != 42*time.Second {
		t.Fatalf("configured: got basic=%v oauth=%v ics=%v, want 42s", b, o, i)
	}

	// Non-positive values are ignored rather than disabling the timeout.
	SetRequestTimeout(0)
	if b, _, _ := clientTimeouts(t); b != 42*time.Second {
		t.Fatalf("after SetRequestTimeout(0): got %v, want 42s unchanged", b)
	}
}
