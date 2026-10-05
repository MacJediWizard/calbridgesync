package caldav

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// selectDestCalendar returns the index of the first discovered destination
// calendar whose path matches destURLPath (the path of the source's dest_url),
// or -1 when none matches. Paths are compared after percent-decoding and
// trimming a trailing slash; the comparison is case-sensitive.
//
// LOG-ONLY: this does not decide which calendar is synced. Sync still uses
// destCalendars[0] (or the URL-path fallback). The result is logged so the
// operator can see whether any source would change calendars before that
// behavior is touched (PR-10a / PR-10b).
func selectDestCalendar(cals []Calendar, destURLPath string) int {
	want := normalizeCalendarPath(destURLPath)
	if want == "" {
		return -1
	}
	for i, cal := range cals {
		if normalizeCalendarPath(cal.Path) == want {
			return i
		}
	}
	return -1
}

func normalizeCalendarPath(p string) string {
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return strings.TrimRight(p, "/")
}

// destSelectLogLine builds the single DESTSELECT line logged per
// source/calendar sync. discover is "ok", "error" or "empty".
func destSelectLogLine(sourceID, calendarName, discover string, cals []Calendar, chosen, urlPath string) string {
	matchIndex := "none"
	if i := selectDestCalendar(cals, urlPath); i >= 0 {
		matchIndex = strconv.Itoa(i)
	}
	return fmt.Sprintf("DESTSELECT source=%s calendar=%q discover=%s chosen=%s url_path=%s match_index=%s discovered=%d",
		sourceID, calendarName, discover, chosen, urlPath, matchIndex, len(cals))
}
