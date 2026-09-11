package main

import (
	"testing"
	"time"
)

func at(day int, hour int) time.Time {
	return time.Date(2026, 9, day, hour, 0, 0, 0, time.Local)
}

// A brief covers what happened since the last one. The alternative, a fixed
// window, makes every morning re-read the mornings before it.
func TestWindowStartsWhereTheLastBriefStopped(t *testing.T) {
	now := at(9, 8) // Wednesday morning
	cfg := &Config{Lookback: 72}

	cases := []struct {
		name   string
		briefs []*Brief
		want   time.Time
	}{
		{
			name:   "no briefs yet, so reach as far as configured",
			briefs: nil,
			want:   now.Add(-72 * time.Hour),
		},
		{
			name:   "yesterday's brief is the anchor",
			briefs: []*Brief{{ID: "2026-09-08", CreatedAt: at(8, 7)}},
			want:   at(8, 7),
		},
		{
			name: "today's own brief is skipped, so rewriting covers the same ground",
			briefs: []*Brief{
				{ID: "2026-09-09", CreatedAt: at(9, 7)},
				{ID: "2026-09-08", CreatedAt: at(8, 7)},
			},
			want: at(8, 7),
		},
		{
			name:   "a brief older than the configured reach does not extend it",
			briefs: []*Brief{{ID: "2026-08-20", CreatedAt: at(1, 7)}},
			want:   now.Add(-72 * time.Hour),
		},
		{
			name:   "a brief with no timestamp is not an anchor",
			briefs: []*Brief{{ID: "2026-09-08"}},
			want:   now.Add(-72 * time.Hour),
		},
	}

	for _, c := range cases {
		got := windowFor(c.briefs, cfg, now)
		if !got.Since.Equal(c.want) {
			t.Errorf("%s: since = %v, want %v", c.name, got.Since, c.want)
		}
		if !got.Now.Equal(now) {
			t.Errorf("%s: now = %v, want %v", c.name, got.Now, now)
		}
	}
}

// An unfinished to-do has to survive into the next brief with enough of itself
// to be reissued: the message behind it will be outside tomorrow's window.
func TestHistoryCarriesUnfinishedTodosWhole(t *testing.T) {
	data := []byte(`{
	  "push_forward": {"title": "a suggestion nobody acted on"},
	  "top_todos": [
	    {"title": "Add your piece to the REEMDAY Figma", "body": "Reem's birthday is Friday.",
	     "source_url": "https://figma.test/reemday"},
	    {"title": "Already handled", "body": "b", "source_url": "https://x.test/done"}
	  ]
	}`)
	past := []*Brief{{ID: "2026-09-08", CreatedAt: at(8, 7), Data: data, Done: []int{1}}}

	got := historyFrom(past, 3, "2026-09-09")
	if len(got) != 1 {
		t.Fatalf("got %d days, want 1", len(got))
	}
	day := got[0]
	if day.Pushed != "a suggestion nobody acted on" {
		t.Errorf("push not carried: %q", day.Pushed)
	}
	if len(day.Todos) != 2 {
		t.Fatalf("got %d todos, want 2", len(day.Todos))
	}

	open := day.Todos[0]
	if open.Done {
		t.Error("the unfinished to-do came back marked done")
	}
	if open.Body == "" || open.Source != "https://figma.test/reemday" {
		t.Errorf("an unfinished to-do lost what it needs to be reissued: %+v", open)
	}
	if !day.Todos[1].Done {
		t.Error("the ticked to-do did not come back done")
	}
}

func TestHistorySkipsTodaysOwnBrief(t *testing.T) {
	todo := func(title string) []byte {
		return []byte(`{"push_forward":{"title":""},"top_todos":[{"title":"` + title + `","body":"","source_url":""}]}`)
	}
	past := []*Brief{
		{ID: "2026-09-09", CreatedAt: at(9, 7), Data: todo("this morning's earlier attempt")},
		{ID: "2026-09-08", CreatedAt: at(8, 7), Data: todo("yesterday")},
	}

	got := historyFrom(past, 3, "2026-09-09")
	for _, day := range got {
		if day.ID == "2026-09-09" {
			t.Error("the brief being replaced was fed back in as history")
		}
	}
	if len(got) != 1 || got[0].ID != "2026-09-08" {
		t.Fatalf("got %d days, want just yesterday", len(got))
	}
}

// The Team Syncs deck was collected on Tuesday. On Wednesday morning it is not
// a to-do, it is over, and a brief that keeps asking for it is one nobody
// trusts. The server counts the days so the model never has to.
func TestStandingOfCountsTheDays(t *testing.T) {
	now := time.Date(2026, 9, 9, 7, 30, 0, 0, time.Local) // Wednesday

	cases := []struct {
		due  string
		want string
		live bool
	}{
		{"", "still open", true},
		{"2026-09-09", "still open, DUE TODAY", true},
		{"2026-09-10", "still open, due tomorrow Thursday", true},
		{"2026-09-11", "still open, due Friday Sep 11, 2 days left", true},
		{"2026-09-30", "still open, due Wednesday Sep 30", true},
		{"2026-09-08", "", false}, // yesterday: over
		{"2026-01-01", "", false},
		{"not a date", "still open", true}, // never drop on a parse failure
	}
	for _, c := range cases {
		state, live := standingOf(c.due, now)
		if state != c.want || live != c.live {
			t.Errorf("standingOf(%q) = (%q, %v), want (%q, %v)", c.due, state, live, c.want, c.live)
		}
	}
}

// An expired item must not reach the model at all: mentioning it is how it
// ends up in the brief anyway.
func TestExpiredTodosAreNotCarried(t *testing.T) {
	now := time.Date(2026, 9, 9, 7, 30, 0, 0, time.Local)
	cfg := &Config{}
	cfg.Profile.Name = "Sebastian"
	past := []DayHistory{{
		ID: "2026-09-08",
		Todos: []PastTodo{
			{Title: "Deck that was due yesterday", Due: "2026-09-08"},
			{Title: "Figma that is due Friday", Due: "2026-09-11"},
		},
	}}

	prompt := BuildPrompt(cfg, now, nil, nil, past, nil)
	if contains(prompt, "Deck that was due yesterday") {
		t.Error("an expired to-do was still put in front of the model")
	}
	if !contains(prompt, "Figma that is due Friday") {
		t.Error("a live to-do was dropped")
	}
	if !contains(prompt, "2 days left") {
		t.Error("the days were not counted for the model")
	}
}

func TestSpokenDayNamesPastMornings(t *testing.T) {
	now := time.Date(2026, 9, 9, 7, 30, 0, 0, time.Local) // Wednesday
	cases := map[string]string{
		"2026-09-09": "today",
		"2026-09-08": "yesterday, Tuesday",
		"2026-09-07": "Monday, 2 days ago",
		"2026-09-04": "Friday, 5 days ago",
		"2026-08-20": "Thursday Aug 20",
		"not a date": "not a date",
	}
	for key, want := range cases {
		if got := spokenDay(key, now); got != want {
			t.Errorf("spokenDay(%q) = %q, want %q", key, got, want)
		}
	}
}

// A to-do carried out of Tuesday has to arrive labelled Tuesday, or there is
// no way to tell that "add to today's sync deck" was owed then and not now.
func TestCarriedTodosSayWhichDayAskedForThem(t *testing.T) {
	now := time.Date(2026, 9, 9, 7, 30, 0, 0, time.Local)
	cfg := &Config{}
	cfg.Profile.Name = "Sebastian"
	past := []DayHistory{{
		ID:    "2026-09-08",
		Todos: []PastTodo{{Title: "Add to the Team Syncs deck"}},
	}}

	prompt := BuildPrompt(cfg, now, nil, nil, past, nil)
	if !contains(prompt, "yesterday, Tuesday") {
		t.Errorf("the day that raised it was not named:\n%s", prompt)
	}
	if !contains(prompt, "Add to the Team Syncs deck") {
		t.Error("the to-do itself was dropped")
	}
}

// An undated to-do nobody could put a date on is shown two mornings and then
// let go. Carried forever, it is not a to-do, it is a thing the brief says.
func TestUndatedTodosAgeOut(t *testing.T) {
	now := time.Date(2026, 9, 12, 8, 0, 0, 0, time.Local)
	if _, live := (PastTodo{Shown: 1}).standing(now); !live {
		t.Error("one morning undated should still be carried")
	}
	if state, live := (PastTodo{Shown: carriedMornings}).standing(now); live {
		t.Errorf("worn out and still carried as %q", state)
	}
	if _, live := (PastTodo{}).standing(now); !live {
		t.Error("never shown: keep it")
	}
	if _, live := (PastTodo{Due: "2026-09-20", Shown: 30}).standing(now); !live {
		t.Error("a dated to-do is judged by its date, however old")
	}
	if _, live := (PastTodo{Lapsed: true}).standing(now); live {
		t.Error("a lapsed to-do was carried")
	}
	if _, live := (PastTodo{Done: true}).standing(now); live {
		t.Error("a ticked to-do was carried")
	}
}
