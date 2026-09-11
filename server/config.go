package main

import "time"

// Config is everything the user sets. It lives as JSON next to the briefs, so
// secrets stay on disk under the user's own permissions rather than inside a
// browser profile.
type Config struct {
	Profile struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Focus    string `json:"focus"`
		Timezone string `json:"timezone"`
		// Which of these were filled in by inference rather than typed, so a
		// later inference may replace them and a typed value is never touched.
		Inferred map[string]bool `json:"inferred,omitempty"`
	} `json:"profile"`

	Claude struct {
		// "subscription" runs the local claude CLI, which is covered by your
		// plan. "apikey" calls the Messages API and bills per token.
		Mode   string `json:"mode"`
		APIKey string `json:"apiKey"`
		// A long-lived Claude Code credential from `claude setup-token`, for
		// subscription mode on a server that is not the reader's own machine.
		// Each account brings its own; the server never shares a login.
		OAuthToken string `json:"oauthToken"`
		Model      string `json:"model"`
		Effort     string `json:"effort"`
		// The small model for triage, profile guessing and anything else that
		// is a few hundred tokens of judgement rather than a page of prose.
		TriageModel string `json:"triageModel"`
	} `json:"claude"`

	Schedule struct {
		Enabled      bool   `json:"enabled"`
		Time         string `json:"time"` // "07:00": when it should be ready by, in the reader's timezone
		WeekdaysOnly bool   `json:"weekdaysOnly"`
		Set          bool   `json:"set"` // the reader chose the time themselves
	} `json:"schedule"`

	// Places the reader has silenced: "#money-laundering", "hackclub/site".
	// Applied before anything is ranked or shown, so a muted room costs
	// nothing and cannot be argued back in.
	Mutes []string `json:"mutes"`

	// What the reader says they own, over and above what was inferred, and
	// what they say they do not. Both are places: "#orchard-support",
	// "hackclub/orchard".
	Owns    []string `json:"owns"`
	Disowns []string `json:"disowns"`

	Sources  map[string]map[string]string `json:"sources"`
	Custom   []CustomSource               `json:"custom"`
	Lookback int                          `json:"lookbackHours"`

	// How many past briefs to keep. They are summaries of other people's
	// messages, so hoarding them is the wrong default.
	KeepDays int `json:"keepDays"`
}

// CustomSource is the escape hatch: any URL returning JSON, RSS or text.
type CustomSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Method    string `json:"method"`
	Headers   string `json:"headers"`
	Body      string `json:"body"`
	ItemsPath string `json:"itemsPath"`
	Enabled   bool   `json:"enabled"`
}

// Location is the reader's timezone, or the process's if none has been
// inferred yet. The brief is about their day, and the schedule fires at their
// 07:30: a server in one place writing for a person in another used to get
// both wrong.
func (c *Config) Location() *time.Location {
	if c.Profile.Timezone != "" {
		if loc, err := time.LoadLocation(c.Profile.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// Now is the current moment as the reader experiences it.
func (c *Config) Now() time.Time { return time.Now().In(c.Location()) }

// Small is the chain for cheap asks: the configured small model, then Sonnet
// so a Haiku outage does not take the profile guess or triage down with it.
func (c *Config) Small() []string {
	model := c.Claude.TriageModel
	if model == "" {
		model = "claude-haiku-4-5"
	}
	if model == "claude-sonnet-5" {
		return []string{model}
	}
	return []string{model, "claude-sonnet-5"}
}

func defaultConfig() *Config {
	c := &Config{
		Sources:  map[string]map[string]string{},
		Custom:   []CustomSource{},
		Mutes:    []string{},
		Owns:     []string{},
		Disowns:  []string{},
		Lookback: 72,
		KeepDays: 7,
	}
	c.Claude.Mode = "subscription"
	c.Claude.Model = "claude-opus-5"
	c.Claude.Effort = "high"
	c.Claude.TriageModel = "claude-haiku-4-5"
	c.Schedule.Enabled = true
	c.Schedule.Time = "07:00" // ready by, in the reader's timezone
	return c
}
