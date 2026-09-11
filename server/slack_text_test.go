package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A directory with no client can still render everything Slack labels inline,
// which is the common case: Slack usually writes <#C1|general>, not <#C1>.
func offline() *slackDirectory { return newSlackDirectory(&slackClient{granted: map[string]bool{}}) }

func TestRenderLabelledEntities(t *testing.T) {
	d := offline()
	cases := []struct{ in, want string }{
		{"<!channel> Happy Tuesday", "@channel Happy Tuesday"},
		{"<!here> anyone about?", "@here anyone about?"},
		{"<!everyone> ship it", "@everyone ship it"},
		{"<@U1|rebeka> can you look", "@rebeka can you look"},
		{"<#C1|hq-hq> is the place", "#hq-hq is the place"},
		{"<!subteam^S1|@design> thoughts?", "@design thoughts?"},
		{"see <https://x.test/a|the slides>", "see the slides (https://x.test/a)"},
		{"see <https://x.test/a>", "see https://x.test/a"},
		{"a &amp; b &lt;3", "a & b <3"},
		{"<!date^1699^{date_short}|Nov 5, 2023> works", "Nov 5, 2023 works"},
	}
	for _, c := range cases {
		if got := d.Render(c.in); got != c.want {
			t.Errorf("Render(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Unlabelled ids need the API. With no client they must degrade to something
// readable rather than leaking a raw id into the brief, which is the bug that
// put "U082GTRTR5X pinged your group" in front of the reader.
func TestRenderNeverLeaksRawIDs(t *testing.T) {
	d := offline()
	d.users["U1"] = ""    // asked, and Slack would not say
	d.channels["C1"] = "" // same
	d.groups["S1"] = ""

	for _, in := range []string{"<@U1> hi", "<#C1> hi", "<!subteam^S1> hi"} {
		got := d.Render(in)
		for _, id := range []string{"U1", "C1", "S1"} {
			if contains(got, id) {
				t.Errorf("Render(%q) = %q, which still shows the raw id %s", in, got, id)
			}
		}
	}
}

func TestRenderResolvesFromCache(t *testing.T) {
	d := offline()
	d.users["U082GTRTR5X"] = "Rebeka"
	d.channels["C08"] = "hq-hq"

	got := d.Render("<@U082GTRTR5X> posted in <#C08>")
	if want := "@Rebeka posted in #hq-hq"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMeaningful(t *testing.T) {
	yes := []string{
		"@channel Happy Tuesday, Team Sync Day! Please add to the slides",
		"can someone review this before standup",
	}
	no := []string{
		"", "@channel", "@here @design", "@group", "!", "@someone ?",
	}
	for _, s := range yes {
		if !meaningful(s) {
			t.Errorf("meaningful(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if meaningful(s) {
			t.Errorf("meaningful(%q) = true, want false", s)
		}
	}
}

// The messages that arrived as "no message attached": text is empty and the
// words are in blocks or attachments.
func TestMessageWordsFallsBackToBlocks(t *testing.T) {
	blocks := json.RawMessage(`[{
	  "type": "rich_text",
	  "elements": [{
	    "type": "rich_text_section",
	    "elements": [
	      {"type": "broadcast", "range": "channel"},
	      {"type": "text", "text": " Happy Tuesday, "},
	      {"type": "user", "user_id": "U1"},
	      {"type": "text", "text": " has the deck: "},
	      {"type": "link", "url": "https://x.test/deck", "text": "slides"}
	    ]
	  }]
	}]`)

	words := messageWords("", blocks, nil)
	for _, want := range []string{"<!channel>", "Happy Tuesday", "<@U1>", "slides", "https://x.test/deck"} {
		if !contains(words, want) {
			t.Errorf("messageWords lost %q, got %q", want, words)
		}
	}

	// And once rendered it has to read as prose, not as wire format.
	d := offline()
	d.users["U1"] = "Rebeka"
	got := d.Render(words)
	for _, want := range []string{"@channel", "@Rebeka", "slides"} {
		if !contains(got, want) {
			t.Errorf("Render lost %q, got %q", want, got)
		}
	}
	if !meaningful(got) {
		t.Errorf("a real announcement was judged content-free: %q", got)
	}
}

func TestMessageWordsFallsBackToAttachments(t *testing.T) {
	attachments := json.RawMessage(`[{"fallback":"ignored","title":"Build failed","text":"3 tests broke on main"}]`)
	got := messageWords("", nil, attachments)
	if !contains(got, "Build failed") || !contains(got, "3 tests broke on main") {
		t.Errorf("attachment text lost, got %q", got)
	}
}

func TestMessageWordsPrefersText(t *testing.T) {
	got := messageWords("the real text", json.RawMessage(`[{"type":"section","text":{"type":"mrkdwn","text":"fallback"}}]`), nil)
	if got != "the real text" {
		t.Errorf("got %q, want the plain text", got)
	}
}

func TestMessageWordsSurvivesJunk(t *testing.T) {
	for _, raw := range []string{``, `null`, `"a string"`, `{`, `[1,2,3]`} {
		if got := messageWords("", json.RawMessage(raw), nil); got != "" && raw != `"a string"` {
			t.Errorf("messageWords(%q) = %q, want empty", raw, got)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestHousekeepingIsNotNews(t *testing.T) {
	real := slackMessage{Type: "message", Text: "can someone look at this"}
	if !real.said() {
		t.Error("a plain message was treated as housekeeping")
	}
	for _, subtype := range []string{"channel_join", "channel_leave", "channel_topic", "pinned_item"} {
		m := slackMessage{Type: "message", Subtype: subtype, Text: "<@U1> has joined the channel"}
		if m.said() {
			t.Errorf("%s was treated as something a person said", subtype)
		}
	}
	if (slackMessage{Type: "channel_join"}).said() {
		t.Error("a non-message type was treated as a message")
	}
}

func TestThreadOwnership(t *testing.T) {
	me := "U_ME"
	cases := []struct {
		name string
		m    slackMessage
		want bool
	}{
		{"you started it", slackMessage{User: me}, true},
		{"you replied", slackMessage{User: "U1", ReplyUsers: []string{"U2", me}}, true},
		{"you follow it", slackMessage{User: "U1", Subscribed: true}, true},
		{"nothing to do with you", slackMessage{User: "U1", ReplyUsers: []string{"U2", "U3"}}, false},
	}
	for _, c := range cases {
		if got := c.m.yours(me); got != c.want {
			t.Errorf("%s: yours() = %v, want %v", c.name, got, c.want)
		}
	}
}

// The bug this exists to prevent: a 33 reply thread clipped from the front,
// so the brief reported an open question that had been answered fifteen
// replies further down.
func TestTailKeepsTheEndOfAThread(t *testing.T) {
	lines := []string{}
	for i := 0; i < 33; i++ {
		lines = append(lines, fmt.Sprintf("person%02d: message number %02d", i, i))
	}
	got := tail(lines, 300)

	if !contains(got, "message number 32") {
		t.Errorf("the last thing said was dropped: %q", got)
	}
	if contains(got, "message number 00") {
		t.Errorf("kept the start instead of the end: %q", got)
	}
	if !contains(got, "earlier replies") {
		t.Errorf("said nothing about what it left out: %q", got)
	}
	if len(got) > 400 {
		t.Errorf("ran way over budget: %d chars", len(got))
	}
}

func TestTailKeepsEverythingThatFits(t *testing.T) {
	lines := []string{"a: hello", "b: hi", "c: bye"}
	got := tail(lines, 500)
	if got != "a: hello | b: hi | c: bye" {
		t.Errorf("got %q, want the whole conversation untouched", got)
	}
	if contains(got, "earlier") {
		t.Error("claimed to have skipped something when it skipped nothing")
	}
}

func TestTailAlwaysKeepsSomething(t *testing.T) {
	// One reply longer than the entire budget still has to come through.
	long := "someone: " + strings.Repeat("x", 500)
	got := tail([]string{"a: first", long}, 50)
	if !contains(got, "xxxxx") {
		t.Errorf("dropped everything: %q", got)
	}
	if got == "" {
		t.Error("returned nothing at all")
	}
}

func TestTailOnNothing(t *testing.T) {
	if got := tail(nil, 100); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// Being in a thread covers everything from opening it to saying "same" once,
// and a brief that cannot tell those apart invents the difference.
func TestStandingSaysHowYouAreInIt(t *testing.T) {
	cases := []struct {
		started bool
		mine    int
		want    string
	}{
		{true, 0, "a thread you started"},
		{true, 4, "a thread you started"},
		{false, 1, "a thread you commented in once"},
		{false, 3, "a thread you commented in 3 times"},
		{false, 0, "a thread you follow but have not spoken in"},
	}
	for _, c := range cases {
		if got := standing(c.started, c.mine); got != c.want {
			t.Errorf("standing(%v, %d) = %q, want %q", c.started, c.mine, got, c.want)
		}
	}
}

// A room item points at the channel, not at a message, and there is no
// timestamp for it. Appending an empty one produced links ending in a bare
// "p" that opened nothing.
func TestMessageLinkWithoutATimestamp(t *testing.T) {
	cases := []struct{ name, ts, thread, want string }{
		{"whole channel", "", "", "https://hackclub.slack.com/archives/C1"},
		{"one message", "1788871724.841239", "", "https://hackclub.slack.com/archives/C1/p1788871724841239"},
		{"a reply in a thread", "1788871999.000100", "1788871724.841239",
			"https://hackclub.slack.com/archives/C1/p1788871999000100?thread_ts=1788871724.841239&cid=C1"},
	}
	for _, c := range cases {
		if got := messageLink("hackclub", "C1", c.ts, c.thread); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := messageLink("", "C1", "", ""); got != "" {
		t.Errorf("no workspace should mean no link, got %q", got)
	}
}
