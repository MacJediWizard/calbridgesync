package web

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/macjediwizard/calbridgesync/internal/auth"
)

// TestResolveLoginUser covers the OIDC callback's user-lookup step: the
// status each failure maps to, and that the logs carry the user ID rather
// than the email address.
func TestResolveLoginUser(t *testing.T) {
	th := setupTestHandlers(t)
	defer th.cleanup()

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)

	owner := &auth.OIDCClaims{Subject: "sub-owner", Email: "owner@example.com", Name: "Owner", EmailVerified: true}
	user, status, msg := th.handlers.resolveLoginUser(owner)
	if user == nil {
		t.Fatalf("owner login failed: %d %q", status, msg)
	}

	t.Run("different subject with the same email is forbidden", func(t *testing.T) {
		logs.Reset()
		other := &auth.OIDCClaims{Subject: "sub-other", Email: "owner@example.com", Name: "Other", EmailVerified: true}
		got, status, _ := th.handlers.resolveLoginUser(other)
		if got != nil || status != http.StatusForbidden {
			t.Fatalf("got user %v status %d, want nil 403", got, status)
		}
		out := logs.String()
		if !strings.Contains(out, user.ID) {
			t.Errorf("log %q does not name user %s", out, user.ID)
		}
		if strings.Contains(out, "owner@example.com") {
			t.Errorf("log leaks the email address: %q", out)
		}
	})

	t.Run("missing subject is a bad request and is logged", func(t *testing.T) {
		logs.Reset()
		got, status, _ := th.handlers.resolveLoginUser(&auth.OIDCClaims{Email: "nosub@example.com", EmailVerified: true})
		if got != nil || status != http.StatusBadRequest {
			t.Fatalf("got user %v status %d, want nil 400", got, status)
		}
		if logs.Len() == 0 {
			t.Error("missing subject was not logged")
		}
	})

	t.Run("database failure is a logged 500", func(t *testing.T) {
		th2 := setupTestHandlers(t)
		th2.cleanup() // closes the database
		logs.Reset()
		got, status, _ := th2.handlers.resolveLoginUser(owner)
		if got != nil || status != http.StatusInternalServerError {
			t.Fatalf("got user %v status %d, want nil 500", got, status)
		}
		if logs.Len() == 0 {
			t.Error("database failure was not logged")
		}
	})
}
