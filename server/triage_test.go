package main

import (
	"strings"
	"testing"
	"time"
)

// Triage annotates and reorders. It never gets to delete a direct message,
// a review request, a severe advisory, or anything in an owned room.
func TestFloorKeepsWhatMustReachTheWriter(t *testing.T) {
	own := Ownership{"orchard-support": &Owned{Kind: "channel", Score: 0.9}}
	cases := []struct {
		name string
		item Item
		said int
		want int
	}{
		{"a DM called noise", Item{Kind: "slack.dm"}, 5, 60},
		{"a review request called noise", Item{Kind: "github.review_requested"}, 0, 60},
		{"a critical advisory", Item{Kind: "github.review_requested", Tags: []string{"critical"}}, 10, 60},
		{"an unanswered question", Item{Kind: "slack.unanswered"}, 20, 60},
		{"something in an owned room", Item{Kind: "slack.broadcast", Origin: "#orchard-support"}, 10, 50},
		{"a real noise", Item{Kind: "github.notification", Origin: "hackclub/dns"}, 5, 5},
		{"a high score is kept", Item{Kind: "slack.dm"}, 95, 95},
	}
	for _, c := range cases {
		if got := floorFor(c.item, own, c.said); got != c.want {
			t.Errorf("%s: floorFor = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestValidDue(t *testing.T) {
	if validDue("2026-09-11") != "2026-09-11" || validDue(" 2026-09-11 ") != "2026-09-11" {
		t.Error("a real date was rejected")
	}
	for _, bad := range []string{"", "tomorrow", "2026-9-1", "Friday", "none"} {
		if validDue(bad) != "" {
			t.Errorf("validDue(%q) accepted junk", bad)
		}
	}
}

func TestTriagePromptMarksOwnedPlaces(t *testing.T) {
	own := Ownership{"orchard-support": &Owned{Kind: "channel", Score: 0.9}}
	batch := []*StoredItem{
		{Item: Item{Key: "k1", Kind: "slack.broadcast", Origin: "#orchard-support", Title: "t", Body: strings.Repeat("x", 500), Time: time.Now()}},
		{Item: Item{Key: "k2", Kind: "slack.broadcast", Origin: "#random", Title: "t2"}},
	}
	got := triagePrompt(batch, own, time.Now())
	if !strings.Contains(got, "where: #orchard-support (owned)") {
		t.Errorf("owned room not marked:\n%s", got)
	}
	if strings.Contains(got, "where: #random (owned)") {
		t.Error("an unowned room was marked owned")
	}
	if !strings.Contains(got, "Places this person owns: #orchard-support") {
		t.Error("the owned list is missing")
	}
	// Bodies are clipped: the small model does not get the whole thread.
	if strings.Count(got, "x") > triageBodyChars+5 {
		t.Error("body was not clipped for triage")
	}
}

// The shortlist drops what triage called noise, keeps what it must, and
// never lets one source flood the writer.
func TestShortlistDropsNoiseKeepsTheFloor(t *testing.T) {
	sig := newSignalStore()
	own := Ownership{}
	ledger := Ledger{}
	now := time.Now()

	mk := func(key, kind, origin string, class string) Item {
		it := Item{Key: key, Source: strings.Split(key, ":")[0], Kind: kind, Origin: origin, URL: "https://x/" + key, Time: now}
		sig.Items[key] = &StoredItem{Item: it, Triage: &Triage{Class: class, Score: 50}}
		return it
	}
	results := []*SourceResult{
		{ID: "slack", OK: true, Items: []Item{
			mk("slack:dm", "slack.dm", "", "noise"), // triage is wrong about this one
			mk("slack:b", "slack.broadcast", "#random", "noise"),
			mk("slack:m", "slack.mention", "#hq", "ask_you"),
		}},
		{ID: "github", OK: true, Items: func() []Item {
			out := []Item{}
			for i := 0; i < 20; i++ {
				out = append(out, mk("github:n"+itoa(i), "github.notification", "hackclub/dns", "fyi"))
			}
			return out
		}()},
	}

	Shortlist(results, sig, ledger, own)

	slack := results[0].Items
	if len(slack) != 2 {
		t.Fatalf("slack kept %d, want the DM and the mention", len(slack))
	}
	for _, it := range slack {
		if it.Key == "slack:b" {
			t.Error("noise survived")
		}
	}
	if len(results[1].Items) > shortlistPerSource {
		t.Errorf("github flooded with %d items", len(results[1].Items))
	}
}

func TestShortlistWorksWithoutTriage(t *testing.T) {
	results := []*SourceResult{{ID: "slack", OK: true, Items: []Item{
		{Key: "slack:1", Source: "slack", Kind: "slack.dm", URL: "https://x/1", Time: time.Now()},
	}}}
	Shortlist(results, nil, Ledger{}, nil)
	if len(results[0].Items) != 1 {
		t.Error("an untriaged morning lost its items")
	}
}
