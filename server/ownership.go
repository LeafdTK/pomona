package main

import "time"

// Ownership is the map of what the reader owns: rooms they run, repositories
// they maintain. It is what turns "a question in some channel" into "a
// question in your channel", which is the difference between summarising an
// inbox and knowing what is yours.
//
// Nothing here asks the reader anything. It is inferred from where they
// speak, what they merge, and what they made; it is recomputed every morning
// so a project started yesterday is owned today; and every inference is
// shown in the settings with one click to disown it.
type Ownership map[string]*Owned

type Owned struct {
	Kind       string    `json:"kind"`  // channel | repo
	Score      float64   `json:"score"` // 0-1
	Why        []string  `json:"why"`   // in the reader's terms, one line each
	LastActive time.Time `json:"lastActive"`
	Pinned     bool      `json:"pinned"` // the reader said so; never decays
	ComputedAt time.Time `json:"computedAt"`
}

// Owns reports whether a place is the reader's.
func (o Ownership) Owns(origin string) bool {
	if o == nil {
		return false
	}
	owned := o[normaliseOrigin(origin)]
	return owned != nil && (owned.Pinned || owned.Score >= ownedThreshold)
}

const ownedThreshold = 0.35
