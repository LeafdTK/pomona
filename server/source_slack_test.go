package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A Slack that answers from a script and remembers what it was asked. The
// pacing clock is switched off for the duration, or every test would wait
// 1.2s per history call.
type fakeSlack struct {
	mu       sync.Mutex
	calls    []string                  // "method?query" in the order they arrived
	channels []slackChannel            // answered to a public/private listing
	dms      []slackChannel            // answered to an im/mpim listing
	history  map[string][]slackMessage // by channel id
	search   map[string][]any          // by query prefix ("from:@", "@")
	replies  map[string][]slackMessage // by thread ts, root first
	scopes   string
}

func newFakeSlack(t *testing.T) (*fakeSlack, func()) {
	t.Helper()
	f := &fakeSlack{
		history: map[string][]slackMessage{},
		search:  map[string][]any{},
		replies: map[string][]slackMessage{},
		scopes:  "search:read,channels:read,channels:history,groups:read,groups:history,im:read,im:history,mpim:read,mpim:history,users:read",
	}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	oldBase, oldPace := slackBase, paced
	slackBase = server.URL + "/"
	paced = map[string]time.Duration{}
	return f, func() {
		slackBase, paced = oldBase, oldPace
		server.Close()
	}
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	f.calls = append(f.calls, method+"?"+r.URL.RawQuery)
	f.mu.Unlock()

	w.Header().Set("X-OAuth-Scopes", f.scopes)
	reply := func(v any) {
		_ = json.NewEncoder(w).Encode(v)
	}

	switch method {
	case "auth.test":
		reply(map[string]any{"ok": true, "user_id": "U_ME", "user": "me", "url": "https://t.slack.com/"})
	case "users.conversations":
		// Slack answers the types it was asked for. Answering everything to
		// every listing made the DM pass read rooms as conversations.
		list := f.channels
		if strings.Contains(r.URL.Query().Get("types"), "im") {
			list = f.dms
		}
		reply(map[string]any{"ok": true, "channels": list, "response_metadata": map[string]string{"next_cursor": ""}})
	case "conversations.history":
		reply(map[string]any{"ok": true, "messages": f.history[r.URL.Query().Get("channel")]})
	case "conversations.replies":
		msgs := f.replies[r.URL.Query().Get("ts")]
		if msgs == nil {
			msgs = []slackMessage{}
		}
		reply(map[string]any{"ok": true, "messages": msgs})
	case "search.messages":
		q := r.URL.Query().Get("query")
		matches := []any{}
		for prefix, m := range f.search {
			if strings.HasPrefix(q, prefix) {
				matches = m
			}
		}
		reply(map[string]any{"ok": true, "messages": map[string]any{"matches": matches}})
	case "users.info":
		reply(map[string]any{"ok": true, "user": map[string]any{"name": "someone", "profile": map[string]string{"display_name": "someone"}}})
	default:
		reply(map[string]any{"ok": false, "error": "unknown_method"})
	}
}

func (f *fakeSlack) historyCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "conversations.history?") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeSlack) historyChannelsInOrder() []string {
	out := []string{}
	for _, c := range f.historyCalls() {
		for _, part := range strings.Split(strings.TrimPrefix(c, "conversations.history?"), "&") {
			if strings.HasPrefix(part, "channel=") {
				out = append(out, strings.TrimPrefix(part, "channel="))
			}
		}
	}
	return out
}

func channel(id, name string, updated int64) slackChannel {
	return slackChannel{ID: id, Name: name, Updated: updated}
}

func msg(user, ts, text string) slackMessage {
	return slackMessage{Type: "message", User: user, TS: ts, Text: text}
}

// ts is a Slack timestamp a little before now, so recency checks pass.
func ts(minutesAgo int) string {
	return fmt.Sprintf("%d.000100", time.Now().Add(-time.Duration(minutesAgo)*time.Minute).Unix())
}

func slackWindow(sig *SignalStore, now time.Time) Window {
	// Pacing is off, so the budget only has to clear threadReserve.
	w := Window{Now: now, Since: now.Add(-24 * time.Hour), Budget: 10 * time.Minute}
	if sig != nil {
		w.Signals = sig
		w.Cursors = sig.CursorSet()
	}
	return w
}

// A channel read yesterday is asked today only for what came after.
func TestSlackHistoryPassesOldestCursor(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	f.channels = []slackChannel{channel("C1", "orchard", 100)}
	newest := ts(5)
	f.history["C1"] = []slackMessage{msg("U2", newest, "hello there everyone")}

	sig := newSignalStore()
	older := ts(60)
	sig.Cursors["slack:C1"] = &Cursor{TS: older}
	now := time.Now()

	_, _, err := fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, slackWindow(sig, now))
	if err != nil {
		t.Fatal(err)
	}

	calls := f.historyCalls()
	if len(calls) != 1 {
		t.Fatalf("history calls = %v, want one", calls)
	}
	if !strings.Contains(calls[0], "oldest="+older) {
		t.Errorf("did not read from the cursor: %s", calls[0])
	}
	if got := sig.Cursors["slack:C1"].TS; got != newest {
		t.Errorf("cursor did not advance to the newest message: %q", got)
	}
}

// Without a cursor, the read starts at the window, never earlier.
func TestSlackHistoryStartsAtTheWindowWithoutACursor(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	f.channels = []slackChannel{channel("C1", "orchard", 100)}
	sig := newSignalStore()
	now := time.Now()
	w := slackWindow(sig, now)

	_, _, _ = fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, w)
	calls := f.historyCalls()
	if len(calls) != 1 || !strings.Contains(calls[0], fmt.Sprintf("oldest=%d", w.Since.Unix())) {
		t.Errorf("history calls = %v, want oldest at the window start", calls)
	}
}

// A muted room costs nothing: the check happens before the call.
func TestSkipPreventsPacedCall(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	f.channels = []slackChannel{channel("C1", "orchard", 100), channel("C2", "money-laundering", 200)}
	w := slackWindow(newSignalStore(), time.Now())
	w.Skip = func(origin string) bool { return origin == "#money-laundering" }

	_, _, _ = fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, w)
	for _, c := range f.historyCalls() {
		if strings.Contains(c, "channel=C2") {
			t.Fatalf("a muted room was still read: %s", c)
		}
	}
	if got := f.historyChannelsInOrder(); len(got) != 1 || got[0] != "C1" {
		t.Errorf("history channels = %v, want just C1", got)
	}
}

// Rooms are read in the order they deserve: where you spoke, then what you
// own, then the rest. Slack's "updated" field, which used to decide this,
// is when metadata changed and says nothing about messages.
func TestChannelOrderSpokeThenOwnedThenRest(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	// By Slack's updated field the order would be busy, owned, mine.
	f.channels = []slackChannel{
		channel("C_BUSY", "random", 900),
		channel("C_OWNED", "orchard-support", 500),
		channel("C_MINE", "new-project", 100),
	}
	// The from:@me search says the reader spoke in new-project yesterday.
	f.search["from:@"] = []any{map[string]any{
		"text": "starting this", "user": "U_ME", "ts": ts(30),
		"channel": map[string]string{"id": "C_MINE", "name": "new-project"},
	}}

	w := slackWindow(newSignalStore(), time.Now())
	w.Owned = func(origin string) bool { return origin == "#orchard-support" }

	_, _, _ = fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, w)
	got := f.historyChannelsInOrder()
	want := []string{"C_MINE", "C_OWNED", "C_BUSY"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("read order = %v, want %v", got, want)
	}

	// And the search left ownership evidence behind.
	if m := w.Signals.Channels["C_MINE"]; m == nil || m.MyPosts != 1 || m.LastSpoke.IsZero() {
		t.Errorf("new-project was not recorded as spoken in: %+v", m)
	}
}

// A room the reader is in comes back as lines, so the store can append
// tomorrow's to today's.
func TestRoomItemsCarryLines(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	f.channels = []slackChannel{channel("C1", "orchard", 100)}
	second, first := ts(1), ts(2)
	f.history["C1"] = []slackMessage{
		msg("U2", second, "second thing"),
		msg("U_ME", first, "first thing from me"),
	}
	sig := newSignalStore()
	sig.NoteChannel("C1", "orchard", false, "").Spoke(true, time.Now()) // the reader is in this room

	items, _, _ := fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, slackWindow(sig, time.Now()))
	var room *Item
	for i := range items {
		if items[i].Kind == "slack.active_channel" {
			room = &items[i]
		}
	}
	if room == nil {
		t.Fatalf("no room item in %+v", items)
	}
	if len(room.Lines) != 2 || !room.Lines[0].Mine || room.Lines[0].TS != first {
		t.Errorf("lines = %+v, want two in time order with the reader's marked", room.Lines)
	}
}

// Being added to a room is a signal. The join used to count by accident,
// then got filtered as housekeeping, and the room went dark.
func TestAJoinMakesTheRoomYours(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	f.channels = []slackChannel{channel("C1", "reem-birthday-omgyay", 100)}
	f.history["C1"] = []slackMessage{
		msg("U2", ts(1), "here is the figma for the card"),
		msg("U3", ts(2), "yayyy"),
		{Type: "message", Subtype: "channel_join", User: "U_ME", TS: ts(3), Text: "<@U_ME> has joined the channel"},
	}
	items, _, _ := fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, slackWindow(newSignalStore(), time.Now()))
	var room *Item
	for i := range items {
		if items[i].Kind == "slack.active_channel" {
			room = &items[i]
		}
	}
	if room == nil {
		t.Fatalf("a room the reader was just added to produced nothing: %+v", items)
	}
	if !contains(room.Title, "just added") {
		t.Errorf("title = %q", room.Title)
	}
	if len(room.Lines) != 2 {
		t.Errorf("lines = %+v, want the two real messages and not the join", room.Lines)
	}
}

// A room the reader is merely in can still light up.
func TestABusyRoomSurfacesFromTheOutside(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	f.channels = []slackChannel{channel("C1", "hq-olympics", 100), channel("C2", "quiet", 100)}
	burst := []slackMessage{}
	for i := 0; i < 9; i++ {
		burst = append(burst, msg([]string{"U2", "U3", "U4"}[i%3], ts(20-i), "message number "+itoa(i)+" about the thing"))
	}
	f.history["C1"] = burst
	f.history["C2"] = []slackMessage{msg("U2", ts(1), "just one thing")}

	items, _, _ := fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, slackWindow(newSignalStore(), time.Now()))
	got := map[string]string{}
	for _, it := range items {
		if it.Kind == "slack.active_channel" {
			got[it.Origin] = it.Title
		}
	}
	if !contains(got["#hq-olympics"], "lit up") {
		t.Errorf("the busy room did not surface: %v", got)
	}
	if _, quiet := got["#quiet"]; quiet {
		t.Error("a quiet room the reader is not in was surfaced")
	}
}


// A mention is read with its thread to the end, and the item says so. The
// brief once wrote "the thread has been quiet since" about a thread it had
// never opened, which was three people arguing about kernel versions.
func TestAMentionCarriesItsThreadToTheEnd(t *testing.T) {
	f, done := newFakeSlack(t)
	defer done()
	now := time.Now()
	f.search["@"] = []any{
		map[string]any{
			"text": "<@U_ME> are the control planes on current kernels?", "user": "U_PARTH", "ts": ts(60),
			"channel":   map[string]string{"id": "C_INFRA", "name": "infra"},
			"permalink": "https://t.slack.com/archives/C_INFRA/p" + strings.ReplaceAll(ts(60), ".", ""),
		},
		map[string]any{
			"text": "<@U_ME> ping", "user": "U_OTHER", "ts": ts(50),
			"channel":   map[string]string{"id": "C_QUIET", "name": "quiet"},
			"permalink": "https://t.slack.com/archives/C_QUIET/p" + strings.ReplaceAll(ts(50), ".", ""),
		},
	}
	f.replies[ts(60)] = []slackMessage{
		{Type: "message", TS: ts(60), User: "U_PARTH", Text: "<@U_ME> are the control planes on current kernels?"},
		{Type: "message", TS: ts(55), User: "U_NORA", Text: "we are two point releases behind on the workers"},
		{Type: "message", TS: ts(40), User: "U_ROWAN", Text: "control planes were patched this morning"},
	}
	f.replies[ts(50)] = []slackMessage{
		{Type: "message", TS: ts(50), User: "U_OTHER", Text: "<@U_ME> ping"},
	}

	items, _, err := fetchSlack(t.Context(), map[string]string{"token": "x", "access": AccessAll}, slackWindow(newSignalStore(), now))
	if err != nil {
		t.Fatal(err)
	}
	byOrigin := map[string]Item{}
	for _, it := range items {
		if it.Kind == "slack.mention" {
			byOrigin[it.Origin] = it
		}
	}
	busy, ok := byOrigin["#infra"]
	if !ok {
		t.Fatalf("the infra mention was lost: %+v", items)
	}
	if !strings.Contains(busy.Body, "patched this morning") || !strings.Contains(busy.Body, "read to the end: 2 replies, 2 after this message") {
		t.Errorf("the thread's replies were not carried: %q", busy.Body)
	}
	if !hasTag(busy.Tags, "thread:read") || len(busy.Lines) != 2 {
		t.Errorf("the item does not say its thread was read: tags %v, %d lines", busy.Tags, len(busy.Lines))
	}
	quiet := byOrigin["#quiet"]
	if !strings.Contains(quiet.Body, "no replies") || !hasTag(quiet.Tags, "thread:read") {
		t.Errorf("a thread with nothing after the mention should say so: %q %v", quiet.Body, quiet.Tags)
	}
}
