package main

import (
	"testing"
	"time"
)

func stamp(day, hour int) time.Time {
	return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC)
}

func thread(key string, lines ...Line) Item {
	return Item{Key: key, Source: "slack", Kind: "slack.thread_reply", Title: "a thread", URL: "https://x/" + key, Lines: lines, Time: stamp(9, 9)}
}

// A thread seen twice is one thread. Lines that arrived after the first
// sighting are appended; lines already held are not repeated.
func TestMergeAppendsLinesOnce(t *testing.T) {
	s := newSignalStore()
	first := s.Merge([]Item{thread("t1",
		Line{TS: "1.0", Who: "nora", Text: "is auth down?"},
		Line{TS: "2.0", Who: "rowan", Text: "looking"},
	)}, stamp(9, 8))
	if len(first) != 1 || first[0].Updates != 0 {
		t.Fatalf("first sighting: %+v", first)
	}

	// The next read overlaps (cursor is exclusive but a resend can happen)
	// and brings one genuinely new line.
	second := s.Merge([]Item{thread("t1",
		Line{TS: "2.0", Who: "rowan", Text: "looking"},
		Line{TS: "3.0", Who: "rowan", Text: "fixed, it was the rule"},
	)}, stamp(10, 8))
	if len(second) != 1 {
		t.Fatalf("a grown thread was not reported as fresh: %d", len(second))
	}
	got := s.Items["t1"]
	if len(got.Lines) != 3 || got.Updates != 1 {
		t.Errorf("lines=%d updates=%d, want 3 and 1", len(got.Lines), got.Updates)
	}
	if !contains(got.Body, "fixed, it was the rule") || !contains(got.Body, "is auth down?") {
		t.Errorf("body lost an end: %q", got.Body)
	}
	if got.FirstSeen != stamp(9, 8) || got.LastSeen != stamp(10, 8) {
		t.Errorf("seen times wrong: %v %v", got.FirstSeen, got.LastSeen)
	}

	// Seeing exactly the same thing a third time is not news.
	third := s.Merge([]Item{thread("t1", Line{TS: "3.0", Who: "rowan", Text: "fixed, it was the rule"})}, stamp(10, 9))
	if len(third) != 0 {
		t.Errorf("an unchanged thread was reported fresh")
	}
}

func TestMergeMarksYourOwnLines(t *testing.T) {
	s := newSignalStore()
	s.Merge([]Item{thread("t2",
		Line{TS: "1.0", Who: "nora", Text: "anyone?"},
		Line{TS: "2.0", Who: "sebastian", Text: "on it", Mine: true},
	)}, stamp(9, 8))
	if !contains(s.Items["t2"].Body, "you: on it") {
		t.Errorf("the reader's line is not marked as theirs: %q", s.Items["t2"].Body)
	}
}

// A non-conversation that comes back changed is fresh; unchanged is not.
func TestMergeNoticesAChangedItem(t *testing.T) {
	s := newSignalStore()
	pr := Item{Key: "github:pr1", Source: "github", Kind: "github.review_requested", Title: "Fix auth", Body: "Waiting on a review.", Time: stamp(9, 8)}
	s.Merge([]Item{pr}, stamp(9, 8))
	if fresh := s.Merge([]Item{pr}, stamp(10, 8)); len(fresh) != 0 {
		t.Error("an unchanged PR was reported fresh")
	}
	pr.Body = "Checks are green: it is waiting on your review."
	if fresh := s.Merge([]Item{pr}, stamp(10, 9)); len(fresh) != 1 || fresh[0].Updates != 1 {
		t.Errorf("a changed PR was not reported fresh: %+v", fresh)
	}
}

// An open PR that stops coming back was merged or closed. That is news.
func TestResolvedMarksWhatVanished(t *testing.T) {
	s := newSignalStore()
	s.Merge([]Item{
		{Key: "github:a", Source: "github", Kind: "github.review_requested", Title: "a", Time: stamp(9, 8)},
		{Key: "github:b", Source: "github", Kind: "github.review_requested", Title: "b", Time: stamp(9, 8)},
		{Key: "github:n", Source: "github", Kind: "github.notification", Title: "n", Time: stamp(9, 8)},
	}, stamp(9, 8))

	gone := s.Resolved("github", []string{"github.review_requested"}, map[string]bool{"github:a": true}, stamp(10, 8))
	if len(gone) != 1 || gone[0].Key != "github:b" {
		t.Fatalf("resolved = %+v, want just b", gone)
	}
	if !hasTag(s.Items["github:b"].Tags, "resolved") {
		t.Error("b was not tagged")
	}
	if hasTag(s.Items["github:n"].Tags, "resolved") {
		t.Error("a notification was resolved: only the listed kinds count")
	}
	if again := s.Resolved("github", []string{"github.review_requested"}, map[string]bool{"github:a": true}, stamp(11, 8)); len(again) != 0 {
		t.Error("b was resolved twice")
	}
}

func TestPruneKeepsRecentAndCaps(t *testing.T) {
	s := newSignalStore()
	now := stamp(10, 8)
	s.Merge([]Item{
		{Key: "old", Source: "slack", Title: "old", Time: now.Add(-4 * 24 * time.Hour)},
		{Key: "new", Source: "slack", Title: "new", Time: now.Add(-1 * time.Hour)},
	}, now.Add(-4*24*time.Hour))
	s.Items["new"].LastSeen = now
	s.Prune(now)
	if s.Items["old"] != nil || s.Items["new"] == nil {
		t.Errorf("prune kept %v", s.Items)
	}

	// Over the cap, the oldest go.
	big := newSignalStore()
	items := []Item{}
	for i := 0; i < signalsMax+50; i++ {
		items = append(items, Item{Key: "k" + itoa(i), Source: "slack", Title: "x", Time: now.Add(-time.Duration(i) * time.Minute)})
	}
	big.Merge(items, now)
	big.Prune(now)
	if len(big.Items) != signalsMax {
		t.Errorf("got %d items, want the cap %d", len(big.Items), signalsMax)
	}
	if big.Items["k0"] == nil || big.Items["k"+itoa(signalsMax+49)] != nil {
		t.Error("the wrong end was pruned")
	}
}

// The rest of the pipeline reads what CollectAll used to return. The store
// has to hand back exactly that shape, one result per enabled source.
func TestResultsSinceMatchesCollectAllShape(t *testing.T) {
	cfg := defaultConfig()
	cfg.Sources["slack"] = map[string]string{"enabled": "true"}
	cfg.Sources["github"] = map[string]string{"enabled": "true"}
	cfg.Sources["calendar"] = map[string]string{"enabled": "true"}

	s := newSignalStore()
	s.Merge([]Item{
		{Key: "slack:1", Source: "slack", Kind: "slack.dm", Title: "recent", Time: stamp(10, 7)},
		{Key: "slack:2", Source: "slack", Kind: "slack.dm", Title: "stale", Time: stamp(8, 7)},
		{Key: "github:1", Source: "github", Kind: "github.review_requested", Title: "pr", Time: stamp(10, 6)},
	}, stamp(10, 8))
	s.Events = []Event{{Title: "standup", Start: stamp(10, 9)}}
	s.Reports = []SourceReport{{ID: "github", OK: false, Error: "github 401"}}

	results := s.ResultsSince(stamp(9, 20), cfg)
	byID := map[string]*SourceResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want one per enabled source", len(results))
	}
	if got := byID["slack"]; got == nil || len(got.Items) != 1 || got.Items[0].Title != "recent" {
		t.Errorf("slack = %+v, want only the recent item", byID["slack"])
	}
	if got := byID["github"]; got == nil || got.OK || got.Err != "github 401" {
		t.Errorf("github should carry last time's failure: %+v", byID["github"])
	}
	if got := byID["calendar"]; got == nil || len(got.Events) != 1 {
		t.Errorf("calendar events not carried: %+v", byID["calendar"])
	}
	if byID["linear"] != nil {
		t.Error("a disabled source appeared")
	}
}

func TestCursorSetAdvances(t *testing.T) {
	s := newSignalStore()
	cs := s.CursorSet()
	if got := cs.Get("slack:C1"); got.TS != "" {
		t.Errorf("fresh cursor = %+v", got)
	}
	cs.Advance("slack:C1", "1700.5", stamp(9, 8), true)
	cs.Advance("slack:C1", "1600.0", stamp(9, 9), false) // older ts never moves it back
	got := cs.Get("slack:C1")
	if got.TS != "1700.5" || got.Empty != 1 || got.LastSweep != stamp(9, 9) {
		t.Errorf("cursor = %+v", got)
	}
	cs.Advance("slack:C1", "1800.0", stamp(9, 10), true)
	if got := cs.Get("slack:C1"); got.TS != "1800.0" || got.Empty != 0 {
		t.Errorf("cursor after new = %+v", got)
	}
	// It is the store's own map, so the advance persists with the store.
	if s.Cursors["slack:C1"].TS != "1800.0" {
		t.Error("CursorSet did not write through to the store")
	}
	var none *CursorSet
	none.Advance("x", "1", stamp(9, 8), true) // must not panic
	if none.Get("x").TS != "" {
		t.Error("nil set returned something")
	}
}

func TestChannelMetaCountsAndDecays(t *testing.T) {
	s := newSignalStore()
	m := s.NoteChannel("C1", "orchard-support", false, "U_ME")
	m.Spoke(true, stamp(9, 8))
	m.Spoke(false, stamp(9, 9))
	m.Spoke(true, stamp(9, 10))
	if m.MyPosts != 2 || m.Posts != 3 || m.LastSpoke != stamp(9, 10) || m.LastOther != stamp(9, 9) {
		t.Errorf("meta = %+v", m)
	}
	// First sight starts the clock and touches nothing: decaying a room on
	// the morning it was first seen threw away the one post it had.
	s.Decay(stamp(9, 10))
	if m.MyPosts != 2 || m.Decayed != stamp(9, 10) {
		t.Errorf("first decay should only stamp: %+v", m)
	}
	s.Decay(stamp(10, 10)) // less than a week: untouched
	if m.MyPosts != 2 {
		t.Error("decayed within a week")
	}
	s.Decay(stamp(9, 10).Add(8 * 24 * time.Hour))
	if m.MyPosts != 1 || m.Posts != 2 {
		t.Errorf("after a week = %+v", m)
	}
	if s.ChannelByName("#Orchard-Support") != m {
		t.Error("lookup by name failed")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := ""
	for i > 0 {
		out = string(rune('0'+i%10)) + out
		i /= 10
	}
	return out
}
