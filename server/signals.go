package main

import (
	"sort"
	"strings"
	"time"
)

// What has been gathered, kept.
//
// Until now an item lived exactly as long as one Generate call: five minutes
// of Slack sweeping, one failed Claude call, and every message read was gone.
// Worse, every morning began from nothing, re-reading the same three days it
// had read the morning before.
//
// The store remembers. Each place has a cursor, so the next read asks Slack
// only for what is new; each conversation keeps its lines, so a thread that
// grew overnight is the same thread with more in it rather than a fresh item
// that has forgotten how it started. It is encrypted like everything else,
// and written once per run.

// Cursor is how far one place has been read.
type Cursor struct {
	Since     time.Time `json:"since"`         // for sources that take a timestamp
	TS        string    `json:"ts,omitempty"`  // the newest Slack ts seen
	LastSweep time.Time `json:"lastSweep"`     // when it was last asked
	Empty     int       `json:"empty"`         // consecutive reads with nothing new
	Err       string    `json:"err,omitempty"` // what went wrong last time, if anything
}

// ChannelMeta is what a Slack room looks like from the reader's chair. It is
// the raw material of ownership: who posts here, how often, and whether it
// was the reader who made the place.
type ChannelMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Private   bool      `json:"private"`
	Creator   string    `json:"creator,omitempty"`
	LastSpoke time.Time `json:"lastSpoke"` // the reader, any message
	LastOther time.Time `json:"lastOther"` // anyone else
	Joined    time.Time `json:"joined"`    // when the reader was added, if a sweep saw it
	MyPosts   int       `json:"myPosts"`   // rolling counters, decayed weekly
	Posts     int       `json:"posts"`
	Decayed   time.Time `json:"decayed"` // when the counters were last aged
}

// ThreadState is a thread the reader is part of, and how far into it they
// have been shown.
type ThreadState struct {
	Channel   string    `json:"channel"`
	TS        string    `json:"ts"`
	Name      string    `json:"name"`
	LastReply string    `json:"lastReply"` // newest reply ts already stored
	Started   bool      `json:"started"`   // the reader opened it
	Mine      int       `json:"mine"`      // how many times the reader has spoken in it
	Others    int       `json:"others"`    // replies by anyone else, ever shown
	Seen      time.Time `json:"seen"`
}

// StoredItem is an Item with a history.
type StoredItem struct {
	Item
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Updates   int       `json:"updates"` // times new lines arrived after first sight
	Triage    *Triage   `json:"triage,omitempty"`
}

// Triage is the small model's read of one item. Filled in later; kept here so
// the store is the one place an item's state lives.
type Triage struct {
	Class   string    `json:"class"`   // ask_you | unanswered | fyi | done | noise
	Score   int       `json:"score"`   // 0-100
	AsksOf  string    `json:"asksOf"`  // you | someone_else | nobody
	Due     string    `json:"due"`     // YYYY-MM-DD or ""
	OneLine string    `json:"oneLine"` // for the strip, at most 120 chars
	Model   string    `json:"model"`
	At      time.Time `json:"at"`
}

type SignalStore struct {
	Version     int                     `json:"version"`
	Me          string                  `json:"me,omitempty"` // the reader's Slack user id
	me          string                  // same, for code that has the store but not the config
	Items       map[string]*StoredItem  `json:"items"`
	Cursors     map[string]*Cursor      `json:"cursors"`
	Channels    map[string]*ChannelMeta `json:"channels"` // by Slack channel id
	Threads     map[string]*ThreadState `json:"threads"`  // by "channel/ts"
	Events      []Event                 `json:"events"`   // the last calendar read, whole
	Reports     []SourceReport          `json:"reports"`  // how each source fared last time
	RefreshedAt time.Time               `json:"refreshedAt"`
}

const (
	signalsVersion = 1
	signalsKeep    = 72 * time.Hour
	signalsMax     = 3000
)

func newSignalStore() *SignalStore {
	return &SignalStore{
		Version:  signalsVersion,
		Items:    map[string]*StoredItem{},
		Cursors:  map[string]*Cursor{},
		Channels: map[string]*ChannelMeta{},
		Threads:  map[string]*ThreadState{},
	}
}

// ready makes a store loaded from disk (or a zero one) safe to use.
func (s *SignalStore) ready() {
	if s.Items == nil {
		s.Items = map[string]*StoredItem{}
	}
	if s.Cursors == nil {
		s.Cursors = map[string]*Cursor{}
	}
	if s.Channels == nil {
		s.Channels = map[string]*ChannelMeta{}
	}
	if s.Threads == nil {
		s.Threads = map[string]*ThreadState{}
	}
	if s.Version == 0 {
		s.Version = signalsVersion
	}
	s.me = s.Me
}

// CursorSet hands the collectors a view of the cursors they can advance from
// several goroutines at once.
func (s *SignalStore) CursorSet() *CursorSet {
	s.ready()
	return &CursorSet{cursors: s.Cursors}
}

// Merge folds one morning's reading into what was already known and reports
// what was genuinely new: items never seen before, and conversations that
// gained lines. A second sighting of the same thing is not new, which is the
// whole reason the store exists.
func (s *SignalStore) Merge(items []Item, now time.Time) (fresh []*StoredItem) {
	s.ready()
	for _, it := range items {
		if it.Key == "" {
			it.Key = keyFor(it.Source, it)
		}
		have := s.Items[it.Key]
		if have == nil {
			stored := &StoredItem{Item: it, FirstSeen: now, LastSeen: now}
			if len(stored.Lines) > 0 {
				stored.Body = tail(lineText(stored.Lines), conversationBudget(stored.Kind))
			}
			s.Items[it.Key] = stored
			fresh = append(fresh, stored)
			continue
		}

		have.LastSeen = now
		if len(it.Lines) > 0 {
			// A conversation: keep what we had and add what arrived after it.
			added := appendLines(have, it.Lines)
			if added > 0 {
				have.Updates++
				have.Body = tail(lineText(have.Lines), conversationBudget(have.Kind))
				if it.Time.After(have.Time) {
					have.Time = it.Time
				}
				have.Triage = nil // it has changed; whatever was decided is stale
				fresh = append(fresh, have)
			}
			// Titles carry the reply count and the reader's standing; the
			// newest one is the truest.
			have.Title = it.Title
			have.Tags = it.Tags
			continue
		}

		// Not a conversation: the newest reading wins, and a real change is
		// worth a fresh look.
		changed := it.Body != have.Body || it.Title != have.Title
		if it.Time.After(have.Time) {
			have.Time = it.Time
		}
		have.Body, have.Title, have.Tags, have.Origin = it.Body, it.Title, it.Tags, it.Origin
		if changed {
			have.Updates++
			have.Triage = nil
			fresh = append(fresh, have)
		}
	}
	return fresh
}

// appendLines adds the lines newer than anything stored, and says how many.
func appendLines(have *StoredItem, incoming []Line) int {
	newest := ""
	for _, l := range have.Lines {
		if l.TS > newest {
			newest = l.TS
		}
	}
	added := 0
	for _, l := range incoming {
		if l.TS > newest {
			have.Lines = append(have.Lines, l)
			added++
		}
	}
	if added > 0 {
		sort.SliceStable(have.Lines, func(i, j int) bool { return have.Lines[i].TS < have.Lines[j].TS })
	}
	return added
}

func lineText(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		who := l.Who
		if l.Mine {
			who = "you"
		}
		out = append(out, who+": "+l.Text)
	}
	return out
}

// conversationBudget is how much of a conversation the model gets to see,
// matching what the collectors gave before the store existed.
func conversationBudget(kind string) int {
	switch kind {
	case "slack.thread_reply":
		return 1400
	case "slack.dm":
		return 1000
	}
	return 900
}

// Resolved marks stored items of these kinds that did not come back this
// morning: an open pull request that is no longer open was merged or closed,
// and that is news rather than silence.
func (s *SignalStore) Resolved(source string, kinds []string, present map[string]bool, now time.Time) []*StoredItem {
	s.ready()
	wanted := map[string]bool{}
	for _, k := range kinds {
		wanted[k] = true
	}
	out := []*StoredItem{}
	for key, it := range s.Items {
		if it.Source != source || !wanted[it.Kind] || present[key] || hasTag(it.Tags, "resolved") {
			continue
		}
		it.Tags = append(it.Tags, "resolved")
		it.Time = now
		it.Triage = nil
		out = append(out, it)
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// Prune forgets what is old, and caps the rest. Old is judged by the item's
// own time, not by when it was seen: a thread that is still moving is not old.
func (s *SignalStore) Prune(now time.Time) {
	s.ready()
	cutoff := now.Add(-signalsKeep)
	for key, it := range s.Items {
		if it.Time.Before(cutoff) && it.LastSeen.Before(cutoff) {
			delete(s.Items, key)
		}
	}
	if len(s.Items) > signalsMax {
		keys := make([]string, 0, len(s.Items))
		for k := range s.Items {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return s.Items[keys[i]].Time.After(s.Items[keys[j]].Time) })
		for _, k := range keys[signalsMax:] {
			delete(s.Items, k)
		}
	}
	for key, t := range s.Threads {
		if t.Seen.Before(cutoff) {
			delete(s.Threads, key)
		}
	}
}

// ResultsSince rebuilds what CollectAll used to return, from the store, for
// everything that moved after a moment. The rest of the pipeline, Silence,
// FadedOut, Refine, BuildPrompt, decorate, does not know the store exists.
func (s *SignalStore) ResultsSince(since time.Time, cfg *Config) []*SourceResult {
	s.ready()
	byID := map[string]*SourceResult{}
	order := []string{}

	// Every enabled source gets a result, so a quiet one reads as quiet
	// rather than missing, and a broken one carries its error forward.
	for _, c := range Collectors() {
		if settings := cfg.Sources[c.ID]; settings != nil && settings["enabled"] == "true" {
			byID[c.ID] = &SourceResult{ID: c.ID, Name: c.Name, IconKey: c.IconKey, OK: true}
			order = append(order, c.ID)
		}
	}
	for _, custom := range cfg.Custom {
		if custom.Enabled {
			byID[custom.ID] = &SourceResult{ID: custom.ID, Name: custom.Name, IconKey: "provider:link", OK: true}
			order = append(order, custom.ID)
		}
	}
	for _, rep := range s.Reports {
		if r := byID[rep.ID]; r != nil && !rep.OK {
			r.OK, r.Err = false, rep.Error
		}
	}

	for _, it := range s.Items {
		r := byID[it.Source]
		if r == nil || !it.Time.After(since) {
			continue
		}
		r.Items = append(r.Items, it.Item)
	}
	if cal := byID["calendar"]; cal != nil {
		cal.Events = append([]Event{}, s.Events...)
	}

	out := make([]*SourceResult, 0, len(order))
	for _, id := range order {
		r := byID[id]
		sort.SliceStable(r.Items, func(i, j int) bool { return r.Items[i].Time.After(r.Items[j].Time) })
		out = append(out, r)
	}
	return out
}

// NoteChannel records what a sweep learned about a room.
func (s *SignalStore) NoteChannel(id, name string, private bool, creator string) *ChannelMeta {
	s.ready()
	m := s.Channels[id]
	if m == nil {
		m = &ChannelMeta{ID: id}
		s.Channels[id] = m
	}
	m.Name, m.Private = name, private
	if creator != "" {
		m.Creator = creator
	}
	return m
}

// Spoke records one message in a room: the reader's own, or somebody else's.
func (m *ChannelMeta) Spoke(mine bool, at time.Time) {
	m.Posts++
	if mine {
		m.MyPosts++
		if at.After(m.LastSpoke) {
			m.LastSpoke = at
		}
		return
	}
	if at.After(m.LastOther) {
		m.LastOther = at
	}
}

// Decay ages the counters once a week, so a project the reader left fades
// from the ownership map on its own.
func (s *SignalStore) Decay(now time.Time) {
	s.ready()
	for _, m := range s.Channels {
		// A room seen for the first time starts its clock now. Decaying on
		// first sight turned one post into zero and lost the evidence it had
		// just gathered.
		if m.Decayed.IsZero() {
			m.Decayed = now
			continue
		}
		if now.Sub(m.Decayed) < 7*24*time.Hour {
			continue
		}
		m.MyPosts = int(float64(m.MyPosts) * 0.7)
		m.Posts = int(float64(m.Posts) * 0.7)
		m.Decayed = now
	}
}

// ChannelByName finds a room by its name, for matching a place the reader
// named against what was swept.
func (s *SignalStore) ChannelByName(name string) *ChannelMeta {
	name = strings.TrimPrefix(strings.ToLower(name), "#")
	for _, m := range s.Channels {
		if strings.ToLower(m.Name) == name {
			return m
		}
	}
	return nil
}
