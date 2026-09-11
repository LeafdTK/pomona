package main

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Asking somebody to type their own name into a box, when they have just handed
// over a Slack token that knows their name, their job title and their timezone,
// is a form pretending to be a conversation. Connect first, then say what was
// worked out and let them correct it. The setup that asks for the least is the
// one people finish.

// Guess is what the connected sources already say about their owner.
type Guess struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Timezone string   `json:"timezone"`
	From     []string `json:"from"` // which sources answered, for the page to name
}

// GuessProfile asks each connected source who it thinks you are. Sources are
// asked in parallel and none of them is allowed to fail the whole thing: a
// guess is a courtesy, and a broken one should leave empty boxes, not an error.
func GuessProfile(ctx context.Context, cfg *Config) Guess {
	// Every source is asked at once, and each reports its fields verbatim. The
	// merge below is the fallback: the real answer comes from handing all of it
	// to Claude, which is the only thing here that can tell a person's name
	// from a handle they picked in 2019.
	var (
		mu       sync.Mutex
		answers  = map[string]Guess{}
		gathered []facts
		wg       sync.WaitGroup
	)

	ask := func(source string, fn func() (facts, Guess)) {
		if s := cfg.Sources[strings.ToLower(source)]; s["enabled"] != "true" || s["token"] == "" {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, fallback := fn()
			mu.Lock()
			defer mu.Unlock()
			answers[source] = fallback
			if len(raw.Lines) > 0 {
				gathered = append(gathered, raw)
			}
		}()
	}

	ask("Slack", func() (facts, Guess) {
		token := cfg.Sources["slack"]["token"]
		name, role, tz := askSlack(ctx, token)
		return slackFacts(ctx, token), Guess{Name: name, Role: role, Timezone: tz}
	})
	ask("GitHub", func() (facts, Guess) {
		token := cfg.Sources["github"]["token"]
		name, role := askGitHub(ctx, token)
		return githubFacts(ctx, token), Guess{Name: name, Role: role}
	})
	wg.Wait()

	guess := mergeGuesses(answers)

	// Asked in a fixed order so the question is the same for the same profile.
	sort.Slice(gathered, func(i, j int) bool { return gathered[i].Source < gathered[j].Source })
	if name, role := askClaude(ctx, cfg, gathered); name != "" || role != "" {
		if name != "" {
			guess.Name = name
		}
		// An empty role from Claude is a real answer: it means none of the
		// fields honestly described anybody's work, and repeating "lif" back at
		// somebody is worse than leaving the box for them.
		guess.Role = role
	}
	return guess
}

// mergeGuesses folds the answers together in a fixed order of preference.
func mergeGuesses(answers map[string]Guess) Guess {
	guess := Guess{From: []string{}}
	for _, source := range []string{"Slack", "GitHub"} {
		got, asked := answers[source]
		if !asked {
			continue
		}
		if got.Name == "" && got.Role == "" && got.Timezone == "" {
			continue // it answered, but knew nothing
		}
		guess.From = append(guess.From, source)
		if guess.Name == "" {
			guess.Name = strings.TrimSpace(got.Name)
		}
		if guess.Role == "" {
			guess.Role = strings.TrimSpace(got.Role)
		}
		if guess.Timezone == "" {
			guess.Timezone = strings.TrimSpace(got.Timezone)
		}
	}
	return guess
}

// facts is what one source says, in its own words, with no interpretation. A
// display name, a handle and a job title are three different things and only a
// reader can tell which is which, so nothing here decides.
type facts struct {
	Source   string
	Lines    []string
	Timezone string
}

func askSlack(ctx context.Context, token string) (name, role, tz string) {
	client := &slackClient{ctx: ctx, token: token, granted: map[string]bool{}, nextAt: map[string]time.Time{}}

	var me struct {
		UserID string `json:"user_id"`
	}
	if client.call("auth.test", nil, &me) != nil || me.UserID == "" {
		return "", "", ""
	}

	var out struct {
		User struct {
			TZ      string `json:"tz"`
			Profile struct {
				RealName    string `json:"real_name"`
				DisplayName string `json:"display_name"`
				Title       string `json:"title"`
			} `json:"profile"`
		} `json:"user"`
	}
	if client.call("users.info", url.Values{"user": {me.UserID}}, &out) != nil {
		return "", "", ""
	}

	name = out.User.Profile.RealName
	if name == "" {
		name = out.User.Profile.DisplayName
	}
	return firstName(name), out.User.Profile.Title, out.User.TZ
}

// slackFacts reports what Slack holds without choosing between the fields.
func slackFacts(ctx context.Context, token string) facts {
	client := &slackClient{ctx: ctx, token: token, granted: map[string]bool{}, nextAt: map[string]time.Time{}}

	var me struct {
		UserID string `json:"user_id"`
		User   string `json:"user"`
		Team   string `json:"team"`
	}
	if client.call("auth.test", nil, &me) != nil || me.UserID == "" {
		return facts{}
	}

	var out struct {
		User struct {
			Name    string `json:"name"`
			TZ      string `json:"tz"`
			Profile struct {
				RealName    string `json:"real_name"`
				DisplayName string `json:"display_name"`
				Title       string `json:"title"`
			} `json:"profile"`
		} `json:"user"`
	}
	if client.call("users.info", url.Values{"user": {me.UserID}}, &out) != nil {
		return facts{}
	}

	f := facts{Source: "Slack", Timezone: out.User.TZ}
	for label, value := range map[string]string{
		"real name":    out.User.Profile.RealName,
		"display name": out.User.Profile.DisplayName,
		"username":     out.User.Name,
		"job title":    out.User.Profile.Title,
		"workspace":    me.Team,
	} {
		if strings.TrimSpace(value) != "" {
			f.Lines = append(f.Lines, label+": "+clip(value, 90))
		}
	}
	sort.Strings(f.Lines) // stable order, so the same profile asks the same question
	return f
}

// githubFacts does the same for a forge account.
func githubFacts(ctx context.Context, token string) facts {
	req, err := newRequest(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return facts{}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	res, err := httpClient.Do(req)
	if err != nil {
		return facts{}
	}
	defer res.Body.Close()
	body, _ := readAll(res.Body)

	var me struct {
		Login   string `json:"login"`
		Name    string `json:"name"`
		Bio     string `json:"bio"`
		Company string `json:"company"`
	}
	if json.Unmarshal(body, &me) != nil {
		return facts{}
	}

	f := facts{Source: "GitHub"}
	for label, value := range map[string]string{
		"username": me.Login,
		"name":     me.Name,
		"bio":      me.Bio,
		"company":  strings.TrimPrefix(me.Company, "@"),
	} {
		if strings.TrimSpace(value) != "" {
			f.Lines = append(f.Lines, label+": "+clip(value, 90))
		}
	}
	sort.Strings(f.Lines)
	return f
}

func askGitHub(ctx context.Context, token string) (name, role string) {
	req, err := newRequest(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	res, err := httpClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer res.Body.Close()
	body, _ := readAll(res.Body)

	var me struct {
		Name    string `json:"name"`
		Bio     string `json:"bio"`
		Company string `json:"company"`
	}
	if json.Unmarshal(body, &me) != nil {
		return "", ""
	}

	role = me.Bio
	if role == "" && me.Company != "" {
		role = strings.TrimPrefix(me.Company, "@")
	}
	return firstName(me.Name), clip(role, 90)
}

// firstName is what the brief actually says. It greets people the way a
// colleague would, and "Morning, Sebastian Sanchez Mota" is not that.
func firstName(full string) string {
	fields := strings.Fields(full)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ── Working out who somebody is ─────────────────────────
//
// A Slack profile has a real name, a display name and a job title, and any of
// them can be a joke, a handle or blank. GitHub has a login, a name and a bio.
// Rules that pick between them are guesses dressed as logic: "first word of
// real_name" gives Sebastian from one account and Leafd from another, and the
// job title field said "lif".
//
// So the raw fields go to Claude and it works out which is a person's name and
// which is a username. This is the one job in the whole server that is actually
// a judgement call, and there is a model right here.

const whoSystemPrompt = `You are told what a few accounts say about their owner.
Work out the person's name and what they do.

The name is what a colleague says out loud to greet them: a given name, not a
username, not a full legal name, not a handle. If the fields disagree, prefer
the one that reads like a name a person would answer to. "Leafd" is a handle;
"Sebastian Sanchez Mota" gives "Sebastian".

The role is a short description of their work, at most six words. Job title
fields are often blank, stale or a joke, and a bio is often not a role at all.
If nothing there honestly describes what somebody does, leave it empty rather
than repeating a joke back at them.

Reply with one JSON object and nothing else: {"name": "", "role": ""}. Use an
empty string where you cannot tell. Never invent either.`

func whoSchema() map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required":   []string{"name", "role"},
		"properties": map[string]any{"name": str, "role": str},
	}
}

// askClaude turns the raw account fields into a name and a role. It is only
// ever a nicety, so any failure leaves the caller with whatever it already had.
func askClaude(ctx context.Context, cfg *Config, gathered []facts) (name, role string) {
	if len(gathered) == 0 {
		return "", ""
	}
	var b strings.Builder
	for _, f := range gathered {
		b.WriteString(f.Source + ":\n")
		for _, line := range f.Lines {
			b.WriteString("  " + line + "\n")
		}
	}

	written, err := AskClaude(ctx, cfg, Ask{
		System: whoSystemPrompt, Prompt: b.String(), Schema: whoSchema(),
		Models: cfg.Small(), MaxTokens: 512, Purpose: "guess",
	})
	if err != nil {
		return "", ""
	}
	var out struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if json.Unmarshal(written.JSON, &out) != nil {
		return "", ""
	}
	return strings.TrimSpace(out.Name), strings.TrimSpace(clip(out.Role, 90))
}
