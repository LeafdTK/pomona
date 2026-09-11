package main

import (
	"testing"
	"time"
)

func shown(l Ledger, origin string, times int, now time.Time) {
	for i := 0; i < times; i++ {
		l.Saw([]Item{{Origin: origin}}, now)
	}
}

// Three quiet mornings is not a preference. Nothing moves until there is real
// evidence.
func TestStandingNeedsEvidence(t *testing.T) {
	now := time.Now()
	l := Ledger{}
	for i := 1; i <= attentionFloor; i++ {
		shown(l, "hackclub/dns", 1, now)
		if got := l.Standing("hackclub/dns"); got != 1 {
			t.Fatalf("after %d showings standing = %v, want 1", i, got)
		}
	}
	shown(l, "hackclub/dns", 1, now)
	if got := l.Standing("hackclub/dns"); got >= 1 {
		t.Errorf("past the floor standing = %v, want it sinking", got)
	}
}

func TestStandingSinksWithTheStreak(t *testing.T) {
	now := time.Now()
	l := Ledger{}
	shown(l, "hackclub/dns", 26, now)

	got := l.Standing("hackclub/dns")
	if got > 0.4 || got <= 0 {
		t.Errorf("standing after 26 ignores = %v, want well under half", got)
	}

	// One act and it is back to full: whatever was being ignored, not this.
	l.Acted("hackclub/dns", now)
	if got := l.Standing("hackclub/dns"); got != 1 {
		t.Errorf("standing after acting = %v, want 1", got)
	}
}

// A place nobody ever touches stops being gathered, and says so.
func TestPlacesFadeAndComeBack(t *testing.T) {
	now := time.Now()
	l := Ledger{}
	shown(l, "hackclub/dns", attentionFade-1, now)
	if len(l.Faded()) != 0 {
		t.Fatal("faded before the threshold")
	}

	shown(l, "hackclub/dns", 1, now)
	if faded := l.Faded(); len(faded) != 1 || faded[0] != "hackclub/dns" {
		t.Fatalf("faded = %v, want hackclub/dns", faded)
	}

	results := []*SourceResult{{Items: []Item{
		{Origin: "hackclub/dns", Title: "Add record for x"},
		{Origin: "hackclub/orchard", Title: "a real PR"},
	}}}
	FadedOut(results, l)
	if len(results[0].Items) != 1 || results[0].Items[0].Origin != "hackclub/orchard" {
		t.Errorf("FadedOut kept %+v", results[0].Items)
	}

	l.Revive("hackclub/dns")
	if len(l.Faded()) != 0 {
		t.Error("reviving did not bring it back")
	}
	if got := l.Standing("hackclub/dns"); got != 1 {
		t.Errorf("revived standing = %v, want a clean slate", got)
	}
}

// A busy repo and a quiet one that are equally ignored must fade at the same
// rate: one morning of not caring is one morning, whatever the volume.
func TestOneShowingPerMorningWhateverTheVolume(t *testing.T) {
	now := time.Now()
	l := Ledger{}
	l.Saw([]Item{
		{Origin: "hackclub/dns"}, {Origin: "hackclub/dns"}, {Origin: "hackclub/dns"},
		{Origin: "#hq-hq"},
	}, now)

	if l["hackclub/dns"].Shown != 1 {
		t.Errorf("a busy repo counted %d showings, want 1", l["hackclub/dns"].Shown)
	}
	if l["hq-hq"].Shown != 1 {
		t.Errorf("a quiet channel counted %d showings, want 1", l["hq-hq"].Shown)
	}
}

// Saying an item does not belong is worth more than a morning of silence.
func TestRejectionCountsForSeveralMornings(t *testing.T) {
	now := time.Now()
	quiet, loud := Ledger{}, Ledger{}
	shown(quiet, "#noise", 2, now)
	shown(loud, "#noise", 2, now)
	loud.Rejected("#noise", now)

	if loud.Standing("#noise") >= quiet.Standing("#noise") {
		t.Errorf("a rejection did not sink it faster: %v vs %v",
			loud.Standing("#noise"), quiet.Standing("#noise"))
	}
}

func TestLedgerIgnoresItemsWithNoOrigin(t *testing.T) {
	l := Ledger{}
	l.Saw([]Item{{Title: "no origin"}}, time.Now())
	if len(l) != 0 {
		t.Errorf("ledger grew an empty key: %+v", l)
	}
	if got := l.Standing(""); got != 1 {
		t.Errorf("Standing(\"\") = %v, want 1", got)
	}
	l.Acted("", time.Now()) // must not panic
}

func TestOriginFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/hackclub/orchard/pull/28": "hackclub/orchard",
		"https://github.com/hackclub/dns/pulls":       "hackclub/dns",
		"https://figma.com/design/x":                  "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := originFromURL(in); got != want {
			t.Errorf("originFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
