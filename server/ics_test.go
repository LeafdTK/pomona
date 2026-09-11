package main

import (
	"strings"
	"testing"
	"time"
)

// The same cases the JavaScript parser was checked against, so the port is
// provably equivalent: recurrence, exceptions, moved occurrences, COUNT,
// UNTIL, all-day, and a calendar in another timezone.
func TestEventsInWindow(t *testing.T) {
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"BEGIN:VEVENT", "UID:1", "SUMMARY:Standup",
		"DTSTART;TZID=Europe/Madrid:20260101T090000",
		"DTEND;TZID=Europe/Madrid:20260101T091500",
		"RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
		"EXDATE;TZID=Europe/Madrid:20260908T090000",
		"ATTENDEE;CN=Zach Latta:mailto:z@x.com", "END:VEVENT",
		// A moved occurrence replaces the series entry for that day.
		"BEGIN:VEVENT", "UID:1", "RECURRENCE-ID;TZID=Europe/Madrid:20260907T090000",
		"SUMMARY:Standup (moved)",
		"DTSTART;TZID=Europe/Madrid:20260907T113000",
		"DTEND;TZID=Europe/Madrid:20260907T120000", "END:VEVENT",
		"BEGIN:VEVENT", "UID:2", "SUMMARY:Monthly review",
		"DTSTART;TZID=Europe/Madrid:20260113T140000",
		"RRULE:FREQ=MONTHLY;BYDAY=2TU", "END:VEVENT",
		"BEGIN:VEVENT", "UID:3", "SUMMARY:All day offsite",
		"DTSTART;VALUE=DATE:20260909", "END:VEVENT",
		"BEGIN:VEVENT", "UID:4", "SUMMARY:Every other day",
		"DTSTART:20260101T170000Z", "RRULE:FREQ=DAILY;INTERVAL=2;UNTIL=20260908T235959Z", "END:VEVENT",
		"BEGIN:VEVENT", "UID:5", "SUMMARY:Twice only",
		"DTSTART:20260907T080000Z", "RRULE:FREQ=DAILY;COUNT=2", "END:VEVENT",
		"BEGIN:VEVENT", "UID:6", "SUMMARY:Cancelled thing",
		"DTSTART:20260907T100000Z", "STATUS:CANCELLED", "END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")

	// The window carries the reader's zone; an all-day event starts at
	// their midnight, wherever the server happens to be.
	denver, _ := time.LoadLocation("America/Denver")
	from := time.Date(2026, 9, 7, 0, 0, 0, 0, denver)
	to := time.Date(2026, 9, 10, 0, 0, 0, 0, denver)

	got := EventsInWindow(ics, from, to)
	seen := map[string]string{}
	for _, e := range got {
		seen[e.Start.UTC().Format("2006-01-02T15:04Z")] = e.Title
	}

	want := map[string]string{
		"2026-09-07T09:30Z": "Standup (moved)", // override, not the 07:00 series entry
		"2026-09-07T08:00Z": "Twice only",
		"2026-09-08T08:00Z": "Twice only",
		"2026-09-08T12:00Z": "Monthly review",  // second Tuesday
		"2026-09-08T17:00Z": "Every other day", // lands exactly on UNTIL day
		"2026-09-09T06:00Z": "All day offsite", // midnight in Denver
		"2026-09-09T07:00Z": "Standup",
	}
	for at, title := range want {
		if seen[at] != title {
			t.Errorf("at %s want %q, got %q", at, title, seen[at])
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d events, want %d: %v", len(got), len(want), seen)
	}
	// 09:00 Madrid is 07:00 UTC in September: the zone conversion is real.
	if seen["2026-09-08T07:00Z"] != "" {
		t.Error("EXDATE did not remove the 8 September standup")
	}
	if seen["2026-09-07T10:00Z"] != "" {
		t.Error("a CANCELLED event was included")
	}
	for _, e := range got {
		if e.Title == "Standup" && (len(e.Attendees) != 1 || e.Attendees[0] != "Zach Latta") {
			t.Errorf("attendees not parsed: %v", e.Attendees)
		}
	}
}
