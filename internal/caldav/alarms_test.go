package caldav

import (
	"strings"
	"testing"
)

func TestSanitizeAlarms_NoAlarmsPassthrough(t *testing.T) {
	in := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:abc\r\nSUMMARY:Test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if got := sanitizeAlarms(in, false); got != in {
		t.Errorf("expected passthrough; got diff:\nin=%q\nout=%q", in, got)
	}
	if got := sanitizeAlarms(in, true); got != in {
		t.Errorf("expected passthrough with stripAll=true too; got diff")
	}
}

func TestSanitizeAlarms_StripsMalformedAlarmKeepsValid(t *testing.T) {
	// VEVENT with one well-formed VALARM (has TRIGGER) and one malformed
	// VALARM (no TRIGGER, the Gusto-style bug).
	in := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"UID:abc",
		"SUMMARY:Test",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"DESCRIPTION:Bad alarm without TRIGGER",
		"END:VALARM",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"TRIGGER:-PT15M",
		"DESCRIPTION:Good alarm with TRIGGER",
		"END:VALARM",
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")

	got := sanitizeAlarms(in, false)
	if strings.Contains(got, "Bad alarm without TRIGGER") {
		t.Errorf("malformed alarm was not stripped:\n%s", got)
	}
	if !strings.Contains(got, "Good alarm with TRIGGER") {
		t.Errorf("well-formed alarm was incorrectly stripped:\n%s", got)
	}
	if !strings.Contains(got, "UID:abc") || !strings.Contains(got, "SUMMARY:Test") {
		t.Errorf("VEVENT body was corrupted:\n%s", got)
	}
}

func TestSanitizeAlarms_StripAllRemovesEverything(t *testing.T) {
	in := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"BEGIN:VEVENT",
		"UID:abc",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"TRIGGER:-PT15M",
		"DESCRIPTION:Even valid alarm should be stripped",
		"END:VALARM",
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")

	got := sanitizeAlarms(in, true)
	if strings.Contains(got, "VALARM") || strings.Contains(got, "Even valid alarm") {
		t.Errorf("stripAll=true should remove every VALARM:\n%s", got)
	}
	if !strings.Contains(got, "UID:abc") {
		t.Errorf("VEVENT body was corrupted:\n%s", got)
	}
}

func TestSanitizeAlarms_TriggerWithParameters(t *testing.T) {
	// TRIGGER;RELATED=END:-PT5M is valid per RFC 5545 and must NOT be
	// treated as malformed even when stripAll=false.
	in := strings.Join([]string{
		"BEGIN:VEVENT",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"TRIGGER;RELATED=END:-PT5M",
		"END:VALARM",
		"END:VEVENT",
		"",
	}, "\r\n")

	got := sanitizeAlarms(in, false)
	if !strings.Contains(got, "TRIGGER;RELATED=END:-PT5M") {
		t.Errorf("alarm with parameterized TRIGGER was incorrectly stripped:\n%s", got)
	}
}

func TestSanitizeAlarms_LFOnlyLineEndings(t *testing.T) {
	// Some sources hand us LF-only iCalendar text. We must preserve the
	// input's line ending (not silently convert to CRLF) so the output
	// matches byte-for-byte outside the dropped alarms.
	in := strings.Join([]string{
		"BEGIN:VEVENT",
		"UID:lf-test",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"DESCRIPTION:malformed",
		"END:VALARM",
		"END:VEVENT",
		"",
	}, "\n")

	got := sanitizeAlarms(in, false)
	if strings.Contains(got, "\r\n") {
		t.Errorf("LF-only input should produce LF-only output; got CRLF in:\n%q", got)
	}
	if strings.Contains(got, "VALARM") {
		t.Errorf("malformed alarm not stripped:\n%s", got)
	}
}

func TestSanitizeAlarms_EmptyAndAlarmless(t *testing.T) {
	if got := sanitizeAlarms("", false); got != "" {
		t.Errorf("empty input should yield empty output, got %q", got)
	}
	noAlarm := "BEGIN:VEVENT\r\nUID:x\r\nEND:VEVENT\r\n"
	if got := sanitizeAlarms(noAlarm, true); got != noAlarm {
		t.Errorf("input without VALARM should be returned unchanged")
	}
}

// RFC 5545 §3.1: a content line may be folded at any point, including
// inside the property name. A valid alarm whose TRIGGER name is folded
// must not be mistaken for a malformed one and dropped.
func TestSanitizeAlarms_FoldedTriggerNameKept(t *testing.T) {
	in := strings.Join([]string{
		"BEGIN:VEVENT",
		"UID:fold",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"TRIG",
		" GER:-PT5M",
		"END:VALARM",
		"END:VEVENT",
		"",
	}, "\r\n")

	if got := sanitizeAlarms(in, false); got != in {
		t.Errorf("valid alarm with folded TRIGGER name was altered:\nin=%q\nout=%q", in, got)
	}
	if got := sanitizeAlarms(in, true); strings.Contains(got, "VALARM") {
		t.Errorf("stripAll=true left a VALARM behind:\n%q", got)
	}
}

// RFC 5545 §2: property names and component names are case-insensitive.
func TestSanitizeAlarms_LowercaseNames(t *testing.T) {
	in := strings.Join([]string{
		"BEGIN:VEVENT",
		"UID:lower",
		"begin:valarm",
		"action:DISPLAY",
		"trigger:-PT5M",
		"end:valarm",
		"Begin:VAlarm",
		"action:DISPLAY",
		"description:no trigger",
		"End:VAlarm",
		"END:VEVENT",
		"",
	}, "\r\n")

	got := sanitizeAlarms(in, false)
	if !strings.Contains(got, "trigger:-PT5M") {
		t.Errorf("valid lowercase alarm was dropped:\n%q", got)
	}
	if strings.Contains(got, "no trigger") {
		t.Errorf("malformed lowercase alarm was not stripped:\n%q", got)
	}
	if got := sanitizeAlarms(in, true); strings.Contains(strings.ToUpper(got), "VALARM") {
		t.Errorf("stripAll=true left a lowercase VALARM behind:\n%q", got)
	}
}

// A CRLF envelope around LF-only component lines must still be scanned
// line by line, and every byte outside dropped alarms preserved.
func TestSanitizeAlarms_MixedLineEndings(t *testing.T) {
	in := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\nUID:mixed\nBEGIN:VALARM\nACTION:DISPLAY\nEND:VALARM\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	want := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\nUID:mixed\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	if got := sanitizeAlarms(in, false); got != want {
		t.Errorf("malformed alarm in mixed line endings not stripped cleanly:\nwant=%q\ngot =%q", want, got)
	}
	if got := sanitizeAlarms(in, true); got != want {
		t.Errorf("stripAll=true with mixed line endings:\nwant=%q\ngot =%q", want, got)
	}
}

// Dropping one alarm must leave every other byte, including folded
// lines elsewhere in the event, exactly as received.
func TestSanitizeAlarms_PreservesBytesOutsideDroppedAlarm(t *testing.T) {
	head := "BEGIN:VEVENT\r\nUID:bytes\r\nDESCRIPTION:a long\r\n  folded value\r\n"
	alarm := "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT5M\r\nEND:VALARM\r\n"
	tail := "END:VEVENT\r\n"

	if got := sanitizeAlarms(head+alarm+tail, true); got != head+tail {
		t.Errorf("unexpected output:\nwant=%q\ngot =%q", head+tail, got)
	}
}
