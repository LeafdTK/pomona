package main

import "testing"

func TestMutedIsForgivingAboutForm(t *testing.T) {
	mutes := []string{"#money-laundering", "hackclub/site"}
	for _, yes := range []string{"#money-laundering", "money-laundering", "#Money-Laundering", " hackclub/site "} {
		if !muted(yes, mutes) {
			t.Errorf("muted(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{"#hq-hq", "hackclub/orchard", "", "money"} {
		if muted(no, mutes) {
			t.Errorf("muted(%q) = true, want false", no)
		}
	}
}

// A mute has to be undoable, and muting twice must not grow the list.
func TestAddAndDropMute(t *testing.T) {
	m := addMute(nil, "#hq-hq")
	m = addMute(m, "#hq-hq")
	m = addMute(m, "hq-hq") // same room, written differently
	if len(m) != 1 {
		t.Errorf("got %v, want one entry", m)
	}
	if m = addMute(m, "  "); len(m) != 1 {
		t.Errorf("an empty origin was stored: %v", m)
	}

	m = addMute(m, "hackclub/site")
	if m = dropMute(m, "HQ-HQ"); len(m) != 1 || m[0] != "hackclub/site" {
		t.Errorf("unmute left %v", m)
	}
}

// The point of a mute is that it costs nothing: it applies before ranking, so
// a silenced room cannot crowd anything out on its way to being discarded.
func TestSilenceDropsMutedOrigins(t *testing.T) {
	results := []*SourceResult{{
		OK: true,
		Items: []Item{
			{Title: "keep", Origin: "#hq-hq"},
			{Title: "drop", Origin: "#money-laundering"},
			{Title: "keep too", Origin: "hackclub/orchard"},
			{Title: "a direct message", Origin: ""}, // never mutable
		},
	}}

	Silence(results, []string{"money-laundering"})

	got := []string{}
	for _, it := range results[0].Items {
		got = append(got, it.Title)
	}
	want := []string{"keep", "keep too", "a direct message"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

func TestSilenceWithNoMutesChangesNothing(t *testing.T) {
	results := []*SourceResult{{OK: true, Items: []Item{{Title: "a"}, {Title: "b"}}}}
	Silence(results, nil)
	if len(results[0].Items) != 2 {
		t.Errorf("got %d items, want 2", len(results[0].Items))
	}
}
