package main

import (
	"encoding/json"
	"testing"
	"time"
)

// The birthday-card morning: the push was to-do #1 restated.
func TestDedupePushDropsRestatedTodo(t *testing.T) {
	raw := json.RawMessage(`{
	  "push_forward": {"title": "Add your piece to the REEMDAY Figma", "body": "b", "cta_prompt": "c", "source_url": "https://figma.test/reemday", "draft": ""},
	  "top_todos": [{"title": "Add your piece to the REEMDAY Figma", "source_url": "https://figma.test/reemday", "due": ""}]
	}`)
	var out struct {
		Push struct{ Title string } `json:"push_forward"`
	}
	if err := json.Unmarshal(dedupePush(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Push.Title != "" {
		t.Errorf("push still restates the to-do: %q", out.Push.Title)
	}
}

func TestDedupePushCatchesAReworded(t *testing.T) {
	raw := json.RawMessage(`{
	  "push_forward": {"title": "Review the four new DNS record PRs on hackclub/dns", "source_url": "", "body": "", "cta_prompt": "", "draft": ""},
	  "top_todos": [{"title": "Review the four DNS record PRs on hackclub/dns", "source_url": "https://github.com/hackclub/dns/pulls"}]
	}`)
	var out struct {
		Push struct{ Title string } `json:"push_forward"`
	}
	_ = json.Unmarshal(dedupePush(raw), &out)
	if out.Push.Title != "" {
		t.Errorf("a reworded restatement survived: %q", out.Push.Title)
	}
}

func TestDedupePushKeepsARealPush(t *testing.T) {
	raw := json.RawMessage(`{
	  "push_forward": {"title": "Draft a what's new in Orchard announcement", "source_url": "https://github.com/hackclub/orchard/releases/v2.28.2", "body": "", "cta_prompt": "", "draft": "Hi all"},
	  "top_todos": [{"title": "Clear the dependency PRs on hackclub/orchard", "source_url": "https://github.com/hackclub/orchard/pulls"}]
	}`)
	var out struct {
		Push struct{ Title, Draft string } `json:"push_forward"`
	}
	_ = json.Unmarshal(dedupePush(raw), &out)
	if out.Push.Title == "" || out.Push.Draft != "Hi all" {
		t.Errorf("a genuine push was blanked: %+v", out.Push)
	}
}

func TestDedupePushSurvivesJunk(t *testing.T) {
	for _, raw := range []string{`{}`, `{"push_forward": null}`, `not json`, `{"top_todos": "x"}`} {
		if got := dedupePush(json.RawMessage(raw)); string(got) != raw {
			t.Errorf("dedupePush(%q) changed it to %q", raw, got)
		}
	}
}

// The Tuesday deck on Thursday: the writer forgot due, the store knew it.
func TestBackfillDueFromTriage(t *testing.T) {
	sig := newSignalStore()
	sig.Items["slack:x"] = &StoredItem{
		Item:   Item{Key: "slack:x", URL: "https://hackclub.slack.com/archives/G1/p1", Title: "deck"},
		Triage: &Triage{Due: "2026-09-08"},
	}
	raw := json.RawMessage(`{"top_todos": [
	  {"title": "Add your update to the deck", "source_url": "https://hackclub.slack.com/archives/G1/p1", "due": ""},
	  {"title": "Something with its own date", "source_url": "https://x/y", "due": "2026-09-11"},
	  {"title": "Something unknown", "source_url": "https://x/z", "due": ""}
	]}`)
	var out struct {
		Todos []struct{ Due string } `json:"top_todos"`
	}
	if err := json.Unmarshal(backfillDue(raw, sig), &out); err != nil {
		t.Fatal(err)
	}
	if out.Todos[0].Due != "2026-09-08" {
		t.Errorf("due not backfilled: %q", out.Todos[0].Due)
	}
	if out.Todos[1].Due != "2026-09-11" {
		t.Errorf("an existing due was overwritten: %q", out.Todos[1].Due)
	}
	if out.Todos[2].Due != "" {
		t.Errorf("a due was invented: %q", out.Todos[2].Due)
	}
	if got := backfillDue(raw, nil); string(got) != string(raw) {
		t.Error("no store should mean no change")
	}
	_ = time.Now
}

// A to-do carried from an earlier brief, whose message has left the store,
// gets the date the judge put on it.
func TestBackfillDueFromACarriedTodo(t *testing.T) {
	carried := &PastTodo{Title: "Add your update to the deck", Source: "https://hackclub.slack.com/archives/G1/p1", Due: "2026-09-08"}
	raw := json.RawMessage(`{"top_todos": [
	  {"title": "Add your update to the deck", "source_url": "https://hackclub.slack.com/archives/G1/p1", "due": ""}
	]}`)
	var out struct {
		Todos []struct {
			Due string
		} `json:"top_todos"`
	}
	if err := json.Unmarshal(backfillDue(raw, nil, carried), &out); err != nil {
		t.Fatal(err)
	}
	if out.Todos[0].Due != "2026-09-08" {
		t.Errorf("carried date not written back: %+v", out.Todos[0])
	}
}

// Only carried to-dos that are undated and still being carried go in front
// of the judge. Dated ones have a calendar; ticked, lapsed and worn-out ones
// are already gone.
func TestOnlyLiveUndatedTodosAreJudged(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	todos := []*PastTodo{
		{Title: "undated", Source: "u1", Shown: 1},
		{Title: "dated", Source: "u2", Due: "2026-09-11"},
		{Title: "done", Source: "u4", Done: true},
		{Title: "lapsed", Source: "u5", Lapsed: true},
		{Title: "worn out", Source: "u6", Shown: carriedMornings},
	}
	picked := []string{}
	for _, todo := range toJudge(todos, now) {
		picked = append(picked, todo.Title)
	}
	if len(picked) != 1 || picked[0] != "undated" {
		t.Errorf("picked %v, want just the undated one", picked)
	}
}

// The same to-do in three mornings' briefs is one to-do, three mornings
// old, not three to-dos one morning old each. That is what let the Team
// Syncs deck be "still open" on the third day.
func TestHistoryMergesTheSameTodoAcrossMornings(t *testing.T) {
	deck := func(body, due string) []byte {
		return []byte(`{"top_todos": [{"title": "Add your infra update to the Team Syncs deck", "body": "` + body +
			`", "source_url": "https://hackclub.slack.com/archives/G1/p1", "due": "` + due + `"}]}`)
	}
	past := []*Brief{
		{ID: "2026-09-09", CreatedAt: at(9, 7), Data: deck("Second morning open.", "")},
		{ID: "2026-09-08", CreatedAt: at(8, 21), Data: deck("rebeka asked at 6:48 AM.", "")},
	}
	got := historyFrom(past, 3, "2026-09-10")
	if len(got) != 2 {
		t.Fatalf("got %d days, want 2", len(got))
	}
	if len(got[0].Todos) != 0 {
		t.Errorf("the repeat was not folded away: %+v", got[0].Todos)
	}
	if len(got[1].Todos) != 1 {
		t.Fatalf("the first morning lost it: %+v", got[1].Todos)
	}
	todo := got[1].Todos[0]
	if todo.Shown != 2 || !todo.Raised.Equal(at(8, 21)) {
		t.Errorf("age wrong: shown %d raised %v", todo.Shown, todo.Raised)
	}
	if todo.Body != "Second morning open." {
		t.Errorf("the newest words did not win: %q", todo.Body)
	}
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, time.Local)
	if _, live := todo.standing(now); live {
		t.Error("two mornings undated and unticked: the third should let it go")
	}

	// A tick on any morning is a tick; a date found later is kept.
	past[0].Done = []int{0}
	past[0].Data = deck("Dated now.", "2026-09-12")
	got = historyFrom(past, 3, "2026-09-10")
	todo = got[1].Todos[0]
	if !todo.Done || todo.Due != "2026-09-12" {
		t.Errorf("tick or date lost in the merge: %+v", todo)
	}
}
