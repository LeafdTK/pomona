package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Where ownership comes from.
//
// Slack: the rooms the reader speaks in, and made. The store's ChannelMeta
// carries MyPosts, Posts, LastSpoke and Creator, filled by every sweep and by
// the from:@me search, and decayed weekly so a project they left fades.
//
// GitHub: the repositories they push to and merge into. Two calls a morning:
// the repos they can push to, sorted by last push, and their merged pull
// requests in the last month.
//
// Names bind the two. "#orchard-support" and "hackclub/orchard" share a word,
// and a reader who owns one is very likely to own the other. That is the Dia
// case: knowing a support channel is theirs because the repo is.

// evidence is the raw material for one place before it is scored.
type evidence struct {
	kind      string
	myPosts   int
	posts     int
	creator   bool
	lastSpoke time.Time
	pushedAt  time.Time
	merged    int
	pinned    bool     // the reader said so
	related   []string // places it shares a name with
}

// InferOwnership recomputes the map from everything known this morning. It
// is cheap, needs no model, and runs every time, so a channel first spoken in
// yesterday is owned today.
func InferOwnership(ctx context.Context, cfg *Config, sig *SignalStore, prev Ownership, now time.Time) Ownership {
	facts := map[string]*evidence{}
	at := func(origin, kind string) *evidence {
		key := normaliseOrigin(origin)
		if facts[key] == nil {
			facts[key] = &evidence{kind: kind}
		}
		return facts[key]
	}

	// Slack rooms, from what the sweeps recorded.
	if sig != nil {
		for _, m := range sig.Channels {
			if m.Name == "" {
				continue
			}
			e := at("#"+m.Name, "channel")
			e.myPosts, e.posts, e.lastSpoke = m.MyPosts, m.Posts, m.LastSpoke
			e.creator = m.Creator != "" && sig.me != "" && m.Creator == sig.me
		}
	}

	// GitHub repos, from two calls.
	if s := cfg.Sources["github"]; s["enabled"] == "true" && s["token"] != "" {
		for repo, pushed := range githubRepos(ctx, s["token"]) {
			e := at(repo, "repo")
			if pushed.After(e.pushedAt) {
				e.pushedAt = pushed
			}
		}
		for repo, merged := range githubMerged(ctx, s["token"], now) {
			at(repo, "repo").merged += merged
		}
	}

	// What the reader pinned joins the evidence now, before names are
	// matched, so a pinned repository lifts its rooms the same way an
	// inferred one does.
	for _, place := range cfg.Owns {
		key := normaliseOrigin(place)
		if key == "" {
			continue
		}
		kind := "channel"
		if strings.Contains(key, "/") {
			kind = "repo"
		}
		at(key, kind).pinned = true
	}

	// Names that bind a room to a repo.
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	for _, a := range keys {
		for _, b := range keys {
			if a != b && facts[a].kind != facts[b].kind && shareAWord(a, b) {
				facts[a].related = append(facts[a].related, b)
			}
		}
	}

	own := Ownership{}
	for key, e := range facts {
		score, why := scoreOwnership(e, now)
		// A related place that is itself owned makes this one owned too. This
		// is the correlation the reader loved: #orchard-support is theirs
		// because hackclub/orchard is, whether or not they have ever posted
		// there. So the lift clears the threshold on its own.
		for _, r := range e.related {
			if rs, _ := scoreOwnership(facts[r], now); rs >= ownedThreshold {
				score += ownedThreshold
				why = append(why, "shares a name with "+r+", which you own")
				break
			}
		}
		if score <= 0 {
			continue
		}
		if score > 1 {
			score = 1
		}
		last := e.lastSpoke
		if e.pushedAt.After(last) {
			last = e.pushedAt
		}
		own[key] = &Owned{Kind: e.kind, Score: score, Why: why, LastActive: last, ComputedAt: now}
	}

	// What the reader said, over everything above. A pin never decays; a
	// disown is absolute.
	for _, place := range cfg.Owns {
		key := normaliseOrigin(place)
		if key == "" {
			continue
		}
		kind := "channel"
		if strings.Contains(key, "/") {
			kind = "repo"
		}
		own[key] = &Owned{Kind: kind, Score: 1, Pinned: true, Why: []string{"you said so"}, ComputedAt: now, LastActive: now}
	}
	for _, place := range cfg.Disowns {
		delete(own, normaliseOrigin(place))
	}
	// Pins from a previous run survive a Config that no longer lists them
	// only if Config still lists them: the settings page is the source of
	// truth, so nothing else carries them over.
	_ = prev
	return own
}

// scoreOwnership turns evidence into a number and the reasons for it, in the
// reader's terms. The reasons matter as much as the number: an inference
// nobody can read is an inference nobody can correct.
func scoreOwnership(e *evidence, now time.Time) (float64, []string) {
	if e == nil {
		return 0, nil
	}
	if e.pinned {
		return 1, []string{"you said so"}
	}
	score := 0.0
	why := []string{}

	if e.creator {
		score += 0.35
		why = append(why, "you made this channel")
	}
	if e.posts >= 4 && e.myPosts > 0 {
		share := float64(e.myPosts) / float64(e.posts)
		switch {
		case share >= 0.4:
			score += 0.5
			why = append(why, fmt.Sprintf("you wrote %d of the last %d posts", e.myPosts, e.posts))
		case share >= 0.2:
			score += 0.3
			why = append(why, fmt.Sprintf("you wrote %d of the last %d posts", e.myPosts, e.posts))
		case share >= 0.1:
			score += 0.15
		}
	} else if e.myPosts >= 3 {
		score += 0.3
		why = append(why, fmt.Sprintf("you have posted here %d times lately", e.myPosts))
	}
	if !e.lastSpoke.IsZero() && now.Sub(e.lastSpoke) < 7*24*time.Hour {
		score += 0.15
		why = append(why, "you spoke here this week")
	}

	if e.merged >= 5 {
		score += 0.6
		why = append(why, fmt.Sprintf("you merged %d pull requests here in the last month", e.merged))
	} else if e.merged > 0 {
		score += 0.3
		why = append(why, fmt.Sprintf("you merged %d pull request%s here in the last month", e.merged, plural2(e.merged)))
	}
	if !e.pushedAt.IsZero() && now.Sub(e.pushedAt) < 14*24*time.Hour {
		score += 0.25
		why = append(why, "you pushed here in the last two weeks")
	}
	return score, why
}

// shareAWord says whether two places have a meaningful name in common:
// "#orchard-support" and "hackclub/orchard" do; "#general" and anything
// does not, because "general" is on the list of words that name nothing.
func shareAWord(a, b string) bool {
	for _, wa := range nameWords(a) {
		for _, wb := range nameWords(b) {
			if wa == wb {
				return true
			}
		}
	}
	return false
}

var dullWords = map[string]bool{
	"general": true, "random": true, "help": true, "support": true, "dev": true,
	"team": true, "chat": true, "announcements": true, "the": true, "and": true,
	"hackclub": true, "hq": true, "main": true, "app": true, "api": true, "www": true,
}

func nameWords(place string) []string {
	place = strings.ToLower(strings.TrimPrefix(place, "#"))
	// The owner half of "owner/repo" is not a project name.
	if i := strings.Index(place, "/"); i >= 0 {
		place = place[i+1:]
	}
	out := []string{}
	for _, w := range strings.FieldsFunc(place, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' }) {
		if len(w) >= 3 && !dullWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// Ranked lists what is owned, strongest first, for the prompt and the page.
func (o Ownership) Ranked(limit int) []string {
	keys := make([]string, 0, len(o))
	for k, v := range o {
		if v.Pinned || v.Score >= ownedThreshold {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := o[keys[i]], o[keys[j]]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return keys[i] < keys[j]
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

// ── GitHub evidence ─────────────────────────────────────

var githubBase = "https://api.github.com"

func githubGet(ctx context.Context, token, path string, out any) error {
	req, err := newRequest(ctx, "GET", githubBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := readAll(res.Body)
	if res.StatusCode != 200 {
		return fmt.Errorf("github %d", res.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// githubRepos is every repository the reader can push to, with when it was
// last pushed to. One call.
func githubRepos(ctx context.Context, token string) map[string]time.Time {
	var repos []struct {
		FullName string    `json:"full_name"`
		PushedAt time.Time `json:"pushed_at"`
		Archived bool      `json:"archived"`
	}
	out := map[string]time.Time{}
	// organization_member is what makes hackclub/* count: without it, every
	// repository the reader reaches through a team is invisible, which is
	// most of the ones they actually work in.
	if githubGet(ctx, token, "/user/repos?affiliation=owner,collaborator,organization_member&sort=pushed&per_page=100", &repos) != nil {
		return out
	}
	for _, r := range repos {
		if !r.Archived && r.FullName != "" {
			out[strings.ToLower(r.FullName)] = r.PushedAt
		}
	}
	return out
}

// githubMerged counts the reader's merged pull requests per repository over
// the last month. One call.
func githubMerged(ctx context.Context, token string, now time.Time) map[string]int {
	var me struct {
		Login string `json:"login"`
	}
	out := map[string]int{}
	if githubGet(ctx, token, "/user", &me) != nil || me.Login == "" {
		return out
	}
	since := now.AddDate(0, -1, 0).Format("2006-01-02")
	q := url.QueryEscape("is:pr is:merged author:" + me.Login + " merged:>" + since)
	var found struct {
		Items []struct {
			URL string `json:"html_url"`
		} `json:"items"`
	}
	if githubGet(ctx, token, "/search/issues?per_page=100&q="+q, &found) != nil {
		return out
	}
	for _, it := range found.Items {
		if repo := repoOf(it.URL); repo != "" {
			out[strings.ToLower(repo)]++
		}
	}
	return out
}

// githubReleases is what shipped from one repository lately. One call, and
// only for repositories the reader owns.
func githubReleases(ctx context.Context, token, repo string) []Release {
	var rels []struct {
		Tag         string    `json:"tag_name"`
		Name        string    `json:"name"`
		URL         string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
	}
	out := []Release{}
	if githubGet(ctx, token, "/repos/"+repo+"/releases?per_page=5", &rels) != nil {
		return out
	}
	for _, r := range rels {
		if r.Draft || r.Tag == "" {
			continue
		}
		out = append(out, Release{Repo: repo, Tag: r.Tag, Name: r.Name, URL: r.URL, At: r.PublishedAt})
	}
	return out
}
