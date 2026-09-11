package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Just enough iCalendar to answer "what's on today?".
//
// Recurrence is expanded over a window rather than in general: we never need
// the four hundredth occurrence, only the ones in the next day or two, which
// keeps this a bounded loop instead of a full RRULE engine.

const maxIterations = 750

type icsLine struct {
	name   string
	params map[string]string
	value  string
}

// Unfold RFC 5545 continuation lines, then split.
func icsLines(text string) []icsLine {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = regexp.MustCompile(`\n[ \t]`).ReplaceAllString(text, "")

	out := []icsLine{}
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSpace(raw)
		colon := strings.Index(raw, ":")
		if colon == -1 {
			continue
		}
		head, value := raw[:colon], raw[colon+1:]
		parts := strings.Split(head, ";")
		line := icsLine{name: strings.ToUpper(parts[0]), params: map[string]string{}, value: value}
		for _, p := range parts[1:] {
			if eq := strings.Index(p, "="); eq != -1 {
				line.params[strings.ToUpper(p[:eq])] = strings.Trim(p[eq+1:], `"`)
			}
		}
		out = append(out, line)
	}
	return out
}

func unescapeICS(s string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(s)
}

// A wall-clock time plus the zone it should be read in. Recurrence arithmetic
// happens on the wall clock; the conversion to a real instant happens once, per
// occurrence, at the end.
type icsTime struct {
	wall   time.Time // fields are the local reading, held in UTC
	loc    *time.Location
	allDay bool
}

var dateRe = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2}))?`)

func parseICSTime(value string, params map[string]string) (icsTime, bool) {
	m := dateRe.FindStringSubmatch(value)
	if m == nil {
		return icsTime{}, false
	}
	num := func(s string) int { n, _ := strconv.Atoi(s); return n }
	wall := time.Date(num(m[1]), time.Month(num(m[2])), num(m[3]), num(m[4]), num(m[5]), num(m[6]), 0, time.UTC)

	loc := time.Local
	if strings.HasSuffix(value, "Z") {
		loc = time.UTC
	} else if tzid := params["TZID"]; tzid != "" {
		if l, err := time.LoadLocation(tzid); err == nil {
			loc = l
		}
	}
	return icsTime{wall: wall, loc: loc, allDay: params["VALUE"] == "DATE" || len(value) == 8}, true
}

// instant reads the wall clock in its own zone.
func (t icsTime) instant() time.Time {
	return time.Date(t.wall.Year(), t.wall.Month(), t.wall.Day(),
		t.wall.Hour(), t.wall.Minute(), t.wall.Second(), 0, t.loc)
}

func wallKey(t time.Time) string { return t.Format("2006-01-02T15:04:05") }

type rrule struct {
	freq       string
	interval   int
	count      int
	until      time.Time
	hasUntil   bool
	byDay      []byDay
	byMonthDay []int
}

type byDay struct {
	nth int
	day time.Weekday
}

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

var byDayRe = regexp.MustCompile(`^([+-]?\d)?([A-Z]{2})$`)

func parseRRule(value string) rrule {
	r := rrule{interval: 1}
	for _, part := range strings.Split(value, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.ToUpper(kv[0]), kv[1]
		switch key {
		case "FREQ":
			r.freq = strings.ToUpper(val)
		case "INTERVAL":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				r.interval = n
			}
		case "COUNT":
			r.count, _ = strconv.Atoi(val)
		case "UNTIL":
			if t, ok := parseICSTime(val, map[string]string{}); ok {
				r.until, r.hasUntil = t.wall, true
			}
		case "BYDAY":
			for _, token := range strings.Split(val, ",") {
				if m := byDayRe.FindStringSubmatch(strings.TrimSpace(token)); m != nil {
					nth := 0
					if m[1] != "" {
						nth, _ = strconv.Atoi(m[1])
					}
					if wd, ok := weekdays[m[2]]; ok {
						r.byDay = append(r.byDay, byDay{nth: nth, day: wd})
					}
				}
			}
		case "BYMONTHDAY":
			for _, token := range strings.Split(val, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(token)); err == nil {
					r.byMonthDay = append(r.byMonthDay, n)
				}
			}
		}
	}
	return r
}

// nthWeekdayOfMonth finds e.g. the second Tuesday of the month `wall` sits in.
// A negative nth counts back from the end.
func nthWeekdayOfMonth(wall time.Time, spec byDay) time.Time {
	y, mo := wall.Year(), wall.Month()
	h, mi, s := wall.Hour(), wall.Minute(), wall.Second()

	if spec.nth > 0 {
		first := time.Date(y, mo, 1, h, mi, s, 0, time.UTC)
		shift := (int(spec.day) - int(first.Weekday()) + 7) % 7
		return first.AddDate(0, 0, shift+(spec.nth-1)*7)
	}
	last := time.Date(y, mo+1, 0, h, mi, s, 0, time.UTC)
	shift := (int(last.Weekday()) - int(spec.day) + 7) % 7
	return last.AddDate(0, 0, -shift-(abs(spec.nth)-1)*7)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// expand lists the wall-clock starts of an event inside [from, to].
func expand(start icsTime, r *rrule, from, to time.Time) []time.Time {
	if r == nil || r.freq == "" {
		return []time.Time{start.wall}
	}

	// BYDAY candidates can land before the cursor that generated them (the
	// second Tuesday precedes a cursor sitting on the 13th), so run past the
	// window by a period and let the per-candidate filter do the work.
	slack := 0
	switch r.freq {
	case "MONTHLY", "YEARLY":
		slack = 31
	case "WEEKLY":
		slack = 7
	}
	stopAfter := to.AddDate(0, 0, slack)

	out := []time.Time{}
	emitted := 0
	cursor := start.wall

	for i := 0; i < maxIterations; i++ {
		if cursor.After(stopAfter) {
			break
		}
		if r.hasUntil && cursor.After(r.until.AddDate(0, 0, slack)) {
			break
		}
		if r.count > 0 && emitted >= r.count {
			break
		}

		candidates := []time.Time{cursor}
		switch {
		case r.freq == "WEEKLY" && len(r.byDay) > 0:
			weekStart := cursor.AddDate(0, 0, -int(cursor.Weekday()))
			candidates = nil
			for _, spec := range r.byDay {
				candidates = append(candidates, weekStart.AddDate(0, 0, int(spec.day)))
			}
		case (r.freq == "MONTHLY" || r.freq == "YEARLY") && len(r.byDay) > 0:
			candidates = nil
			for _, spec := range r.byDay {
				candidates = append(candidates, nthWeekdayOfMonth(cursor, spec))
			}
		case (r.freq == "MONTHLY" || r.freq == "YEARLY") && len(r.byMonthDay) > 0:
			candidates = nil
			for _, day := range r.byMonthDay {
				candidates = append(candidates, cursor.AddDate(0, 0, day-cursor.Day()))
			}
		}

		for _, c := range candidates {
			if c.Before(start.wall) {
				continue
			}
			if r.hasUntil && c.After(r.until) {
				continue
			}
			if r.count > 0 && emitted >= r.count {
				break
			}
			emitted++
			if !c.Before(from) && !c.After(to) {
				out = append(out, c)
			}
		}

		switch r.freq {
		case "DAILY":
			cursor = cursor.AddDate(0, 0, r.interval)
		case "WEEKLY":
			cursor = cursor.AddDate(0, 0, 7*r.interval)
		case "MONTHLY":
			cursor = cursor.AddDate(0, r.interval, 0)
		case "YEARLY":
			cursor = cursor.AddDate(0, 12*r.interval, 0)
		default:
			return out // unsupported frequency: treat as a one-off
		}
	}
	return out
}

// EventsInWindow parses a calendar and returns every occurrence starting in
// [from, to], which are real instants.
func EventsInWindow(text string, from, to time.Time) []Event {
	type raw struct {
		uid, title, description, location, status string
		start, end, recurrenceID                  *icsTime
		rule                                      *rrule
		exdates                                   map[string]bool
		attendees                                 []string
	}

	events := []raw{}
	var cur *raw

	for _, line := range icsLines(text) {
		switch line.name {
		case "BEGIN":
			if line.value == "VEVENT" {
				cur = &raw{exdates: map[string]bool{}}
			}
			continue
		case "END":
			if line.value == "VEVENT" && cur != nil && cur.start != nil {
				events = append(events, *cur)
			}
			if line.value == "VEVENT" {
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}

		switch line.name {
		case "UID":
			cur.uid = line.value
		case "SUMMARY":
			cur.title = unescapeICS(line.value)
		case "DESCRIPTION":
			cur.description = unescapeICS(line.value)
		case "LOCATION":
			cur.location = unescapeICS(line.value)
		case "STATUS":
			cur.status = strings.ToUpper(line.value)
		case "DTSTART":
			if t, ok := parseICSTime(line.value, line.params); ok {
				cur.start = &t
			}
		case "DTEND":
			if t, ok := parseICSTime(line.value, line.params); ok {
				cur.end = &t
			}
		case "RRULE":
			r := parseRRule(line.value)
			cur.rule = &r
		case "RECURRENCE-ID":
			if t, ok := parseICSTime(line.value, line.params); ok {
				cur.recurrenceID = &t
			}
		case "EXDATE":
			for _, one := range strings.Split(line.value, ",") {
				if t, ok := parseICSTime(one, line.params); ok {
					cur.exdates[wallKey(t.wall)] = true
				}
			}
		case "ATTENDEE":
			name := line.params["CN"]
			if name == "" {
				name = strings.TrimPrefix(line.value, "mailto:")
			}
			if len(cur.attendees) < 12 {
				cur.attendees = append(cur.attendees, name)
			}
		}
	}

	// A VEVENT carrying RECURRENCE-ID replaces one occurrence of its series.
	overrides := map[string]bool{}
	for _, e := range events {
		if e.recurrenceID != nil {
			overrides[e.uid+"@"+wallKey(e.recurrenceID.wall)] = true
		}
	}

	// Recurrence runs on wall clock, so pad the expansion window by a day each
	// side to cover any offset, then filter precisely on the real instant.
	padFrom, padTo := from.AddDate(0, 0, -1), to.AddDate(0, 0, 1)

	out := []Event{}
	for _, e := range events {
		if e.status == "CANCELLED" {
			continue
		}
		duration := 30 * time.Minute
		if e.end != nil {
			duration = e.end.wall.Sub(e.start.wall)
		} else if e.start.allDay {
			duration = 24 * time.Hour
		}

		starts := []time.Time{e.start.wall}
		if e.recurrenceID == nil {
			starts = expand(*e.start, e.rule, padFrom, padTo)
		}

		for _, wall := range starts {
			key := wallKey(wall)
			if e.exdates[key] {
				continue
			}
			if e.recurrenceID == nil && overrides[e.uid+"@"+key] {
				continue
			}
			at := icsTime{wall: wall, loc: e.start.loc, allDay: e.start.allDay}.instant()
			if at.Before(from) || at.After(to) {
				continue
			}
			title := e.title
			if title == "" {
				title = "(untitled)"
			}
			out = append(out, Event{
				Title: title, Description: e.description, Location: e.location,
				Start: at, End: at.Add(duration), AllDay: e.start.allDay,
				Recurring: e.rule != nil, Attendees: e.attendees,
			})
		}
	}
	return out
}
