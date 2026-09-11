package main

import (
	"sort"
	"strings"
	"time"
)

// What the reader keeps ignoring.
//
// Muting is the explicit answer and it works, but it asks somebody to notice
// their own boredom and act on it. Most people never will: they just skim past
// hackclub/dns every morning for a month. So the brief watches instead. A place
// whose items are shown over and over and never once opened, ticked or acted on
// sinks, and eventually stops being gathered at all.
//
// Two rules keep this honest. It needs real evidence before it does anything,
// because three quiet mornings is not a preference. And anything it fades is
// listed where the reader can see it and bring it back, because a system that
// silently decides what you do not care about, with no way to look at the
// decision, is worse than one that shows you too much.

const (
	// Below this many showings there is not enough evidence to judge a place.
	attentionFloor = 6
	// Consecutive ignores at which a place stops being gathered entirely.
	attentionFade = 30
	// How fast a place sinks once it is past the floor. At this many ignores
	// its items are worth half what they were.
	attentionHalfLife = 10.0
)

// Attention is one origin's record: how often it was put in front of the
// reader, and how often they did anything about it.
type Attention struct {
	Shown   int       `json:"shown"`
	Acted   int       `json:"acted"`
	Ignored int       `json:"ignored"` // consecutive showings with no response
	LastAct time.Time `json:"lastAct,omitempty"`
	Faded   time.Time `json:"faded,omitempty"` // when it stopped being gathered
}

// Ledger is every origin the reader has been shown, keyed by origin.
type Ledger map[string]*Attention

func (l Ledger) at(origin string) *Attention {
	key := normaliseOrigin(origin)
	if key == "" {
		return nil
	}
	if l[key] == nil {
		l[key] = &Attention{}
	}
	return l[key]
}

// Saw records that items from these origins reached a brief. One entry per
// origin per morning: a place that produced nine items has been shown once, or
// a busy repository would fade nine times faster than a quiet one for exactly
// the same amount of not caring.
func (l Ledger) Saw(items []Item, now time.Time) {
	seen := map[string]bool{}
	for _, it := range items {
		key := normaliseOrigin(it.Origin)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true

		a := l.at(key)
		a.Shown++
		a.Ignored++
		if a.Ignored >= attentionFade && a.Faded.IsZero() {
			a.Faded = now
		}
	}
}

// Acted records that the reader did something with an item from this origin:
// ticked it off, or opened it. The streak resets, because whatever they were
// ignoring, it was not this.
func (l Ledger) Acted(origin string, now time.Time) {
	a := l.at(origin)
	if a == nil {
		return
	}
	a.Acted++
	a.Ignored = 0
	a.LastAct = now
	a.Faded = time.Time{} // interest brings a place back
}

// Rejected records that the reader said an item from here did not belong. That
// is worth more than a morning of silence, so it counts for several.
func (l Ledger) Rejected(origin string, now time.Time) {
	a := l.at(origin)
	if a == nil {
		return
	}
	a.Ignored += attentionFloor
	if a.Ignored >= attentionFade && a.Faded.IsZero() {
		a.Faded = now
	}
}

// Standing is how much an origin's items are still worth, between 0 and 1.
// Everything is worth full weight until there is enough evidence to say
// otherwise.
func (l Ledger) Standing(origin string) float64 {
	a := l[normaliseOrigin(origin)]
	// Only the streak is checked, not the number of showings. The floor is
	// there to stop the brief judging a place on a few quiet mornings, but
	// somebody saying out loud that an item did not belong is not a quiet
	// morning: it is the clearest evidence there is, and it should not have to
	// wait its turn behind five more.
	if a == nil || a.Ignored < attentionFloor {
		return 1
	}
	over := float64(a.Ignored - attentionFloor)
	return 1 / (1 + over/attentionHalfLife)
}

// Faded lists the origins that have stopped being gathered, most recent first,
// so the reader can see what went quiet and undo it.
func (l Ledger) Faded() []string {
	out := []string{}
	for origin, a := range l {
		if !a.Faded.IsZero() {
			out = append(out, origin)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return l[out[i]].Faded.After(l[out[j]].Faded)
	})
	return out
}

// Revive puts a faded place back in circulation with a clean slate.
func (l Ledger) Revive(origin string) {
	if a := l[normaliseOrigin(origin)]; a != nil {
		a.Faded = time.Time{}
		a.Ignored = 0
	}
}

// FadedOut drops everything from a place that faded on its own. It runs
// alongside Silence, before anything is ranked.
func FadedOut(results []*SourceResult, ledger Ledger) {
	gone := map[string]bool{}
	for _, origin := range ledger.Faded() {
		gone[origin] = true
	}
	if len(gone) == 0 {
		return
	}
	for _, r := range results {
		kept := r.Items[:0]
		for _, it := range r.Items {
			if !gone[normaliseOrigin(it.Origin)] {
				kept = append(kept, it)
			}
		}
		r.Items = kept
	}
}

// originFromURL works out which place a link belongs to, so a to-do the model
// wrote can be traced back to the room it came from without the model having
// to say. Slack permalinks carry a channel id rather than a name, so they are
// matched against the items instead; this handles the forge links.
func originFromURL(link string) string {
	if m := repoPath.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	if strings.Contains(link, "github.com/") {
		trimmed := strings.TrimPrefix(strings.SplitN(link, "github.com/", 2)[1], "/")
		parts := strings.Split(trimmed, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}
