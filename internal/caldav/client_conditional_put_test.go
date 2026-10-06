package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const condPutBody = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:u1\r\nSEQUENCE:0\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T120000Z\r\nDTEND:20260101T130000Z\r\nSUMMARY:t\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// putRecord is what the stub server saw for one PUT.
type putRecord struct {
	path, ifMatch, ifNoneMatch, contentType, auth string
	hasIfMatch, hasIfNoneMatch                    bool
}

// condPutServer records every PUT's precondition headers and answers
// with the status/headers produced by respond(n), n = 1-based PUT count.
// GETs return existing (for the #168 SEQUENCE retry).
func condPutServer(t *testing.T, existing string, respond func(n int, w http.ResponseWriter, r *http.Request)) (*httptest.Server, func() []putRecord) {
	t.Helper()
	var mu sync.Mutex
	var puts []putRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_, _ = io.ReadAll(r.Body)
			mu.Lock()
			puts = append(puts, putRecord{
				path:           r.URL.EscapedPath(),
				ifMatch:        r.Header.Get("If-Match"),
				ifNoneMatch:    r.Header.Get("If-None-Match"),
				hasIfMatch:     len(r.Header.Values("If-Match")) > 0,
				hasIfNoneMatch: len(r.Header.Values("If-None-Match")) > 0,
				contentType:    r.Header.Get("Content-Type"),
				auth:           r.Header.Get("Authorization"),
			})
			n := len(puts)
			mu.Unlock()
			respond(n, w, r)
		case http.MethodGet:
			if existing == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/calendar")
			_, _ = w.Write([]byte(existing))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []putRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]putRecord(nil), puts...)
	}
}

func created(_ int, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("ETag", `"new"`)
	w.WriteHeader(http.StatusCreated)
}

// TestPutEventPreconditionHeaders pins which precondition headers each
// PUT carries: If-Match only on an update with a known destination
// ETag, and never If-None-Match (it would break the #168 retry for
// existing objects the date filter hid from us).
func TestPutEventPreconditionHeaders(t *testing.T) {
	allowLoopbackDial(t)

	cases := []struct {
		name        string
		path        string // event.Path; a foreign path forces the create branch
		ifMatch     string
		useIfMatch  bool
		wantIfMatch string
	}{
		{name: "create via PutEvent", path: "/other-server/u1.ics"},
		{name: "update via PutEvent (no etag)", path: "/cal/u1.ics"},
		{name: "update with known etag", path: "/cal/u1.ics", ifMatch: "abc-1", useIfMatch: true, wantIfMatch: `"abc-1"`},
		{name: "update with unknown etag", path: "/cal/u1.ics", ifMatch: "", useIfMatch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, puts := condPutServer(t, "", created)
			client, err := NewClient(srv.URL+"/cal", "user", "pass")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			ev := &Event{UID: "u1", Path: tc.path, Data: condPutBody}
			if tc.useIfMatch {
				err = client.PutEventIfMatch(context.Background(), "/cal", ev, tc.ifMatch)
			} else {
				err = client.PutEvent(context.Background(), "/cal", ev)
			}
			if err != nil {
				t.Fatalf("put: %v", err)
			}
			got := puts()
			if len(got) != 1 {
				t.Fatalf("PUT count = %d, want 1", len(got))
			}
			p := got[0]
			if p.path != "/cal/u1.ics" {
				t.Errorf("path = %q, want /cal/u1.ics", p.path)
			}
			if p.ifMatch != tc.wantIfMatch || p.hasIfMatch != (tc.wantIfMatch != "") {
				t.Errorf("If-Match = %q (present=%v), want %q", p.ifMatch, p.hasIfMatch, tc.wantIfMatch)
			}
			if p.hasIfNoneMatch {
				t.Errorf("If-None-Match = %q, want absent", p.ifNoneMatch)
			}
			if p.contentType != "text/calendar" {
				t.Errorf("Content-Type = %q, want text/calendar", p.contentType)
			}
			if p.auth == "" {
				t.Errorf("Authorization header missing")
			}
		})
	}
}

// A SOGo 403 "sequences don't match" on the first PUT must still hand
// off to retryPutWithBumpedSequence, and the retry carries no
// precondition headers, on both the create and the If-Match update path.
func TestPutEventSequenceRetryCarriesNoPreconditions(t *testing.T) {
	allowLoopbackDial(t)
	existing := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//SOGo//EN\r\nBEGIN:VEVENT\r\nUID:u1\r\nSEQUENCE:3\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T120000Z\r\nSUMMARY:existing\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	sogo := func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?><D:error xmlns:D="DAV:">sequences don't match</D:error>`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}

	for _, tc := range []struct {
		name    string
		ifMatch string
		path    string
	}{
		{"create", "", "/other-server/u1.ics"},
		{"update with etag", "abc-1", "/cal/u1.ics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, puts := condPutServer(t, existing, sogo)
			client, err := NewClient(srv.URL+"/cal", "user", "pass")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			ev := &Event{UID: "u1", Path: tc.path, Data: condPutBody}
			if err := client.PutEventIfMatch(context.Background(), "/cal", ev, tc.ifMatch); err != nil {
				t.Fatalf("put should recover via SEQUENCE retry, got %v", err)
			}
			got := puts()
			if len(got) != 2 {
				t.Fatalf("PUT count = %d, want 2 (403 then retry)", len(got))
			}
			retry := got[1]
			if retry.hasIfMatch || retry.hasIfNoneMatch {
				t.Errorf("retry carried preconditions: If-Match=%q If-None-Match=%q", retry.ifMatch, retry.ifNoneMatch)
			}
			if got[0].hasIfNoneMatch {
				t.Errorf("first PUT carried If-None-Match %q", got[0].ifNoneMatch)
			}
		})
	}
}

// A successful PUT answered with a weak ETag is a success. go-webdav ran
// strconv.Unquote on the response ETag and turned W/"..." into an error.
func TestPutEventWeakResponseETagIsSuccess(t *testing.T) {
	allowLoopbackDial(t)
	srv, puts := condPutServer(t, "", func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `W/"weak-1"`)
		w.WriteHeader(http.StatusCreated)
	})
	client, err := NewClient(srv.URL+"/cal", "user", "pass")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ev := &Event{UID: "u1", Path: "/cal/u1.ics", Data: condPutBody}
	if err := client.PutEvent(context.Background(), "/cal", ev); err != nil {
		t.Fatalf("PutEvent with weak response ETag: %v", err)
	}
	if n := len(puts()); n != 1 {
		t.Errorf("PUT count = %d, want 1", n)
	}
}

// A 412 to an If-Match update is reported as ErrPreconditionFailed, so
// the sync loop can count it as skipped rather than as a failure.
func TestPutEventIfMatchPreconditionFailed(t *testing.T) {
	allowLoopbackDial(t)
	srv, puts := condPutServer(t, "", func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	client, err := NewClient(srv.URL+"/cal", "user", "pass")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ev := &Event{UID: "u1", Path: "/cal/u1.ics", Data: condPutBody}
	err = client.PutEventIfMatch(context.Background(), "/cal", ev, "stale")
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("err = %v, want ErrPreconditionFailed", err)
	}
	if n := len(puts()); n != 1 {
		t.Errorf("PUT count = %d, want 1 (no retry on 412)", n)
	}
}

// Non-2xx errors keep go-webdav's "<code> <text>: <body>" shape, which
// the 403 hand-off and the string-based 412/409 classifiers rely on.
func TestPutEventErrorKeepsStatusShape(t *testing.T) {
	allowLoopbackDial(t)
	srv, _ := condPutServer(t, "", func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("try later"))
	})
	client, err := NewClient(srv.URL+"/cal", "user", "pass")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = client.PutEvent(context.Background(), "/cal", &Event{UID: "u1", Path: "/cal/u1.ics", Data: condPutBody})
	if !errors.Is(err, ErrConnectionFailed) {
		t.Fatalf("err = %v, want ErrConnectionFailed", err)
	}
	if !strings.Contains(err.Error(), "503 Service Unavailable: try later") {
		t.Errorf("err = %q, want it to contain %q", err, "503 Service Unavailable: try later")
	}
}
