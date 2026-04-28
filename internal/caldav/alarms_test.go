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
