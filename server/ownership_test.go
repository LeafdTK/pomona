package main

import (
	"testing"
	"time"
)

// The Dia case: a support channel is yours because the repository is.
func TestOwnershipFromChannelAndRepoEvidence(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	sig := newSignalStore()
	sig.Me, sig.me = "U_ME", "U_ME"

	// A room the reader runs: they made it and write most of it.
	m := sig.NoteChannel("C1", "orchard-support", false, "U_ME")
	for i := 0; i < 6; i++ {
		m.Spoke(true, now.Add(-time.Duration(i)*time.Hour))
	}
	for i := 0; i < 4; i++ {
		m.Spoke(false, now.Add(-time.Duration(i)*time.Hour))
	}
	// A room they are merely in.
	quiet := sig.NoteChannel("C2", "random", false, "U_OTHER")
	for i := 0; i < 40; i++ {
		quiet.Spoke(false, now)
	}
	quiet.Spoke(true, now.Add(-20*24*time.Hour))

	cfg := defaultConfig() // no GitHub token, so no network
	own := InferOwnership(t.Context(), cfg, sig, nil, now)

	if !own.Owns("#orchard-support") {
		t.Errorf("orchard-support not owned: %+v", own["orchard-support"])
	}
	if own.Owns("#random") {
		t.Errorf("random owned on one old post: %+v", own["random"])
	}
	o := own["orchard-support"]
	if o == nil || len(o.Why) == 0 || o.Kind != "channel" {
		t.Fatalf("no reasons given: %+v", o)
	}
	if !contains(o.Why[0], "you made this channel") && !contains(o.Why[0], "you wrote") {
		t.Errorf("reasons are not in the reader's terms: %v", o.Why)
	}
}

func TestOwnershipHonoursWhatTheReaderSaid(t *testing.T) {
	now := time.Now()
	sig := newSignalStore()
	sig.NoteChannel("C1", "loud", false, "").Spoke(false, now) // nothing of theirs
	cfg := defaultConfig()
	cfg.Owns = []string{"#loud", "hackclub/orchard"}

	own := InferOwnership(t.Context(), cfg, sig, nil, now)
	if !own.Owns("#loud") || !own["loud"].Pinned {
		t.Errorf("a pinned room is not owned: %+v", own["loud"])
	}
	if !own.Owns("hackclub/orchard") || own["hackclub/orchard"].Kind != "repo" {
		t.Errorf("a pinned repo is not owned as a repo: %+v", own["hackclub/orchard"])
	}

	// Disowning wins over everything, including evidence.
	strong := sig.NoteChannel("C2", "mine", false, "U_ME")
	sig.Me, sig.me = "U_ME", "U_ME"
	for i := 0; i < 10; i++ {
		strong.Spoke(true, now)
	}
	cfg.Disowns = []string{"#mine"}
	own = InferOwnership(t.Context(), cfg, sig, nil, now)
	if own.Owns("#mine") {
		t.Error("a disowned room is still owned")
	}
}

func TestSharesAWordIgnoresTheDullOnes(t *testing.T) {
	yes := [][2]string{
		{"#orchard-support", "hackclub/orchard"},
		{"#hackatime-desktop", "hackclub/hackatime-desktop"},
		{"#orchard", "LeafdTK/orchard-cli"},
	}
	no := [][2]string{
		{"#general", "hackclub/general-tools"},
		{"#support", "hackclub/orchard"},
		{"#hq-hq", "hackclub/hq"},
	}
	for _, c := range yes {
		if !shareAWord(c[0], c[1]) {
			t.Errorf("%s and %s should share a word", c[0], c[1])
		}
	}
	for _, c := range no {
		if shareAWord(c[0], c[1]) {
			t.Errorf("%s and %s should not share a word", c[0], c[1])
		}
	}
}

func TestRankedPutsPinsFirstThenScore(t *testing.T) {
	own := Ownership{
		"weak":   &Owned{Score: 0.4},
		"strong": &Owned{Score: 0.9},
		"pinned": &Owned{Score: 0.1, Pinned: true},
		"below":  &Owned{Score: 0.1},
	}
	got := own.Ranked(10)
	want := []string{"pinned", "strong", "weak"}
	if len(got) != len(want) {
		t.Fatalf("ranked = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ranked = %v, want %v", got, want)
			break
		}
	}
	if len(own.Ranked(1)) != 1 {
		t.Error("limit ignored")
	}
}

func TestOwnsOnNil(t *testing.T) {
	var none Ownership
	if none.Owns("#anything") {
		t.Error("nil ownership owns something")
	}
}

// The correlation the reader loved: a room they have never posted in is
// theirs because it shares a name with a repository they own.
func TestARoomIsOwnedByItsRepoAlone(t *testing.T) {
	now := time.Now()
	sig := newSignalStore()
	sig.NoteChannel("C1", "orchard-support", false, "U_SOMEONE") // never spoke here
	cfg := defaultConfig()
	cfg.Owns = []string{"hackclub/orchard"} // stands in for push/merge evidence

	own := InferOwnership(t.Context(), cfg, sig, nil, now)
	if !own.Owns("#orchard-support") {
		t.Fatalf("orchard-support not owned by correlation: %+v", own["orchard-support"])
	}
	if o := own["orchard-support"]; len(o.Why) == 0 || !contains(o.Why[0], "shares a name with hackclub/orchard") {
		t.Errorf("no correlation reason given: %v", o.Why)
	}
}
