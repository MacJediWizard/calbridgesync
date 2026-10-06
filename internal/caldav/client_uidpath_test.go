package caldav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestPutEventObjectPathFromUID pins the wire path PutEvent uses when it
// has to build an object filename from the UID (the create path). Ordinary
// UIDs must keep exactly today's path so existing destination objects are
// still addressed the same way; UIDs that would produce an invalid or
// traversing path ('/', '..', control characters) get a hashed filename.
func TestPutEventObjectPathFromUID(t *testing.T) {
	allowLoopbackDial(t)

	hashed := func(uid string) string {
		sum := sha256.Sum256([]byte(uid))
		return "/cal/" + hex.EncodeToString(sum[:])[:32] + ".ics"
	}

	cases := []struct {
		name string
		uid  string
		want string
	}{
		{"google", "4k8f2l1m9n0p3q5r7s6t@google.com", "/cal/4k8f2l1m9n0p3q5r7s6t@google.com.ics"},
		{"exchange hex", "040000008200E00074C5B7101A82E00800000000D0F3A1B2C3D4E501000000000000000010000000", "/cal/040000008200E00074C5B7101A82E00800000000D0F3A1B2C3D4E501000000000000000010000000.ics"},
		{"percent", "a%b", "/cal/a%25b.ics"},
		{"slash", "a/b", hashed("a/b")},
		{"dotdot", "..", hashed("..")},
		{"traversal", "../../etc/x", hashed("../../etc/x")},
		{"newline", "a\nb", hashed("a\nb")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.Method+" "+r.URL.EscapedPath())
				mu.Unlock()
				if r.Method != http.MethodPut {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("ETag", `"e1"`)
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()

			client, err := NewClient(srv.URL+"/cal", "user", "pass")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:placeholder\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T120000Z\r\nDTEND:20260101T130000Z\r\nSUMMARY:t\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
			// Path from a different server forces the build-from-UID branch.
			ev := &Event{UID: tc.uid, Path: "/other-server/x.ics", Data: body}
			if err := client.PutEvent(context.Background(), "/cal", ev); err != nil {
				t.Fatalf("PutEvent: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(paths) != 1 || paths[0] != "PUT "+tc.want {
				t.Fatalf("requests = %q, want [%q]", paths, "PUT "+tc.want)
			}
		})
	}
}
