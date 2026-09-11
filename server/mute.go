package main

import "strings"

// Muting a place, rather than an item.
//
// "Doesn't look right?" answers one to-do. This answers a room: #money-laundering
// is a real channel doing real things, none of which are Sebastian's, and no
// amount of judging each message individually gets that right. The cheapest
// signal is the one never gathered, so a mute is applied before the model sees
// anything: it costs nothing to run and cannot be overruled by a good sentence.

// Origin is where an item came from, in the form a person would mute:
// "#hq-hq" for a channel, "hackclub/orchard" for a repository.
func originOf(it Item) string { return it.Origin }

// muted reports whether an origin has been silenced. Matching is forgiving
// about the leading hash and about case, because the string may have come from
// a person typing it rather than from an item.
func muted(origin string, mutes []string) bool {
	origin = normaliseOrigin(origin)
	if origin == "" {
		return false
	}
	for _, m := range mutes {
		if m := normaliseOrigin(m); m != "" && m == origin {
			return true
		}
	}
	return false
}

func normaliseOrigin(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "#")
}

// Silence drops everything from a muted origin. It runs before ranking, so a
// muted room cannot crowd out anything on its way to being discarded.
func Silence(results []*SourceResult, mutes []string) {
	if len(mutes) == 0 {
		return
	}
	for _, r := range results {
		kept := r.Items[:0]
		for _, it := range r.Items {
			if !muted(originOf(it), mutes) {
				kept = append(kept, it)
			}
		}
		r.Items = kept
	}
}

// addMute returns the list with one more origin on it, or unchanged if it is
// already there. Muting the same room twice should not grow the setting.
func addMute(mutes []string, origin string) []string {
	origin = strings.TrimSpace(origin)
	if origin == "" || muted(origin, mutes) {
		return mutes
	}
	return append(mutes, origin)
}

func dropMute(mutes []string, origin string) []string {
	kept := []string{}
	for _, m := range mutes {
		if normaliseOrigin(m) != normaliseOrigin(origin) {
			kept = append(kept, m)
		}
	}
	return kept
}
