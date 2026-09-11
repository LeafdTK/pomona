package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How much of Slack Pomona may look at.
//
// This reads channels directly rather than using Slack's search. Search sounds
// like the obvious tool and it is a trap: search.messages only accepts the
// broad `search:read` scope, which means everything you can see, and refuses
// the granular ones. Measured:
//
//	search.messages -> missing_scope  needed=search:read  provided=search:read.public
//
// Reading histories honours the narrow scopes properly, so "public channels
// only" is a promise Slack itself keeps rather than one this code makes.
const (
	AccessPublic  = "public"
	AccessDMs     = "public+dms"
	AccessPrivate = "public+private"
	AccessAll     = "all"
)

// ScopesFor is the narrowest set that can answer each tier.
//
// `search:read` is asked for on the widest tier only. It is all-or-nothing by
// design (Slack refuses the granular search scopes on search.messages), but it
// is the only way to find a mention across hundreds of channels in one call
// instead of polling each one.
func ScopesFor(access string) []string {
	scopes := []string{"users:read", "channels:read", "channels:history"}
	switch access {
	case AccessDMs:
		scopes = append(scopes, "im:read", "im:history", "mpim:read", "mpim:history")
	case AccessPrivate:
		scopes = append(scopes, "groups:read", "groups:history")
	case AccessAll:
		scopes = append(scopes, "im:read", "im:history", "mpim:read", "mpim:history",
			"groups:read", "groups:history", "search:read")
	}
	return scopes
}

// channelTypes is the tier expressed the way Slack names things, narrowed to
// what the token was actually granted.
//
// These can disagree: you pick a wider tier in the dropdown but install the app
// with fewer scopes, or widen the scopes and forget the dropdown. Asking for a
// type the token can't read fails the whole call, so the intersection wins and
// the brief still gets written from whatever is genuinely readable.
func channelTypes(access string, client *slackClient) string {
	wanted := map[string]bool{"public_channel": true}
	switch access {
	case AccessDMs:
		wanted["im"], wanted["mpim"] = true, true
	case AccessPrivate:
		wanted["private_channel"] = true
	case AccessAll:
		wanted["private_channel"], wanted["im"], wanted["mpim"] = true, true, true
	}

	needs := map[string]string{
		"public_channel":  "channels:read",
		"private_channel": "groups:read",
		"im":              "im:read",
		"mpim":            "mpim:read",
	}

	types := []string{}
	for _, kind := range []string{"public_channel", "private_channel", "im", "mpim"} {
		if wanted[kind] && (!client.knowsScopes() || client.has(needs[kind])) {
			types = append(types, kind)
		}
	}
	return strings.Join(types, ",")
}

// Reading every channel you're in would be slow and pointless, so this caps
// the work and spends it on the places that were active most recently.
const (
	// A brief is written once a morning and nobody is watching it happen, so
	// it reads every channel you're in rather than guessing which ones matter.
	// The thing you needed to know is reliably in the one that would have been
	// cut. Hundreds of calls is fine when you have a minute; they run a few at
	// a time to stay inside Slack's rate limit.
	maxChannels = 200
	perChannel  = 30
	maxThreads  = 25

	// Held back from the channel sweep so the threads it turns up can actually
	// be read.
	threadReserve = 90 * time.Second
	scanWorkers   = 6

	// How long the whole Slack harvest may take before it settles for what it
	// has. Long enough for a few hundred channels, short enough to still be a
	// morning brief.
	slackBudget = 8 * time.Minute
)

func slackCollector() Collector {
	return Collector{
		ID: "slack", Name: "Slack", IconKey: "provider:slack",
		Blurb: "Mentions and replies from the last day, so nothing important dies in a thread.",
		Help: "Add the scopes for your chosen tier under User Token Scopes, install the app, and " +
			"paste the User OAuth Token. Or press Connect if this server has a Slack app set up.",
		Fields: []Field{
			{
				Key: "workspace", Label: "Workspace", Type: "text", Placeholder: "hackclub",
				Help: "The bit before .slack.com, used for message links and to skip Slack's workspace picker.",
			},
			{
				Key: "access", Label: "What Pomona may read", Type: "select", Default: AccessPublic,
				Help: "Slack enforces this: the token is only issued the scopes for the tier you pick.",
				Options: []Option{
					{Value: AccessPublic, Label: "Public channels only"},
					{Value: AccessDMs, Label: "Public channels and DMs"},
					{Value: AccessPrivate, Label: "Public and private channels"},
					{Value: AccessAll, Label: "Everything I can see"},
				},
			},
			{Key: "token", Label: "User OAuth token", Type: "password", Placeholder: "xoxp-…",
				Help: "Only needed if you're not using Connect."},
		},
		Fetch: fetchSlack,
	}
}

// slackBase is where Slack lives. A test points it at a local server.
var slackBase = "https://slack.com/api/"

type slackClient struct {
	ctx   context.Context
	token string

	// What Slack says this token was actually granted, learned from the
	// response headers. Channels are read concurrently, so every touch of this
	// goes through the lock.
	mu      sync.RWMutex
	granted map[string]bool

	// How much of this morning was spent waiting for Slack, and how often it
	// said no. Without these a slow harvest is indistinguishable from a broken
	// one, which is a bad way to spend an hour.
	throttled int
	waited    time.Duration

	// When each metered method may next be called. Six workers sprinting into
	// the rate limit and then sleeping ten seconds apiece is strictly worse
	// than one steady queue that Slack never has to refuse.
	paceMu sync.Mutex
	nextAt map[string]time.Time
}

// Slack meters conversations.history and conversations.replies per method at
// around a call a second. Pacing just under that keeps a few hundred channels
// moving without ever tripping a penalty.
var paced = map[string]time.Duration{
	"conversations.history": 1200 * time.Millisecond,
	"conversations.replies": 1200 * time.Millisecond,
}

func (c *slackClient) pace(method string) error {
	every, metered := paced[method]
	if !metered {
		return nil
	}
	c.paceMu.Lock()
	wait := time.Until(c.nextAt[method])
	c.nextAt[method] = time.Now().Add(max(wait, 0) + every)
	c.paceMu.Unlock()

	if wait <= 0 {
		return nil
	}
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// budget reports whether there is still time to keep gathering. Slack now
// meters conversations.history hard enough that a few hundred channels can
// outlast the morning, so every pass checks before it asks for more.
func (c *slackClient) budget() bool { return c.budgetFor(5 * time.Second) }

// budgetFor reports whether at least this much time is left, so a pass can
// stop early and leave room for one that is worth more.
func (c *slackClient) budgetFor(d time.Duration) bool {
	deadline, ok := c.ctx.Deadline()
	return !ok || time.Until(deadline) > d
}

func (c *slackClient) note429(method string, wait time.Duration) {
	c.mu.Lock()
	c.throttled++
	c.waited += wait
	n := c.throttled
	c.mu.Unlock()
	if n <= 5 {
		log.Printf("slack: %s rate limited, Slack asked for %s", method, wait)
	}
}

func (c *slackClient) toll() (int, time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.throttled, c.waited
}

func (c *slackClient) noteScopes(raw string) {
	if raw == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, scope := range strings.Split(raw, ",") {
		c.granted[strings.TrimSpace(scope)] = true
	}
}

// has reports whether the token carries a scope. Before the first response
// lands nothing is known, and callers treat that as "try it and see".
func (c *slackClient) has(scope string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.granted[scope]
}

func (c *slackClient) knowsScopes() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.granted) > 0
}

func (c *slackClient) call(method string, params url.Values, out any) error {
	if err := c.pace(method); err != nil {
		return err
	}
	endpoint := slackBase + method
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := newRequest(c.ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}

	// Reading a few hundred channels will hit the rate limit. Slack says how
	// long to wait; waiting is better than losing the channel that mattered.
	if res.StatusCode == 429 {
		wait := 2 * time.Second
		if after, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && after > 0 {
			wait = time.Duration(after) * time.Second
		}
		res.Body.Close()
		c.note429(method, wait)

		// Waiting is right when Slack wants a moment and wrong when it wants a
		// minute per call: for a few hundred channels that is a brief nobody
		// reads because it arrives at lunchtime. Give up on this call instead
		// and let the pass move on.
		if deadline, ok := c.ctx.Deadline(); ok && time.Now().Add(wait).After(deadline) {
			return fmt.Errorf("%s: rate limited, and waiting %s would outlast the brief", method, wait)
		}
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(wait):
		}
		return c.call(method, params, out)
	}

	defer res.Body.Close()
	body, _ := readAll(res.Body)

	// Slack reports the token's real scopes on every response. The tier in
	// settings is only an intention; this is the fact.
	c.noteScopes(res.Header.Get("X-OAuth-Scopes"))

	// Slack answers 200 with {ok:false}. Say what it actually objected to,
	// including which scope it wanted: that one line is the difference between
	// a five minute fix and an evening.
	var status struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Needed   string `json:"needed"`
		Provided string `json:"provided"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("%s: %s", method, clip(string(body), 120))
	}
	if !status.OK {
		if status.Needed != "" {
			return fmt.Errorf("%s: %s (Slack wants %s, this token has %s)", method, status.Error, status.Needed, status.Provided)
		}
		return fmt.Errorf("%s: %s", method, status.Error)
	}
	return json.Unmarshal(body, out)
}

type slackChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsIM     bool   `json:"is_im"`
	IsMPIM   bool   `json:"is_mpim"`
	IsPvt    bool   `json:"is_private"`
	Updated  int64  `json:"updated"`
	User     string `json:"user"`
	Creator  string `json:"creator"`
	Archived bool   `json:"is_archived"`
}

func fetchSlack(ctx context.Context, settings map[string]string, w Window) ([]Item, []Event, error) {
	token := settings["token"]
	if token == "" {
		return nil, nil, fmt.Errorf("not connected")
	}
	access := settings["access"]
	if access == "" {
		access = AccessPublic
	}
	// A brief is allowed to be slow, but not unbounded. Slack meters
	// conversations.history per app, and on a workspace with hundreds of
	// channels an honest retry loop will happily wait until the afternoon.
	budget := w.Budget
	if budget == 0 {
		budget = slackBudget
	}
	ctx, done := context.WithTimeout(ctx, budget)
	defer done()
	client := &slackClient{ctx: ctx, token: token, granted: map[string]bool{}, nextAt: map[string]time.Time{}}

	var me struct {
		UserID string `json:"user_id"`
		User   string `json:"user"`
		URL    string `json:"url"`
	}
	if err := client.call("auth.test", nil, &me); err != nil {
		return nil, nil, err
	}

	domain := slackDomain(settings["workspace"])
	if domain == "" {
		domain = strings.TrimSuffix(strings.TrimPrefix(me.URL, "https://"), ".slack.com/")
	}
	dir := newSlackDirectory(client)

	// What actually matters in Slack overnight, in order:
	//
	//   1. Somebody wrote to you directly.
	//   2. Somebody replied in a thread you're part of, which is where things
	//      quietly die.
	//   3. Somebody said your name in a channel.
	//   4. Somebody pinged the room you're in: @here, @channel, or a group
	//      you belong to. Search can't find those, so channels are swept for
	//      them separately.
	//   5. Something moved in a conversation you're part of. Not addressed to
	//      you, not a ping, but you spoke there recently and it carried on
	//      without you. Most of what you actually need to know arrives this
	//      way, so it's gathered too and the model decides what matters.
	//
	// Each is gathered differently, and each is skipped cleanly if this token
	// wasn't granted the scopes for it.
	found := &slackHarvest{
		seen: map[string]bool{}, dir: dir, domain: domain, me: me.UserID,
		sig: w.Signals, cursors: w.Cursors, spoke: map[string]bool{},
	}

	// With search, two calls do most of the work: every mention of you, and
	// every room you spoke in yesterday. The second is what lets a project
	// you started this week show up tomorrow rather than never.
	if found.sig != nil {
		found.sig.Me, found.sig.me = me.UserID, me.UserID
	}

	if client.has("search:read") {
		found.mentions(client, me.User, w)
		found.threads(client, me.User, w)
	}
	found.direct(client, access, w)

	// Sweep channels either way: with search this is only looking for
	// broadcasts, which search doesn't index; without it, for your name too.
	found.scanChannels(client, access, w, client.has("search:read"))
	if found.sig != nil {
		found.sig.Decay(w.Now)
	}

	items := newestFirst(found.items, 35)
	if hits, waited := client.toll(); hits > 0 {
		log.Printf("slack: %d items in %s, rate limited %d times (%s waiting)",
			len(items), time.Since(w.Now).Round(time.Second), hits, waited.Round(time.Second))
	}
	if hits, waited := client.toll(); hits > 0 && len(items) == 0 {
		// Nothing to show and a wall of 429s is not a quiet morning, it is a
		// throttled one, and the difference matters to whoever has to fix it.
		return nil, nil, fmt.Errorf("Slack rate limited this scan %d times (%s spent waiting) and nothing got through", hits, waited.Round(time.Second))
	}
	return items, nil, nil
}

// slackHarvest collects items from several passes without repeating itself: a
// thread reply that also mentions you should appear once.
type slackHarvest struct {
	mu     sync.Mutex
	items  []Item
	seen   map[string]bool
	dir    *slackDirectory
	domain string
	me     string

	// What is kept between mornings. sig may be nil on the source-test path.
	sig     *SignalStore
	cursors *CursorSet
	// Rooms the reader posted in recently, learned from search. This is the
	// new-project detector: a channel you spoke in yesterday is read first
	// today, whatever else is true of it.
	spoke map[string]bool
}

// meta is the room's record, or a throwaway one when nothing is kept.
func (h *slackHarvest) meta(id, name string, private bool, creator string) *ChannelMeta {
	if h.sig == nil {
		return &ChannelMeta{ID: id, Name: name}
	}
	return h.sig.NoteChannel(id, name, private, creator)
}

// joinedRecently says whether the reader was added to a room in the last
// couple of weeks, which the store remembers from the join it saw.
func (h *slackHarvest) joinedRecently(id string, now time.Time) bool {
	if h.sig == nil {
		return false
	}
	m := h.sig.Channels[id]
	return m != nil && !m.Joined.IsZero() && now.Sub(m.Joined) < 14*24*time.Hour
}

// countSaid is how many real messages are in a read.
func countSaid(messages []slackMessage) int {
	n := 0
	for _, m := range messages {
		if m.said() {
			n++
		}
	}
	return n
}

// spokeRecently says whether the reader has posted in a room lately, from
// either this morning's search or what the store remembers.
func (h *slackHarvest) spokeRecently(id string, now time.Time) bool {
	if h.spoke[id] {
		return true
	}
	if h.sig != nil {
		if m := h.sig.Channels[id]; m != nil && now.Sub(m.LastSpoke) < 14*24*time.Hour {
			return true
		}
	}
	return false
}

func (h *slackHarvest) add(key string, item Item) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if key != "" {
		if h.seen[key] {
			return
		}
		h.seen[key] = true
	}
	h.items = append(h.items, item)
}

func (h *slackHarvest) who(user, username string) string {
	return h.dir.Who(user, username)
}

// say renders one message body for the model.
func (h *slackHarvest) say(text string) string { return h.dir.Render(text) }

// mentions: somebody said your name, anywhere you can see.
func (h *slackHarvest) mentions(client *slackClient, handle string, w Window) {
	for _, m := range slackSearchRaw(client, "@"+handle, w) {
		if m.at.Before(w.Since) {
			continue
		}
		h.add(m.channelID+"/"+m.ts, Item{
			Kind:   "slack.mention",
			Title:  fmt.Sprintf("#%s — %s", m.channelName, h.who(m.user, m.username)),
			Body:   clip(h.say(m.text), 500),
			URL:    m.permalinkOr(h.domain),
			Time:   m.at,
			Tags:   []string{m.channelName},
			Origin: "#" + m.channelName,
		})
	}
}

// threads: find where you spoke recently, then see who answered. This is the
// one that catches the reply you never saw, which is most of what a morning
// brief is for.
func (h *slackHarvest) threads(client *slackClient, handle string, w Window) {
	// Look further back than the window for your own messages: a thread you
	// posted in three days ago can still get a reply overnight.
	lookback := Window{Now: w.Now, Since: w.Since.AddDate(0, 0, -3)}

	threads := []threadRef{}
	seen := map[string]bool{}
	for _, m := range slackSearchRaw(client, "from:@"+handle, lookback) {
		if m.channelID == "" {
			continue
		}
		// Every match is somewhere the reader spoke. Remember it: this is
		// the cheapest evidence of ownership there is, and it is what puts a
		// new channel at the front of the sweep.
		h.spoke[m.channelID] = true
		h.meta(m.channelID, m.channelName, false, "").Spoke(true, m.at)

		thread := m.threadTS
		if thread == "" {
			thread = m.ts
		}
		key := m.channelID + "/" + thread
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(threads) < 25 {
			threads = append(threads, threadRef{m.channelID, thread, m.channelName})
		}
	}

	for _, t := range threads {
		h.readThread(client, t, w)
	}
}

// threadRef is a thread worth catching up on, however it was spotted.
type threadRef struct{ channel, thread, channelName string }

// readThread turns one thread into one item: what was said after you, by whom.
// The store remembers how far into each thread the reader has been shown, so
// a thread read yesterday is asked only for what came after.
func (h *slackHarvest) readThread(client *slackClient, t threadRef, w Window) {
	key := t.channel + "/" + t.thread
	var state *ThreadState
	if h.sig != nil {
		state = h.sig.Threads[key]
	}

	params := url.Values{"channel": {t.channel}, "ts": {t.thread}, "limit": {"50"}}
	if state != nil && state.LastReply != "" {
		params.Set("oldest", state.LastReply)
	}
	var out struct {
		Messages []slackMessage `json:"messages"`
	}
	if err := client.call("conversations.replies", params, &out); err != nil {
		return
	}

	// Your own words stay in, marked as yours. Leaving them out was a quiet
	// disaster: with no sight of what you actually said, the only thing left to
	// judge your involvement by was the fact that the thread was collected at
	// all, so a brief would take one passing comment and hand you the whole
	// problem, inventing the authority to go with it.
	lines := []Line{}
	others, mine, started := 0, 0, false
	newest := ""
	var latest time.Time
	for _, m := range out.Messages {
		if !m.said() {
			continue
		}
		at := slackTime(m.TS)
		root := m.TS == t.thread
		yours := m.User == h.me

		switch {
		case root && yours:
			started = true
		case yours:
			mine++
		}
		if root || at.Before(w.Since) {
			continue
		}
		if state != nil && m.TS <= state.LastReply {
			continue // already shown
		}
		if m.TS > newest {
			newest = m.TS
		}

		who := h.who(m.User, m.Username)
		if !yours {
			others++
			if at.After(latest) {
				latest = at
			}
		}
		lines = append(lines, Line{TS: m.TS, Who: who, Text: clip(h.say(m.Words()), 220), Mine: yours})
	}

	// Carry the standing forward: on a partial read the root and the older
	// replies are not in the response, so what was learned the first time is
	// the only record of who started it and how often the reader spoke.
	if state == nil {
		state = &ThreadState{Channel: t.channel, TS: t.thread, Name: t.channelName}
	}
	state.Started = state.Started || started
	state.Mine += mine
	state.Others += others
	state.Seen = w.Now
	if newest > state.LastReply {
		state.LastReply = newest
	}
	if h.sig != nil {
		h.sig.Threads[key] = state
	}
	if others == 0 {
		return // only your own words moved, which is not news
	}

	// One item per thread, not one per reply.
	h.add(key, Item{
		Kind:   "slack.thread_reply",
		Title:  fmt.Sprintf("#%s — %d repl%s in %s", t.channelName, state.Others, plural(state.Others), standing(state.Started, state.Mine)),
		Lines:  lines,
		URL:    messageLink(h.domain, t.channel, t.thread, ""),
		Time:   latest,
		Tags:   []string{t.channelName, "thread"},
		Origin: "#" + t.channelName,
	})
}

// direct: DMs and group DMs, where everything is addressed to you by
// definition and no filtering is needed.
func (h *slackHarvest) direct(client *slackClient, access string, w Window) {
	types := []string{}
	if client.has("im:read") && (access == AccessDMs || access == AccessAll) {
		types = append(types, "im")
	}
	if client.has("mpim:read") && (access == AccessDMs || access == AccessAll) {
		types = append(types, "mpim")
	}
	if len(types) == 0 {
		return
	}

	conversations := listConversations(client, strings.Join(types, ","), 200)
	sort.Slice(conversations, func(i, j int) bool { return conversations[i].Updated > conversations[j].Updated })
	if len(conversations) > 30 {
		conversations = conversations[:30]
	}

	for _, c := range conversations {
		if !client.budget() {
			break
		}
		cursorKey := "slack:dm:" + c.ID
		var out struct {
			Messages []slackMessage `json:"messages"`
		}
		if err := client.call("conversations.history", url.Values{
			"channel": {c.ID},
			"oldest":  {oldestFor(h.cursors.Get(cursorKey), w.Since)},
			"limit":   {"30"},
		}, &out); err != nil {
			continue
		}

		lines := []Line{}
		newest := ""
		var latest time.Time
		var from string
		for i := len(out.Messages) - 1; i >= 0; i-- {
			m := out.Messages[i]
			if m.Type != "message" {
				continue
			}
			if m.TS > newest {
				newest = m.TS
			}
			yours := m.User == h.me
			who := h.who(m.User, m.Username)
			if !yours {
				from = who
				if at := slackTime(m.TS); at.After(latest) {
					latest = at
				}
			}
			lines = append(lines, Line{TS: m.TS, Who: who, Text: clip(h.say(m.Words()), 220), Mine: yours})
		}
		h.cursors.Advance(cursorKey, newest, w.Now, len(out.Messages) > 0)
		if from == "" {
			continue // only your own words, which is not somebody writing to you
		}

		where := from
		// A group DM can be muted like a room. A message from one person to
		// you cannot: silently dropping someone who wrote to you directly is
		// not a preference, it is a way to miss things, and no setting should
		// be able to do it by accident.
		origin := ""
		if c.IsMPIM {
			where = "group DM"
			origin = "group DM: " + from
		}

		// A conversation, not a pile of separate messages.
		h.add("dm/"+c.ID, Item{
			Kind:   "slack.dm",
			Title:  fmt.Sprintf("DM — %s", where),
			Lines:  lines,
			URL:    messageLink(h.domain, c.ID, "", ""),
			Time:   latest,
			Tags:   []string{"dm"},
			Origin: origin,
		})
	}
}

// oldestFor is the Slack `oldest` parameter: the cursor if there is one, else
// the start of the window, and never earlier than the window.
func oldestFor(cur Cursor, since time.Time) string {
	floor := strconv.FormatInt(since.Unix(), 10)
	if cur.TS != "" && cur.TS > floor {
		return cur.TS
	}
	return floor
}

// Slack renders room-wide pings as these tokens. They reach you as surely as
// your own name does, and no search query finds them.
var broadcastTokens = []string{"<!here", "<!channel", "<!everyone", "<!subteam^"}

func broadcastKind(text string) string {
	switch {
	case strings.Contains(text, "<!subteam^"):
		return "a group you're in"
	case strings.Contains(text, "<!here"):
		return "@here"
	case strings.Contains(text, "<!channel"):
		return "@channel"
	case strings.Contains(text, "<!everyone"):
		return "@everyone"
	}
	return ""
}

// scanChannels reads the busiest channels you're in. When search has already
// covered your name it looks only for room-wide pings; without search it does
// both, which is slower and less complete but better than nothing.
func (h *slackHarvest) scanChannels(client *slackClient, access string, w Window, broadcastsOnly bool) {
	// Channels only. DMs are gathered by direct(), and asking for them here
	// too fills the page with conversations this pass then skips, pushing real
	// channels off the end of the list.
	types := channelTypes(access, client)
	kinds := []string{}
	for _, kind := range strings.Split(types, ",") {
		if kind == "public_channel" || kind == "private_channel" {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return
	}
	channels := listConversations(client, strings.Join(kinds, ","), 1000)
	for _, c := range channels {
		h.meta(c.ID, c.Name, c.IsPvt, c.Creator)
	}
	channels = h.order(channels, w)

	mention := "<@" + h.me + ">"
	catchUp := []threadRef{}

	// One at a time. Six workers used to share a single per-method clock, so
	// they were six goroutines waiting in one queue.
	for _, c := range channels {
		if c.IsIM || c.IsMPIM {
			continue // handled by direct()
		}
		// Stop sweeping early. A thread you are in that gained thirty
		// replies overnight is worth more than the next channel you
		// have never spoken in, and it can only be read after the
		// sweep that found it.
		if !client.budgetFor(threadReserve) {
			break
		}
		// Muted or faded: the cheapest signal is the one never gathered,
		// and this is the only place that saving is real.
		if w.Skip != nil && w.Skip("#"+c.Name) {
			continue
		}

		cursorKey := "slack:" + c.ID
		var out struct {
			Messages []slackMessage `json:"messages"`
		}
		if err := client.call("conversations.history", url.Values{
			"channel": {c.ID},
			"oldest":  {oldestFor(h.cursors.Get(cursorKey), w.Since)},
			"limit":   {strconv.Itoa(perChannel)},
		}, &out); err != nil {
			continue
		}
		newest := ""
		for _, m := range out.Messages {
			if m.TS > newest {
				newest = m.TS
			}
		}
		h.cursors.Advance(cursorKey, newest, w.Now, len(out.Messages) > 0)
		if len(out.Messages) == 0 {
			continue
		}

		meta := h.meta(c.ID, c.Name, c.IsPvt, c.Creator)
		joined := false
		speakers := map[string]bool{}
		for _, m := range out.Messages {
			if m.said() {
				meta.Spoke(m.User == h.me, slackTime(m.TS))
				if m.User != h.me && m.User != "" {
					speakers[m.User] = true
				}
			}
			// Being added to a room is a signal: somebody put you here for a
			// reason. A join is housekeeping everywhere else, but here it is
			// the whole reason to read the room.
			if m.User == h.me && (m.Subtype == "channel_join" || m.Subtype == "group_join") {
				joined = true
				meta.Joined = slackTime(m.TS)
			}
		}
		// Did you speak here lately, or get added lately? Then this is a
		// conversation you're in, and the rest of it is context rather than
		// noise. Judged from the store, not from this read: with a cursor,
		// the read only holds what is new, and your own words may well be
		// older than that.
		owned := w.Owned != nil && w.Owned("#"+c.Name)
		yours := h.spokeRecently(c.ID, w.Now) || owned || joined || h.joinedRecently(c.ID, w.Now)
		// A room you are merely in can still light up: several people, a
		// burst of messages. That is worth a look even from the outside.
		litUp := !yours && len(speakers) >= 3 && countSaid(out.Messages) >= 8

		conversation := []Line{}
		var conversationAt time.Time

		for _, m := range out.Messages {
			if !m.said() {
				continue
			}

			// A thread you are in that gained replies overnight is the single
			// most valuable thing Slack has, and history says so directly: no
			// search:read needed, one extra call per thread that qualifies.
			// This has to come before your own messages are set aside, or the
			// threads you started, the ones most likely to be yours, are the
			// exact ones never collected.
			if m.ReplyCount > 0 && m.yours(h.me) && slackTime(m.LatestReply).After(w.Since) {
				catchUp = append(catchUp, threadRef{c.ID, m.TS, c.Name})
			}

			// Entity detection reads the wire text, where a broadcast is still
			// <!channel> and your name is still <@U1>. Only the body is rendered.
			words := m.Words()
			said := h.say(words)

			// Your own words are never an item, but they are context: a room
			// reads very differently once you can see what you said in it.
			if m.User == h.me {
				if yours && len(conversation) < 10 {
					conversation = append(conversation, Line{TS: m.TS, Who: "you", Text: clip(said, 200), Mine: true})
				}
				continue
			}

			// A question in a room you own that nobody has answered is yours
			// to answer. This is a rule, not a judgement: the model is told
			// it is unanswered rather than asked whether it might be.
			if owned && m.ThreadTS == "" && unanswered(said, slackTime(m.TS), w.Now, m.ReplyCount, m.ReplyUsers, h.dir.IsBot, h.me) {
				h.add(c.ID+"/"+m.TS, Item{
					Kind:   "slack.unanswered",
					Title:  fmt.Sprintf("#%s — unanswered: %s asked", c.Name, h.who(m.User, m.Username)),
					Body:   clip(said, 500),
					URL:    messageLink(h.domain, c.ID, m.TS, ""),
					Time:   slackTime(m.TS),
					Tags:   []string{c.Name, "unanswered"},
					Origin: "#" + c.Name,
				})
				continue
			}

			ping := broadcastKind(words)
			named := !broadcastsOnly && strings.Contains(words, mention)

			if ping == "" && !named {
				// Not aimed at you, but if you're part of this conversation,
				// or the room is busy enough to be news, it's still worth the
				// model seeing.
				if (yours || litUp) && len(conversation) < 10 {
					conversation = append(conversation, Line{TS: m.TS, Who: h.who(m.User, m.Username), Text: clip(said, 200)})
					if at := slackTime(m.TS); at.After(conversationAt) {
						conversationAt = at
					}
				}
				continue
			}

			// A ping with no words behind it is a notification, not news. Left
			// in, it becomes a paragraph explaining that there is nothing to do.
			if !named && !meaningful(said) {
				continue
			}

			kind, title := "slack.mention", fmt.Sprintf("#%s — %s", c.Name, h.who(m.User, m.Username))
			if ping != "" {
				kind = "slack.broadcast"
				title = fmt.Sprintf("#%s — %s pinged %s", c.Name, h.who(m.User, m.Username), ping)
			}

			h.add(c.ID+"/"+m.TS, Item{
				Kind:   kind,
				Title:  title,
				Body:   clip(said, 500),
				URL:    messageLink(h.domain, c.ID, m.TS, m.ThreadTS),
				Time:   slackTime(m.TS),
				Tags:   []string{c.Name},
				Origin: "#" + c.Name,
			})
		}

		// One item for the room, in the order it was said. One stray line is
		// not a conversation that carried on without you, it is a channel you
		// happen to be in, and it reads as filler. The store appends these
		// lines to yesterday's, so a single new line on a room already held
		// is still worth sending.
		sort.SliceStable(conversation, func(i, j int) bool { return conversation[i].TS < conversation[j].TS })
		if len(conversation) > 0 && (len(conversation) > 1 || h.cursors.Get(cursorKey).TS != "") {
			title := fmt.Sprintf("#%s — carried on after you", c.Name)
			switch {
			case joined:
				title = fmt.Sprintf("#%s — you were just added, and here is what it is about", c.Name)
			case litUp:
				title = fmt.Sprintf("#%s — lit up: %d people talking", c.Name, len(speakers))
			}
			h.add("room/"+c.ID, Item{
				Kind:   "slack.active_channel",
				Title:  title,
				Lines:  conversation,
				URL:    messageLink(h.domain, c.ID, "", ""),
				Time:   conversationAt,
				Tags:   []string{c.Name},
				Origin: "#" + c.Name,
			})
		}
	}

	// Newest first: if the budget runs out, it should run out on the threads
	// that went quiet, not the ones still moving.
	sort.Slice(catchUp, func(i, j int) bool { return catchUp[i].thread > catchUp[j].thread })
	for i, t := range catchUp {
		if i >= maxThreads || !client.budget() {
			break
		}
		h.readThread(client, t, w)
	}
}

// order puts the rooms in the order they deserve to be read: where the reader
// spoke lately, then what they own, then everything else by how recently
// anybody spoke there. The old order was Slack's "updated" field, which is
// when the room's metadata last changed and says nothing about messages.
func (h *slackHarvest) order(channels []slackChannel, w Window) []slackChannel {
	rank := func(c slackChannel) (int, time.Time) {
		var last time.Time
		if h.sig != nil {
			if m := h.sig.Channels[c.ID]; m != nil {
				last = m.LastOther
				if m.LastSpoke.After(last) {
					last = m.LastSpoke
				}
			}
		}
		if last.IsZero() {
			last = time.Unix(c.Updated, 0)
		}
		switch {
		case h.spokeRecently(c.ID, w.Now):
			return 0, last
		case w.Owned != nil && w.Owned("#"+c.Name):
			return 1, last
		}
		return 2, last
	}
	sort.SliceStable(channels, func(i, j int) bool {
		ri, ti := rank(channels[i])
		rj, tj := rank(channels[j])
		if ri != rj {
			return ri < rj
		}
		return ti.After(tj)
	})
	if len(channels) > maxChannels {
		channels = channels[:maxChannels]
	}
	return channels
}

type slackMessage struct {
	User        string          `json:"user"`
	Username    string          `json:"username"`
	Text        string          `json:"text"`
	TS          string          `json:"ts"`
	ThreadTS    string          `json:"thread_ts"`
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Blocks      json.RawMessage `json:"blocks"`
	Attachments json.RawMessage `json:"attachments"`

	// Thread roots carry their own summary, which is how a thread you are part
	// of can be spotted without search:read: Slack names up to five people who
	// replied, and says whether you are following it.
	ReplyCount  int      `json:"reply_count"`
	ReplyUsers  []string `json:"reply_users"`
	LatestReply string   `json:"latest_reply"`
	Subscribed  bool     `json:"subscribed"`
}

// Slack files housekeeping under the same "message" type as things people
// actually said. Joins, topic changes and pinned items are not news, and left
// in they end up quoted back at the reader inside a brief item.
var chatter = map[string]bool{
	"channel_join": true, "channel_leave": true, "group_join": true, "group_leave": true,
	"channel_topic": true, "channel_purpose": true, "channel_name": true,
	"channel_archive": true, "channel_unarchive": true, "bot_add": true, "bot_remove": true,
	"pinned_item": true, "unpinned_item": true, "reminder_add": true, "file_comment": true,
}

// said reports whether a person meant to say this to the room.
func (m slackMessage) said() bool { return m.Type == "message" && !chatter[m.Subtype] }

// yours reports whether this thread is one the reader is part of.
func (m slackMessage) yours(me string) bool {
	if m.Subscribed || m.User == me {
		return true
	}
	for _, u := range m.ReplyUsers {
		if u == me {
			return true
		}
	}
	return false
}

// Words is everything the message says, wherever Slack chose to put it.
func (m slackMessage) Words() string {
	return messageWords(m.Text, m.Blocks, m.Attachments)
}

// tail keeps the end of a conversation rather than the start.
//
// A thread you missed is read backwards: the last thing said is the thing you
// need, and any resolution is at the bottom. Clipping from the front shows the
// problem being raised and hides the answer, which is how a brief ends up
// asking for something that was settled hours ago.
func tail(lines []string, budget int) string {
	kept := 0
	used := 0
	for i := len(lines) - 1; i >= 0; i-- {
		cost := len(lines[i]) + len(" | ")
		if kept > 0 && used+cost > budget {
			skipped := i + 1
			return fmt.Sprintf("(%d earlier %s) | %s",
				skipped, reply(skipped), strings.Join(lines[i+1:], " | "))
		}
		used += cost
		kept++
	}
	return strings.Join(lines, " | ")
}

// standing says how the reader comes to be in a thread. Being in one covers
// everything from opening it to saying "same" once, and those are not the same
// claim on somebody's morning.
func standing(started bool, mine int) string {
	switch {
	case started:
		return "a thread you started"
	case mine == 1:
		return "a thread you commented in once"
	case mine > 1:
		return fmt.Sprintf("a thread you commented in %d times", mine)
	}
	return "a thread you follow but have not spoken in"
}

func reply(n int) string {
	if n == 1 {
		return "reply"
	}
	return "replies"
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// slackSearchRaw is one search request, unpacked into something usable. Slack
// answers this from its own index, so it sees every channel you can.
type slackMatch struct {
	text, username, user, ts, threadTS string
	channelID, channelName, permalink  string
	at                                 time.Time
}

func (m slackMatch) permalinkOr(domain string) string {
	if m.permalink != "" {
		return m.permalink
	}
	return messageLink(domain, m.channelID, m.ts, m.threadTS)
}

func slackSearchRaw(client *slackClient, query string, w Window) []slackMatch {
	// Slack's `after:` is day-granular and exclusive of the day itself.
	after := w.Since.AddDate(0, 0, -1).Format("2006-01-02")

	var out struct {
		Messages struct {
			Matches []struct {
				Text      string `json:"text"`
				Username  string `json:"username"`
				User      string `json:"user"`
				TS        string `json:"ts"`
				Permalink string `json:"permalink"`
				Channel   struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
			} `json:"matches"`
		} `json:"messages"`
	}
	if err := client.call("search.messages", url.Values{
		"query": {query + " after:" + after},
		"count": {"60"},
		"sort":  {"timestamp"},
	}, &out); err != nil {
		return nil
	}

	matches := make([]slackMatch, 0, len(out.Messages.Matches))
	for _, m := range out.Messages.Matches {
		matches = append(matches, slackMatch{
			text: m.Text, username: m.Username, user: m.User, ts: m.TS,
			channelID: m.Channel.ID, channelName: m.Channel.Name,
			permalink: m.Permalink, at: slackTime(m.TS),
		})
	}
	return matches
}

// Room is a channel as the settings page names it: for choosing what Pomona
// must never read.
type Room struct {
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

// slackRooms lists the channels a token can see, public and (if the tier
// allows) private. Direct messages are not rooms and are not listed: they
// are switched off as a whole or not at all.
func slackRooms(ctx context.Context, settings map[string]string) ([]Room, error) {
	client := &slackClient{ctx: ctx, token: settings["token"], granted: map[string]bool{}, nextAt: map[string]time.Time{}}
	// One call first, so the scopes are known and the listing asks only for
	// types the token can read.
	var me struct {
		OK bool `json:"ok"`
	}
	if err := client.call("auth.test", nil, &me); err != nil {
		return nil, err
	}
	types := []string{"public_channel"}
	access := settings["access"]
	if (access == AccessPrivate || access == AccessAll) && client.has("groups:read") {
		types = append(types, "private_channel")
	}
	rooms := []Room{}
	for _, c := range listConversations(client, strings.Join(types, ","), 1000) {
		if c.Name == "" || c.Archived || c.IsIM || c.IsMPIM {
			continue
		}
		rooms = append(rooms, Room{Name: c.Name, Private: c.IsPvt})
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })
	return rooms, nil
}

// listConversations walks every page. Slack answers 200 at a time and hands
// back a cursor; stopping at the first page silently loses the tail, which is
// where the channel you needed turns out to be.
func listConversations(client *slackClient, types string, limit int) []slackChannel {
	all := []slackChannel{}
	cursor := ""
	for page := 0; page < 10; page++ { // a guard, not a real limit
		params := url.Values{
			"types":            {types},
			"exclude_archived": {"true"},
			"limit":            {"200"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var out struct {
			Channels []slackChannel `json:"channels"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := client.call("users.conversations", params, &out); err != nil {
			break
		}
		all = append(all, out.Channels...)

		cursor = out.Meta.NextCursor
		if cursor == "" || len(all) >= limit {
			break
		}
	}
	return all
}

// messageLink builds the permalink Slack would give us, without spending an
// API call per message to ask for it.
func messageLink(domain, channel, ts, threadTS string) string {
	if domain == "" {
		return ""
	}
	// A whole room rather than one message: there is no timestamp to point at,
	// and appending an empty one gives a link ending in a bare "p" that goes
	// nowhere. The channel's own archive is the right destination.
	if ts == "" {
		return fmt.Sprintf("https://%s.slack.com/archives/%s", domain, channel)
	}

	link := fmt.Sprintf("https://%s.slack.com/archives/%s/p%s", domain, channel, strings.ReplaceAll(ts, ".", ""))
	if threadTS != "" && threadTS != ts {
		link += "?thread_ts=" + threadTS + "&cid=" + channel
	}
	return link
}

func slackTime(ts string) time.Time {
	seconds, err := strconv.ParseFloat(strings.SplitN(ts, ".", 2)[0], 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0)
}
