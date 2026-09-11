package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Rules: the things that are true without a model.
//
// Two of the three items Dia got right that Pomona missed were not judgement
// calls. A question in a room the reader owns, with no human reply after a
// morning, is unanswered; that is arithmetic. A version tagged in a repository
// they own that no message in the matching room mentions has not been
// announced; that is a string search. Both are cheaper than a token and
// righter than a guess, so they run first and the model gets their output.

// ── Unanswered questions ────────────────────────────────

var questionStarts = []string{
	"who ", "what ", "where ", "when ", "why ", "how ", "is ", "are ", "can ", "could ",
	"does ", "do ", "did ", "any ", "anyone ", "anybody ", "should ", "would ", "will ", "has ", "have ",
}

// questionLike says whether a message is asking something. Ends with a
// question mark, or opens the way questions open. "?" alone or "??" is not a
// question, it is a reaction.
func questionLike(text string) bool {
	t := strings.TrimSpace(strings.ToLower(text))
	if len(t) < 8 {
		return false
	}
	if strings.HasSuffix(t, "?") {
		return true
	}
	for _, start := range questionStarts {
		if strings.HasPrefix(t, start) {
			return true
		}
	}
	return false
}

const (
	unansweredAfter  = 30 * time.Minute // younger than this, someone may still be typing
	unansweredWithin = 48 * time.Hour   // older than this, it is not a question any more, it is history
)

// unanswered decides whether a root message in an owned room is a question
// nobody has answered. repliers is who replied; isBot says which of them are
// apps rather than people.
func unanswered(text string, at, now time.Time, replyCount int, repliers []string, isBot func(string) bool, me string) bool {
	if !questionLike(text) {
		return false
	}
	age := now.Sub(at)
	if age < unansweredAfter || age > unansweredWithin {
		return false
	}
	if replyCount == 0 {
		return true
	}
	for _, id := range repliers {
		if id == me || !isBot(id) {
			return false // a person answered
		}
	}
	return true // only apps replied, which is nobody
}

// ── Shipped but not announced ───────────────────────────

var versionString = regexp.MustCompile(`\bv?(\d+\.\d+(?:\.\d+)?)\b`)

// versionsIn lists the version numbers a piece of text mentions.
func versionsIn(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range versionString.FindAllStringSubmatch(text, -1) {
		if v := m[1]; !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// Release is a tag or release the reader shipped in a repository they own.
type Release struct {
	Repo string
	Tag  string
	Name string
	URL  string
	At   time.Time
}

// unannounced finds releases in owned repositories whose version appears in
// no message from the matching owned rooms. own decides which rooms match a
// repository; said is every line of Slack the store holds for a room.
func unannounced(releases []Release, roomsFor func(repo string) []string, said func(room string) string, now time.Time) []Item {
	out := []Item{}
	byRepo := map[string][]Release{}
	for _, r := range releases {
		if now.Sub(r.At) <= 72*time.Hour {
			byRepo[r.Repo] = append(byRepo[r.Repo], r)
		}
	}
	for repo, rels := range byRepo {
		rooms := roomsFor(repo)
		if len(rooms) == 0 {
			continue // nowhere it would have been announced, so nothing to say
		}
		heard := strings.ToLower(strings.Join(mapRooms(rooms, said), " "))
		quiet := []Release{}
		for _, r := range rels {
			mentioned := false
			for _, v := range versionsIn(r.Tag + " " + r.Name) {
				if mentionedIn(heard, v) {
					mentioned = true
					break
				}
			}
			if !mentioned {
				quiet = append(quiet, r)
			}
		}
		if len(quiet) == 0 {
			continue
		}
		sort.Slice(quiet, func(i, j int) bool { return quiet[i].At.Before(quiet[j].At) })
		tags := []string{}
		for _, r := range quiet {
			tags = append(tags, r.Tag)
		}
		span := tags[0]
		if len(tags) > 1 {
			span = tags[0] + " through " + tags[len(tags)-1]
		}
		out = append(out, Item{
			Kind:  "github.shipped",
			Title: fmt.Sprintf("%s — %s shipped, and %s has not heard", repo, span, joinRooms(rooms)),
			Body: fmt.Sprintf("You released %s in %s in the last three days. Nothing in %s mentions %s. "+
				"People who use it may not know it exists.", span, repo, joinRooms(rooms), pluralVersions(len(tags))),
			URL:    quiet[len(quiet)-1].URL,
			Time:   quiet[len(quiet)-1].At,
			Tags:   []string{"shipped", "unannounced"},
			Origin: repo,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// mentionedIn says whether a room has spoken of a version. People drop a
// trailing .0, so v2.27.0 counts as mentioned when someone said "2.27".
func mentionedIn(heard, version string) bool {
	if containsVersion(heard, version) {
		return true
	}
	if strings.HasSuffix(version, ".0") {
		return containsVersion(heard, strings.TrimSuffix(version, ".0"))
	}
	return false
}

// containsVersion looks for the version as a whole number, so "2.2" does not
// match inside "2.28".
func containsVersion(heard, version string) bool {
	for _, v := range versionsIn(heard) {
		if v == version {
			return true
		}
	}
	return false
}

func mapRooms(rooms []string, said func(string) string) []string {
	out := make([]string, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, said(r))
	}
	return out
}

func joinRooms(rooms []string) string {
	switch len(rooms) {
	case 1:
		return displayOrigin(rooms[0])
	case 2:
		return displayOrigin(rooms[0]) + " or " + displayOrigin(rooms[1])
	}
	return displayOrigin(rooms[0]) + " and " + fmt.Sprintf("%d other rooms", len(rooms)-1)
}

func pluralVersions(n int) string {
	if n == 1 {
		return "it"
	}
	return "any of them"
}

// roomsForRepo picks the owned rooms that share a name with a repository:
// #orchard-support for hackclub/orchard.
func roomsForRepo(own Ownership, repo string) []string {
	rooms := []string{}
	for place, o := range own {
		if o.Kind == "channel" && (o.Pinned || o.Score >= ownedThreshold) && shareAWord("#"+place, repo) {
			rooms = append(rooms, place)
		}
	}
	sort.Strings(rooms)
	return rooms
}

// roomSaid is everything the store holds that was said in a room, as one
// string, for the version search.
func roomSaid(sig *SignalStore, room string) string {
	room = normaliseOrigin(room)
	var b strings.Builder
	for _, it := range sig.Items {
		if normaliseOrigin(it.Origin) != room {
			continue
		}
		b.WriteString(it.Title)
		b.WriteByte(' ')
		b.WriteString(it.Body)
		b.WriteByte(' ')
		for _, l := range it.Lines {
			b.WriteString(l.Text)
			b.WriteByte(' ')
		}
	}
	return b.String()
}
