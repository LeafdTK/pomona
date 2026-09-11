package main

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Item is the normalised unit of signal handed to Claude.
type Item struct {
	// Key is what makes two sightings of the same thing the same thing:
	// "slack:<url>", "github:<url>". CollectAll fills it in from Source and
	// URL, so collectors need not know it exists.
	Key    string    `json:"key"`
	Source string    `json:"source"` // which collector: slack, github, linear, calendar, custom:<id>
	Kind   string    `json:"kind"`
	Title  string    `json:"title"`
	Body   string    `json:"body"`
	URL    string    `json:"url"`
	Time   time.Time `json:"time"`
	Tags   []string  `json:"tags"`

	// A conversation, kept as lines so that tomorrow's new replies can be
	// appended to today's and the body rebuilt as the tail of the whole thing.
	// Nil for anything that is not a conversation.
	Lines []Line `json:"lines,omitempty"`

	// Where this came from, in the form a person would mute: "#hq-hq" for a
	// channel, "hackclub/orchard" for a repository. Empty means unmutable.
	Origin string `json:"origin"`
}

// Line is one message in a conversation item.
type Line struct {
	TS   string `json:"ts"`
	Who  string `json:"who"`
	Text string `json:"text"`
	Mine bool   `json:"mine"`
}

// Event is a calendar entry. Times are real instants; the prompt formats them.
type Event struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Location    string    `json:"location"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"allDay"`
	Recurring   bool      `json:"recurring"`
	Attendees   []string  `json:"attendees"`
}

// SourceResult is one connector's morning.
type SourceResult struct {
	ID      string
	Name    string
	IconKey string
	OK      bool
	Err     string
	Items   []Item
	Events  []Event
}

// Collector fetches one source.
type Collector struct {
	ID      string
	Name    string
	IconKey string
	Blurb   string
	Help    string
	Fields  []Field
	Fetch   func(ctx context.Context, settings map[string]string, window Window) ([]Item, []Event, error)
}

type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Placeholder string   `json:"placeholder"`
	Help        string   `json:"help,omitempty"`
	Options     []Option `json:"options,omitempty"`
	Default     string   `json:"default,omitempty"`
}

// Option is one choice in a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Window is the slice of time a brief covers.
type Window struct {
	Now   time.Time
	Since time.Time

	// Skip says whether a place is muted or has faded, so a collector can
	// decline to spend a paced call on it. Checked before the call, which is
	// the only time it saves anything. Nil means skip nothing.
	Skip func(origin string) bool

	// Cursors are where each place was last read to, so a collector asks
	// only for what is new. Nil means read the whole window, as the source
	// test does.
	Cursors *CursorSet

	// Owned says whether a place is one the reader owns, which a collector
	// reads first and never lets go cold. Nil means nothing is.
	Owned func(origin string) bool

	// Budget is how long the slow collector may take. Zero means its default.
	Budget time.Duration

	// Signals is the store a collector may write what it learns about places
	// into: who posts where, which threads the reader is in. Nil on the
	// source-test path, where nothing is kept.
	Signals *SignalStore
}

// CursorSet is the cursors of one signal store, safe for the collectors that
// run at the same time.
type CursorSet struct {
	mu      sync.Mutex
	cursors map[string]*Cursor
}

func (c *CursorSet) Get(key string) Cursor {
	if c == nil {
		return Cursor{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.cursors[key]; cur != nil {
		return *cur
	}
	return Cursor{}
}

// Advance records a read. ts is the newest Slack timestamp seen (or ""), at
// is the moment of reading, and hadNew says whether anything came back.
func (c *CursorSet) Advance(key, ts string, at time.Time, hadNew bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.cursors[key]
	if cur == nil {
		cur = &Cursor{}
		c.cursors[key] = cur
	}
	if ts != "" && ts > cur.TS {
		cur.TS = ts
	}
	cur.Since = at
	cur.LastSweep = at
	if hadNew {
		cur.Empty = 0
	} else {
		cur.Empty++
	}
}

func Collectors() []Collector {
	return []Collector{calendarCollector(), slackCollector(), githubCollector(), linearCollector()}
}

func collectorByID(id string) *Collector {
	for _, c := range Collectors() {
		if c.ID == id {
			return &c
		}
	}
	return nil
}

// CollectAll runs every enabled source at once. A source that fails is
// reported, not fatal: a brief with three of four sources beats no brief.
// Watcher is told which sources are being read and how each one turned out, so
// the page can say what is actually happening instead of spinning for five
// minutes. A nil Watcher is fine.
type Watcher interface {
	Reading(names []string)
	Read(name string, count int, err string)
}

func CollectAll(ctx context.Context, cfg *Config, window Window, watch Watcher) []*SourceResult {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []*SourceResult
	)

	run := func(id, name, iconKey string, fn func() ([]Item, []Event, error)) {
		defer wg.Done()
		items, events, err := fn()
		for i := range items {
			items[i].Source = id
			items[i].Key = keyFor(id, items[i])
		}
		r := &SourceResult{ID: id, Name: name, IconKey: iconKey, OK: err == nil, Items: items, Events: events}
		if err != nil {
			r.Err = err.Error()
		}
		if watch != nil {
			watch.Read(name, len(items)+len(events), r.Err)
		}
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}

	if watch != nil {
		watch.Reading(sourceNames(cfg))
	}

	for _, c := range Collectors() {
		settings := cfg.Sources[c.ID]
		if settings == nil || settings["enabled"] != "true" {
			continue
		}
		wg.Add(1)
		c, settings := c, settings
		go run(c.ID, c.Name, c.IconKey, func() ([]Item, []Event, error) {
			return c.Fetch(ctx, settings, window)
		})
	}

	for _, source := range cfg.Custom {
		if !source.Enabled {
			continue
		}
		wg.Add(1)
		source := source
		go run(source.ID, source.Name, "provider:link", func() ([]Item, []Event, error) {
			items, err := fetchCustom(ctx, source)
			return items, nil, err
		})
	}

	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results
}

// keyFor names an item stably across mornings. The URL is the natural key: a
// message permalink, a PR, a channel archive for a room item. Something with
// no URL is keyed on its title, which is the best there is.
func keyFor(source string, it Item) string {
	if it.URL != "" {
		return source + ":" + strings.TrimRight(strings.ToLower(it.URL), "/")
	}
	return source + ":title:" + strings.ToLower(strings.TrimSpace(it.Title))
}

// sourceNames lists what this morning will actually try to read, in the order
// the page should show them.
func sourceNames(cfg *Config) []string {
	names := []string{}
	for _, c := range Collectors() {
		if settings := cfg.Sources[c.ID]; settings != nil && settings["enabled"] == "true" {
			names = append(names, c.Name)
		}
	}
	for _, source := range cfg.Custom {
		if source.Enabled {
			names = append(names, source.Name)
		}
	}
	return names
}

// ── Shaping what the model sees ─────────────────────────

// Refine is the cheapest quality lever there is: the same brief written from
// forty clean signals beats one written from a hundred noisy ones, on any
// model. Deduplicate by URL, then keep the strongest few per source.
func Refine(results []*SourceResult, perSource int, ledger Ledger) {
	seen := map[string]bool{}
	for _, r := range results {
		if !r.OK {
			continue
		}
		kept := make([]Item, 0, len(r.Items))
		for _, it := range r.Items {
			key := strings.TrimRight(strings.ToLower(it.URL), "/")
			if key != "" {
				if seen[key] {
					continue // the same PR arriving as a review request and a notification
				}
				seen[key] = true
			}
			kept = append(kept, it)
		}
		sort.SliceStable(kept, func(i, j int) bool {
			return weight(kept[i])*ledger.Standing(kept[i].Origin) >
				weight(kept[j])*ledger.Standing(kept[j].Origin)
		})
		if len(kept) > perSource {
			kept = kept[:perSource]
		}
		r.Items = kept
	}
}

// weight ranks an item by how directly it's aimed at you, then how recent it is.
func weight(it Item) float64 {
	score := map[string]float64{
		"github.review_requested": 100,
		"slack.unanswered":        96,
		"slack.dm":                95,
		"github.shipped":          70,
		"slack.thread_reply":      92,
		"github.assigned":         90,
		"linear.assigned":         85,
		"slack.mention":           80,
		"slack.broadcast":         55,
		"slack.active_channel":    45,
		"github.my_open_pr":       50,
		"linear.updated":          40,
		"github.notification":     30,
	}[it.Kind]

	// A critical advisory outranks everything, including a direct message: it
	// is the one item where being wrong about the order has a blast radius.
	for _, tag := range it.Tags {
		switch tag {
		case "critical":
			score += 60
		case "high":
			score += 35
		}
	}

	if !it.Time.IsZero() {
		// A day old is worth about half of brand new.
		hours := time.Since(it.Time).Hours()
		score += 40 / (1 + hours/24)
	}
	return score
}

func newestFirst(items []Item, limit int) []Item {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Time.After(items[j].Time) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
