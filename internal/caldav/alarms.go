package caldav

import "strings"

// sanitizeAlarms walks the iCalendar text and removes VALARM blocks per the
// requested policy. Two cases:
//
//   - stripAll = true: remove every VALARM block. The user has set the
//     "Ignore alarms" flag on the source — typically for subscribed feeds
//     (Gusto payroll reminders, billing deadlines, sports schedules) where
//     the alarms are noise on the destination calendar.
//   - stripAll = false: remove only malformed VALARM blocks (those missing
//     the required TRIGGER property). RFC 5545 §3.6.6 mandates TRIGGER on
//     every VALARM. Some publish feeds (notably Gusto) emit alarms without
//     it, and RFC-strict CalDAV servers (notably SOGo) reject the entire
//     calendar object with 501 Not Implemented when they see one. Stripping
//     just the broken alarm preserves the surrounding VEVENT instead of
//     dropping the whole event.
//
// Operates on the raw iCalendar text rather than the parsed go-ical tree
// because string-level filtering preserves the source server's exact
// formatting (line folding, property ordering, parameter quoting) for
// everything outside the VALARM blocks we drop. Re-encoding through
// go-ical can subtly reformat surrounding properties in ways some servers
// care about.
func sanitizeAlarms(data string, stripAll bool) string {
	if data == "" || !strings.Contains(data, "BEGIN:VALARM") {
		return data
	}

	// Mirror the input's line ending so the output matches byte-for-byte
	// outside the VALARM blocks. iCalendar mandates CRLF, but inputs from
	// some sources arrive LF-only.
	lineEnd := "\n"
	if strings.Contains(data, "\r\n") {
		lineEnd = "\r\n"
	}
	lines := strings.Split(data, lineEnd)

	out := make([]string, 0, len(lines))
	var alarmBuf []string
	inAlarm := false
	hasTrigger := false

	for _, line := range lines {
		if !inAlarm {
			if strings.HasPrefix(line, "BEGIN:VALARM") {
				inAlarm = true
				hasTrigger = false
				alarmBuf = alarmBuf[:0]
				alarmBuf = append(alarmBuf, line)
				continue
			}
			out = append(out, line)
			continue
		}

		alarmBuf = append(alarmBuf, line)
		// Property names are uppercase per RFC 5545. Accept TRIGGER: (no
		// parameters) and TRIGGER; (with parameters like RELATED=START).
		if strings.HasPrefix(line, "TRIGGER:") || strings.HasPrefix(line, "TRIGGER;") {
			hasTrigger = true
		}
		if strings.HasPrefix(line, "END:VALARM") {
			inAlarm = false
			drop := stripAll || !hasTrigger
			if !drop {
				out = append(out, alarmBuf...)
			}
			alarmBuf = alarmBuf[:0]
		}
	}

	// Defensive: an unterminated VALARM at EOF means the input was truncated.
	// Preserve the partial buffer rather than silently dropping data — the
	// destination will reject it for a different reason and surface that to
	// the user, which is more honest than disappearing the event.
	if inAlarm {
		out = append(out, alarmBuf...)
	}

	return strings.Join(out, lineEnd)
}

// Source ETag markers that fold the "Ignore alarms" policy into the
// ETag the sync engine records and compares. The destination copy of an
// event depends on both the source content and this policy, so changing
// the effective policy must look like a source change: every
// already-synced event then gets re-PUT once with (or without) its
// alarms.
const (
	// stripAlarmsETagSuffix marks an event synced with every VALARM
	// stripped (one-way + "Ignore alarms").
	stripAlarmsETagSuffix = ";strip-alarms"
	// alarmsKeptETagSuffix marks an event whose source has "Ignore
	// alarms" set but synced with its alarms because the calendar is
	// two-way. Rows written before the two-way scoping fix hold the raw
	// ETag next to a stripped destination copy; this distinct marker
	// forces one re-PUT that restores the alarms there, before a
	// dest_wins edit can write the stripped copy back over the source.
	alarmsKeptETagSuffix = ";alarms-kept"
)

// markSourceETag appends suffix to a source ETag. Empty ETags stay empty
// so the legacy-record skip in shouldUpdateDestFromSource keeps its
// meaning, and marking is idempotent because the same source events can
// be synced to more than one destination.
func markSourceETag(etag, suffix string) string {
	if etag == "" || strings.HasSuffix(etag, suffix) {
		return etag
	}
	return etag + suffix
}
