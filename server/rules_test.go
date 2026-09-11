package main

import (
	"testing"
	"time"
)

func TestQuestionLike(t *testing.T) {
	yes := []string{
		"does the buildkit cache mount work with orchardctl?",
		"How do I set the cache mounts",
		"anyone know if this is expected",
		"is the deploy stuck",
	}
	no := []string{
		"?", "??", "ok", "thanks all",
		"we shipped v2.28 today",
		"the answer is yes.",
	}
	for _, s := range yes {
		if !questionLike(s) {
			t.Errorf("questionLike(%q) = false", s)
		}
	}
	for _, s := range no {
		if questionLike(s) {
			t.Errorf("questionLike(%q) = true", s)
		}
	}
}

// Dia's item: asked yesterday, only the auto-bot replied, you're the one who
// would know.
func TestUnansweredIgnoresBotOnlyReplies(t *testing.T) {
	now := time.Now()
	yesterday := now.Add(-20 * time.Hour)
	isBot := func(id string) bool { return id == "B_AUTO" }

	if !unanswered("does buildkit cache work here?", yesterday, now, 1, []string{"B_AUTO"}, isBot, "U_ME") {
		t.Error("a question answered only by a bot is unanswered")
	}
	if unanswered("does buildkit cache work here?", yesterday, now, 2, []string{"B_AUTO", "U_HUMAN"}, isBot, "U_ME") {
		t.Error("a person replied: not unanswered")
	}
	if unanswered("does buildkit cache work here?", yesterday, now, 1, []string{"U_ME"}, isBot, "U_ME") {
		t.Error("the reader replied: not unanswered")
	}
	if !unanswered("does buildkit cache work here?", yesterday, now, 0, nil, isBot, "U_ME") {
		t.Error("no replies at all is unanswered")
	}
}

func TestUnansweredRespectsAge(t *testing.T) {
	now := time.Now()
	isBot := func(string) bool { return false }
	if unanswered("is this broken?", now.Add(-10*time.Minute), now, 0, nil, isBot, "me") {
		t.Error("ten minutes old: someone may still be typing")
	}
	if unanswered("is this broken?", now.Add(-5*24*time.Hour), now, 0, nil, isBot, "me") {
		t.Error("five days old: history, not a question")
	}
	if !unanswered("is this broken?", now.Add(-3*time.Hour), now, 0, nil, isBot, "me") {
		t.Error("three hours old with no reply is unanswered")
	}
	if unanswered("we shipped it", now.Add(-3*time.Hour), now, 0, nil, isBot, "me") {
		t.Error("not a question")
	}
}

func TestVersionsIn(t *testing.T) {
	got := versionsIn("shipped v2.27 and v2.28.2, then 2.28.2 again, ip 10.0.0.1 is not a version")
	want := []string{"2.27", "2.28.2", "10.0.0"}
	if len(got) != len(want) {
		t.Fatalf("versionsIn = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("versionsIn = %v, want %v", got, want)
		}
	}
}

// Dia's push: you shipped v2.27 through v2.28.2 and #orchard-support does not
// know half of it exists.
func TestShippedNotAnnounced(t *testing.T) {
	now := time.Now()
	releases := []Release{
		{Repo: "hackclub/orchard", Tag: "v2.27.0", At: now.Add(-50 * time.Hour), URL: "u1"},
		{Repo: "hackclub/orchard", Tag: "v2.28.2", At: now.Add(-5 * time.Hour), URL: "u2"},
		{Repo: "hackclub/orchard", Tag: "v2.20.0", At: now.Add(-10 * 24 * time.Hour), URL: "old"}, // too old to matter
		{Repo: "hackclub/dns", Tag: "v1.0.0", At: now.Add(-1 * time.Hour), URL: "u3"},             // no room for it
	}
	rooms := func(repo string) []string {
		if repo == "hackclub/orchard" {
			return []string{"orchard-support"}
		}
		return nil
	}
	said := func(room string) string { return "someone: is 2.26 still current? | you: yes for now" }

	got := unannounced(releases, rooms, said, now)
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(got), got)
	}
	it := got[0]
	if it.Kind != "github.shipped" || it.Origin != "hackclub/orchard" {
		t.Errorf("item = %+v", it)
	}
	if !contains(it.Title, "v2.27.0 through v2.28.2") || !contains(it.Title, "#orchard-support") {
		t.Errorf("title = %q", it.Title)
	}
	if it.URL != "u2" {
		t.Errorf("url should be the newest release, got %q", it.URL)
	}

	// Once the room has heard, there is nothing to say.
	heard := func(room string) string { return "you: v2.28.2 is out, and 2.27 before it" }
	if got := unannounced(releases, rooms, heard, now); len(got) != 0 {
		t.Errorf("announced releases still reported: %+v", got)
	}
}

func TestRoomsForRepoUsesOwnedRoomsOnly(t *testing.T) {
	own := Ownership{
		"orchard-support":  &Owned{Kind: "channel", Score: 0.8},
		"orchard-noise":    &Owned{Kind: "channel", Score: 0.1}, // shares the name, not owned
		"hackclub/orchard": &Owned{Kind: "repo", Score: 0.9},
	}
	got := roomsForRepo(own, "hackclub/orchard")
	if len(got) != 1 || got[0] != "orchard-support" {
		t.Errorf("rooms = %v, want just orchard-support", got)
	}
}
