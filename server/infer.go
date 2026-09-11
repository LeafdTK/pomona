package main

import (
	"context"
	"sync"
	"time"
)

// Setup that asks for nothing.
//
// The moment a source is connected it can say who the reader is: Slack has
// their name, title and timezone, GitHub their name and bio. So the profile is
// filled in the moment a token arrives, not when they find the form. Only
// blanks are filled, and what was filled by inference is remembered as such,
// so a later run never overwrites something the reader typed.

// InferProfile fills what the connected sources can say and the reader has
// not. Safe to run any number of times.
func InferProfile(ctx context.Context, u *UserStore) error {
	cfg := u.Config()
	guess := GuessProfile(ctx, cfg)
	changed := false

	if cfg.Profile.Inferred == nil {
		cfg.Profile.Inferred = map[string]bool{}
	}
	fill := func(field string, have *string, value string) {
		// Fill a blank, or replace what an earlier inference put there; never
		// touch what a person typed.
		if value == "" || (*have != "" && !cfg.Profile.Inferred[field]) {
			return
		}
		if *have != value {
			*have = value
			changed = true
		}
		cfg.Profile.Inferred[field] = true
	}
	fill("name", &cfg.Profile.Name, guess.Name)
	fill("role", &cfg.Profile.Role, guess.Role)
	fill("timezone", &cfg.Profile.Timezone, guess.Timezone)

	// The schedule follows the timezone: 07:30 wherever they are, unless they
	// have set a time themselves.
	if !cfg.Schedule.Set && cfg.Schedule.Time == "" {
		cfg.Schedule.Time = "07:30"
		changed = true
	}

	if !changed {
		return nil
	}
	return u.SetConfig(cfg)
}

// tokensChanged says whether any source gained or changed a token between
// two configs, which is the moment to go and ask who the reader is.
func tokensChanged(before, after *Config) bool {
	for id, next := range after.Sources {
		prev := before.Sources[id]
		if next["token"] != "" && (prev == nil || prev["token"] != next["token"]) {
			return true
		}
	}
	return false
}

// inferSoon runs the inference off the request, so connecting a source
// answers at once and the profile fills in a moment later. One at a time
// per account: a loop of config writes must not become a loop of network
// calls.
var inferring sync.Map

func inferSoon(u *UserStore) {
	if _, busy := inferring.LoadOrStore(u.ID(), true); busy {
		return
	}
	go func() {
		defer inferring.Delete(u.ID())
		ctx, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		_ = InferProfile(ctx, u)
	}()
}
