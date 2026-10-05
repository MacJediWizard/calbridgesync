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
//
// Matching follows RFC 5545 rather than assuming a tidy encoder: content
// lines are unfolded before they are inspected (§3.1, a fold may fall
// anywhere, even inside a property name), names are compared
// case-insensitively (§2), and each physical line keeps its own line
// ending, so a CRLF envelope around LF-only lines is still scanned line
// by line. Kept and dropped regions are copied as the original physical
// lines, so the output is byte-identical outside the dropped alarms.
func sanitizeAlarms(data string, stripAll bool) string {
	if data == "" {
		return data
	}

	physical := strings.SplitAfter(data, "\n")
	var out, alarmBuf strings.Builder
	out.Grow(len(data))
	inAlarm := false
	hasTrigger := false

	for i := 0; i < len(physical); {
		// Gather one logical content line: the physical line plus any
		// continuation lines (those starting with a space or tab).
		j := i + 1
		for j < len(physical) && isFoldContinuation(physical[j]) {
			j++
		}
		raw := physical[i:j]
		logical := unfoldContentLine(raw)
		i = j

		if !inAlarm {
			if isComponentDelimiter(logical, "BEGIN", "VALARM") {
				inAlarm = true
				hasTrigger = false
				alarmBuf.Reset()
				writeAll(&alarmBuf, raw)
				continue
			}
			writeAll(&out, raw)
			continue
		}

		writeAll(&alarmBuf, raw)
		if strings.EqualFold(contentLineName(logical), "TRIGGER") {
			hasTrigger = true
		}
		if isComponentDelimiter(logical, "END", "VALARM") {
			inAlarm = false
			if !stripAll && hasTrigger {
				out.WriteString(alarmBuf.String())
			}
			alarmBuf.Reset()
		}
	}

	// Defensive: an unterminated VALARM at EOF means the input was truncated.
	// Preserve the partial buffer rather than silently dropping data — the
	// destination will reject it for a different reason and surface that to
	// the user, which is more honest than disappearing the event.
	if inAlarm {
		out.WriteString(alarmBuf.String())
	}

	return out.String()
}

// isFoldContinuation reports whether a physical line continues the
// previous content line (RFC 5545 §3.1).
func isFoldContinuation(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// unfoldContentLine joins the physical lines of one content line, dropping
// line endings and the single leading whitespace of each continuation.
func unfoldContentLine(lines []string) string {
	if len(lines) == 1 {
		return strings.TrimRight(lines[0], "\r\n")
	}
	var b strings.Builder
	for k, l := range lines {
		l = strings.TrimRight(l, "\r\n")
		if k > 0 {
			l = l[1:]
		}
		b.WriteString(l)
	}
	return b.String()
}

// contentLineName returns the property or delimiter name of an unfolded
// content line: everything before the first ';' or ':'.
func contentLineName(line string) string {
	if idx := strings.IndexAny(line, ";:"); idx >= 0 {
		return line[:idx]
	}
	return line
}

// isComponentDelimiter reports whether an unfolded content line is
// "<keyword>:<component>", compared case-insensitively.
func isComponentDelimiter(line, keyword, component string) bool {
	name, value, ok := strings.Cut(line, ":")
	return ok && strings.EqualFold(name, keyword) && strings.EqualFold(strings.TrimSpace(value), component)
}

func writeAll(b *strings.Builder, lines []string) {
	for _, l := range lines {
		b.WriteString(l)
	}
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
