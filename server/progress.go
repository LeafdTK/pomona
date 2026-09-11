package main

import (
	"sync"
	"time"
)

// What a brief in progress is doing, so the page can show the truth rather than
// a spinner. Slack alone can take five minutes, and five minutes of no feedback
// reads as broken.
//
// One of these per account. It is deliberately in memory only: it is worthless
// a minute after it is written, and writing it to the vault would mean
// re-encrypting the store every couple of seconds.

// Stage names, in the order they happen.
const (
	StageReading = "reading" // asking every connected source
	StageSorting = "sorting" // ranking and trimming what came back
	StageWriting = "writing" // Claude has the prompt
	StageSetting = "setting" // storing and typesetting
	StageDone    = "done"
	StageFailed  = "failed"
)

// A Step is one source, and how it went.
type Step struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

type Progress struct {
	Stage   string    `json:"stage"`
	Note    string    `json:"note"`
	Steps   []Step    `json:"steps"`
	Started time.Time `json:"started"`
	Signals int       `json:"signals"`
	Error   string    `json:"error,omitempty"`
	// What the job produced, once it is done: the brief's id for a write,
	// a summary for a refresh. The page polls for these rather than holding
	// a request open for five minutes, which a proxy in front of a hosted
	// server would cut off at one hundred seconds.
	BriefID string `json:"briefId,omitempty"`
	Kind    string `json:"kind,omitempty"` // write | refresh
}

// Running reports whether a brief is being written right now.
func (p Progress) Running() bool {
	return p.Stage != "" && p.Stage != StageDone && p.Stage != StageFailed
}

// progressBoard holds the live progress for every account on the server.
type progressBoard struct {
	mu sync.Mutex
	by map[string]*Progress
}

func newProgressBoard() *progressBoard { return &progressBoard{by: map[string]*Progress{}} }

func (b *progressBoard) For(user string) Progress {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.by[user]; p != nil {
		return *p
	}
	return Progress{}
}

func (b *progressBoard) start(user, kind string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.by[user] = &Progress{Stage: StageReading, Note: "Waking up your sources", Started: time.Now(), Kind: kind}
}

func (b *progressBoard) edit(user string, change func(*Progress)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.by[user]; p != nil {
		change(p)
	}
}

// watcher adapts one account's progress to the Watcher CollectAll expects.
type watcher struct {
	board *progressBoard
	user  string
}

func (w watcher) Reading(names []string) {
	w.board.edit(w.user, func(p *Progress) {
		p.Steps = make([]Step, 0, len(names))
		for _, name := range names {
			p.Steps = append(p.Steps, Step{Name: name})
		}
		p.Note = "Reading " + andList(names)
	})
}

func (w watcher) Read(name string, count int, failed string) {
	w.board.edit(w.user, func(p *Progress) {
		for i := range p.Steps {
			if p.Steps[i].Name == name {
				p.Steps[i].Done = true
				p.Steps[i].Count = count
				p.Steps[i].Error = failed
			}
		}
		waiting := []string{}
		for _, step := range p.Steps {
			if !step.Done {
				waiting = append(waiting, step.Name)
			}
		}
		if len(waiting) > 0 {
			p.Note = "Still reading " + andList(waiting)
		}
	})
}

// andList writes a list the way a person would: "Slack", "Slack and GitHub",
// "Slack, GitHub and Calendar".
func andList(items []string) string {
	switch len(items) {
	case 0:
		return "nothing"
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	out := ""
	for i, item := range items[:len(items)-1] {
		if i > 0 {
			out += ", "
		}
		out += item
	}
	return out + " and " + items[len(items)-1]
}
