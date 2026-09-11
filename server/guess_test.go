package main

import "testing"

// Whichever source answered first used to win, so the result depended on
// network timing and picked a GitHub handle over a real name.
func TestGuessMergesInAFixedOrder(t *testing.T) {
	slack := Guess{Name: "Sebastian", Role: "Infra, Hack Club", Timezone: "America/Mexico_City"}
	github := Guess{Name: "Leafd", Role: "I make stuff"}

	got := mergeGuesses(map[string]Guess{"GitHub": github, "Slack": slack})
	if got.Name != "Sebastian" {
		t.Errorf("name = %q, want the Slack one", got.Name)
	}
	if got.Role != "Infra, Hack Club" {
		t.Errorf("role = %q, want the job title over the bio", got.Role)
	}
	if len(got.From) != 2 || got.From[0] != "Slack" {
		t.Errorf("from = %v, want Slack first", got.From)
	}
}

// A source that answers nothing is not credited, and the other one still fills
// in whatever it can.
func TestGuessFallsBackFieldByField(t *testing.T) {
	got := mergeGuesses(map[string]Guess{
		"Slack":  {Timezone: "America/Mexico_City"}, // connected, but no name set
		"GitHub": {Name: "Leafd", Role: "I make stuff"},
	})
	if got.Name != "Leafd" || got.Role != "I make stuff" {
		t.Errorf("did not fall through to GitHub: %+v", got)
	}
	if got.Timezone != "America/Mexico_City" {
		t.Errorf("lost the Slack timezone: %+v", got)
	}
	if len(got.From) != 2 {
		t.Errorf("from = %v, want both credited", got.From)
	}
}

func TestGuessCreditsNobodyWhoKnewNothing(t *testing.T) {
	got := mergeGuesses(map[string]Guess{"Slack": {}, "GitHub": {Name: "Leafd"}})
	if len(got.From) != 1 || got.From[0] != "GitHub" {
		t.Errorf("from = %v, want only GitHub", got.From)
	}
}

func TestGuessOnNothingAtAll(t *testing.T) {
	got := mergeGuesses(nil)
	if got.Name != "" || got.Role != "" || len(got.From) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// The brief greets people the way a colleague would.
func TestFirstName(t *testing.T) {
	cases := map[string]string{
		"Sebastian Sanchez Mota": "Sebastian",
		"Sebastian":              "Sebastian",
		"  padded  name ":        "padded",
		"":                       "",
	}
	for in, want := range cases {
		if got := firstName(in); got != want {
			t.Errorf("firstName(%q) = %q, want %q", in, got, want)
		}
	}
}
