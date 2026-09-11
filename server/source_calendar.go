package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func calendarCollector() Collector {
	return Collector{
		ID: "calendar", Name: "Calendar", IconKey: "provider:calendar",
		Blurb: "Today's meetings, who's in them, and what tomorrow looks like.",
		Help: "Google Calendar → Settings → click your calendar → 'Secret address in iCal format'. " +
			"Apple and Outlook publish the same kind of link. One URL per line.",
		Fields: []Field{{Key: "urls", Label: "iCal (.ics) URLs, one per line", Type: "textarea",
			Placeholder: "https://calendar.google.com/calendar/ical/…/basic.ics"}},
		Fetch: fetchCalendar,
	}
}

func fetchCalendar(ctx context.Context, settings map[string]string, w Window) ([]Item, []Event, error) {
	urls := strings.Fields(settings["urls"])
	if len(urls) == 0 {
		return nil, nil, fmt.Errorf("no calendar URLs set")
	}

	// Today through the end of tomorrow, so the brief can talk about the day
	// ahead and what lands next.
	from := time.Date(w.Now.Year(), w.Now.Month(), w.Now.Day(), 0, 0, 0, 0, w.Now.Location())
	to := from.AddDate(0, 0, 2)

	events := []Event{}
	failures := []string{}

	for _, u := range urls {
		req, err := newRequest(ctx, "GET", u, nil)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		res, err := httpClient.Do(req)
		if err != nil {
			failures = append(failures, clip(err.Error(), 120))
			continue
		}
		body, _ := readAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			failures = append(failures, fmt.Sprintf("%s returned %d", hostOf(u), res.StatusCode))
			continue
		}
		events = append(events, EventsInWindow(string(body), from, to)...)
	}

	if len(events) == 0 && len(failures) > 0 {
		return nil, nil, fmt.Errorf("%s", strings.Join(failures, "; "))
	}

	items := make([]Item, 0, len(events))
	for _, e := range events {
		kind := "calendar.upcoming"
		if sameDay(e.Start, w.Now) {
			kind = "calendar.today"
		}
		parts := []string{}
		if e.Location != "" {
			parts = append(parts, "at "+e.Location)
		}
		if len(e.Attendees) > 0 {
			parts = append(parts, "with "+strings.Join(e.Attendees, ", "))
		}
		if e.Description != "" {
			parts = append(parts, clip(e.Description, 300))
		}
		items = append(items, Item{
			Kind: kind, Title: e.Title, Body: strings.Join(parts, " · "), Time: e.Start,
		})
	}
	return items, events, nil
}

func hostOf(raw string) string {
	if i := strings.Index(raw, "://"); i != -1 {
		rest := raw[i+3:]
		if j := strings.IndexByte(rest, '/'); j != -1 {
			return rest[:j]
		}
		return rest
	}
	return raw
}
