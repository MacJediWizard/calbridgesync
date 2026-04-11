package caldav

import (
	"strings"
)

// ZombieFingerprint describes a single corrupted recurring-series
// event detected in a source or destination event list. The corruption
// pattern it represents is the one discovered during William's WOS
// Tech Team recovery in April 2026: a recurring series whose master
// VEVENT was replaced (by a round-trip through a lossy iCalendar
// library somewhere in the sync chain) with an empty stub carrying
// an `X-MOZ-FAKED-MASTER:1` marker and a `SUMMARY:Untitled` value —
// leaving the real meeting data trapped inside one or more
// `RECURRENCE-ID` override VEVENTs that Apple Calendar refuses to
// render on their own. Net user-visible effect: the whole weekly
// series becomes invisible in Calendar.app.
//
// We detect two variants of the same bug:
//
//  1. **X-MOZ-FAKED-MASTER marker:** the libical-derived fallback
//     writes this non-standard property on any VEVENT it had to
//     fabricate because the real master was missing when the
//     VCALENDAR was re-emitted. It's the cleanest fingerprint
//     because it's authoritative — if this marker is present, the
//     master is by definition not what the original series had.
//
//  2. **Orphaned RECURRENCE-ID override:** a VEVENT that has
//     `RECURRENCE-ID:...` but no corresponding master VEVENT with
//     the same UID and a live `RRULE:` line. Any CalDAV server
//     that stores overrides as separate items will produce this
//     pattern when the master gets deleted or stripped, whether
//     or not the `X-MOZ-FAKED-MASTER` marker is also present.
//     This is the "shoe dropped" pattern — the real damage — and
//     it's what causes Calendar.app to drop the series entirely.
//
// The detector never classifies a healthy event as corrupted,
// never mutates anything, and never looks outside the Event.Data
// text — it's a pure scan designed to be safe to call anywhere in
// the sync pipeline without changing sync semantics. (#95)
type ZombieFingerprint struct {
	// UID is the shared iCalendar UID of the corrupted series.
	// For Microsoft Outlook meetings this is the Global Object ID
	// (e.g. `040000...A0748BE1...`), identical across every
	// attendee's calendar — which is why the WOS recovery was
	// able to consider borrowing the series from another attendee
	// in the first place.
	UID string

	// Reason is one of the string constants below. Exposed as a
	// public string rather than an int constant so log lines and
	// warnings remain readable without a lookup table.
	Reason string

	// EventPath is the CalDAV path of the specific Event the
	// fingerprint was found in, for operator follow-up (e.g. with
	// cmd/purge-uid). Empty if the Event had no Path populated.
	EventPath string
}

// Zombie detection reasons. Stable strings suitable for log grep
// and future alert templating.
const (
	// ZombieReasonFakedMaster means the VEVENT carries an
	// X-MOZ-FAKED-MASTER property, which is libical's synthetic
	// fallback for a master that had to be fabricated because the
	// real one was missing during re-emission.
	ZombieReasonFakedMaster = "x-moz-faked-master"

	// ZombieReasonOrphanedOverride means the VEVENT has a
	// RECURRENCE-ID property (making it an override of a recurring
	// instance) but no master VEVENT with a matching UID and an
	// RRULE anywhere in the scanned event list. Apple Calendar
	// drops orphaned overrides, which is what makes the series
	// invisible.
	ZombieReasonOrphanedOverride = "orphaned-recurrence-id"
)

// FindZombieMasters scans a slice of CalDAV events for the
// corruption patterns described in ZombieFingerprint's doc comment.
// Returns one entry per (UID, reason) hit. Never modifies the input
// slice and never allocates unless there are findings.
//
// The scan logic is intentionally simple and defensive:
//
//   - Pass 1 walks every Event and classifies it into one of three
//     buckets keyed by UID: (a) has a master VEVENT with a live
//     `RRULE:` property, (b) has an X-MOZ-FAKED-MASTER marker
//     anywhere, (c) has a `RECURRENCE-ID:` line (override).
//   - Pass 2 emits a fingerprint for every faked-master hit
//     (regardless of whether the override case also applies — the
//     marker is authoritative).
//   - Pass 3 emits an orphaned-override fingerprint for every UID
//     that has an override but no live master. Buckets a and b are
//     treated as "has a master" for the purposes of orphan
//     detection so we don't double-flag a faked master whose
//     override also exists (one fingerprint per defect is clearer
//     than two).
//
// Why substring matching instead of parseICalendar: we want this
// detector to run even when the parser would silently drop the
// corrupted block. Substring scanning over raw Event.Data is
// deliberately primitive and cannot be defeated by a parser that
// "cleans up" the iCalendar during a round-trip.
//
// Why we anchor on property prefixes (e.g. "\nRRULE:", "\nRRULE;"):
// to avoid false-positive matches on the text "RRULE" appearing
// inside a SUMMARY, DESCRIPTION, or X-ALT-DESC HTML payload. The
// anchored check only triggers when the substring appears at the
// start of a content line.
func FindZombieMasters(events []Event) []ZombieFingerprint {
	if len(events) == 0 {
		return nil
	}

	// Classify each event by UID. A single UID may appear multiple
	// times in the slice (CalDAV servers can store overrides as
	// separate items) so we accumulate the classification across
	// every event that shares the UID.
	type classification struct {
		hasLiveMaster bool   // VEVENT with RRULE but no X-MOZ-FAKED-MASTER
		hasFakedStub  bool   // VEVENT with X-MOZ-FAKED-MASTER
		hasOverride   bool   // VEVENT with RECURRENCE-ID
		firstPath     string // first Event.Path seen for this UID, for operator follow-up
	}

	byUID := make(map[string]*classification)

	for i := range events {
		uid := events[i].UID
		if uid == "" {
			continue
		}
		data := events[i].Data
		if data == "" {
			continue
		}
		cls, ok := byUID[uid]
		if !ok {
			cls = &classification{firstPath: events[i].Path}
			byUID[uid] = cls
		}
		if cls.firstPath == "" && events[i].Path != "" {
			cls.firstPath = events[i].Path
		}
		fakedHere := containsProperty(data, "X-MOZ-FAKED-MASTER")
		if fakedHere {
			cls.hasFakedStub = true
		}
		// "Live master" = has RRULE AND is not itself a faked stub.
		// The `!fakedHere` check prevents a single event from being
		// counted as both a faked stub AND a live master when the
		// libical fallback emits the X-MOZ-FAKED-MASTER marker
		// alongside a synthesized placeholder RRULE.
		if !fakedHere && containsProperty(data, "RRULE") {
			cls.hasLiveMaster = true
		}
		if containsProperty(data, "RECURRENCE-ID") {
			cls.hasOverride = true
		}
	}

	var findings []ZombieFingerprint

	// Pass 2: X-MOZ-FAKED-MASTER marker is authoritative. One
	// fingerprint per UID regardless of what else is true.
	for uid, cls := range byUID {
		if cls.hasFakedStub {
			findings = append(findings, ZombieFingerprint{
				UID:       uid,
				Reason:    ZombieReasonFakedMaster,
				EventPath: cls.firstPath,
			})
		}
	}

	// Pass 3: orphaned override. Only flag UIDs where we saw an
	// override but neither a live master nor a faked stub (the
	// faked stub would have been flagged by pass 2 already, and
	// double-reporting the same UID under two reasons adds noise
	// without adding insight).
	for uid, cls := range byUID {
		if cls.hasOverride && !cls.hasLiveMaster && !cls.hasFakedStub {
			findings = append(findings, ZombieFingerprint{
				UID:       uid,
				Reason:    ZombieReasonOrphanedOverride,
				EventPath: cls.firstPath,
			})
		}
	}

	return findings
}

// containsProperty reports whether data contains an iCalendar
// property with the given name at the start of a content line. The
// match is deliberately anchored so the text "RRULE" inside a
// SUMMARY or DESCRIPTION block does not produce a false positive.
//
// Content lines in iCalendar are CRLF-delimited per RFC 5545, but
// tools that round-trip the text sometimes collapse to LF, and
// long lines can be folded with " " / "\t" at the start of the
// continuation. We only need to recognize the beginning of a
// property line, so we scan for `\nNAME:` or `\nNAME;` where the
// `;` covers property parameters (e.g. `SUMMARY;LANGUAGE=en-US:`).
// We also handle the case where the property is the very first
// line by allowing the BEGIN:VEVENT right before it — but in
// practice iCalendar bodies always start with BEGIN:VCALENDAR, so
// a leading-line check is academic. The \n-anchored version is
// sufficient and keeps the detector simple.
func containsProperty(data, name string) bool {
	if data == "" || name == "" {
		return false
	}
	// Fast path: exact colon-terminated match at any newline.
	if strings.Contains(data, "\n"+name+":") {
		return true
	}
	// Parameter-terminated match (`SUMMARY;LANGUAGE=en-US:`).
	if strings.Contains(data, "\n"+name+";") {
		return true
	}
	// Handle CRLF that was already split on \n — the \r stays at
	// the end of the previous line, so the check above still works
	// because the needle is anchored on \n not \r\n.
	return false
}
