package caldav

import "testing"

// TestSelectDestCalendar covers the log-only helper that reports which
// discovered destination calendar (if any) matches the source's dest_url
// path. It never changes the calendar actually used for sync.
func TestSelectDestCalendar(t *testing.T) {
	cals := []Calendar{
		{Path: "/dav/calendars/user/personal/"},
		{Path: "/dav/calendars/user/work/"},
		{Path: "/dav/calendars/user/my%20cal/"},
	}

	tests := []struct {
		name    string
		cals    []Calendar
		urlPath string
		want    int
	}{
		{"nil calendars", nil, "/dav/calendars/user/personal/", -1},
		{"empty calendars", []Calendar{}, "/dav/calendars/user/personal/", -1},
		{"exact match first", cals, "/dav/calendars/user/personal/", 0},
		{"exact match second", cals, "/dav/calendars/user/work/", 1},
		{"trailing slash missing on url path", cals, "/dav/calendars/user/work", 1},
		{"trailing slash missing on calendar path", []Calendar{{Path: "/a/b"}}, "/a/b/", 0},
		{"percent-encoded calendar matches decoded url path", cals, "/dav/calendars/user/my cal/", 2},
		{"principal url does not match a calendar", cals, "/dav/calendars/user/", -1},
		{"root url path matches nothing", cals, "/", -1},
		{"empty url path matches nothing", cals, "", -1},
		{"case differs is not a match", cals, "/dav/calendars/user/Work/", -1},
		{"first of duplicates wins", []Calendar{{Path: "/x/"}, {Path: "/x"}}, "/x/", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectDestCalendar(tt.cals, tt.urlPath); got != tt.want {
				t.Errorf("selectDestCalendar(%v, %q) = %d, want %d", tt.cals, tt.urlPath, got, tt.want)
			}
		})
	}
}

func TestDestSelectLogLine(t *testing.T) {
	cals := []Calendar{{Path: "/cal/a/"}, {Path: "/cal/b/"}}
	tests := []struct {
		name     string
		discover string
		cals     []Calendar
		chosen   string
		urlPath  string
		want     string
	}{
		{"ok match 1", "ok", cals, "/cal/a/", "/cal/b/",
			"DESTSELECT source=src-1 calendar=\"Home\" discover=ok chosen=/cal/a/ url_path=/cal/b/ match_index=1 discovered=2"},
		{"ok no match", "ok", cals, "/cal/a/", "/principal/",
			"DESTSELECT source=src-1 calendar=\"Home\" discover=ok chosen=/cal/a/ url_path=/principal/ match_index=none discovered=2"},
		{"error fallback", "error", nil, "/cal/b/", "/cal/b/",
			"DESTSELECT source=src-1 calendar=\"Home\" discover=error chosen=/cal/b/ url_path=/cal/b/ match_index=none discovered=0"},
		{"empty fallback", "empty", []Calendar{}, "/cal/b/", "/cal/b/",
			"DESTSELECT source=src-1 calendar=\"Home\" discover=empty chosen=/cal/b/ url_path=/cal/b/ match_index=none discovered=0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := destSelectLogLine("src-1", "Home", tt.discover, tt.cals, tt.chosen, tt.urlPath)
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
