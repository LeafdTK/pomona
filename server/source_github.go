package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func githubCollector() Collector {
	return Collector{
		ID: "github", Name: "GitHub", IconKey: "provider:github",
		Blurb: "Review requests, assigned issues, and notifications you're participating in.",
		Help:  "A token from github.com/settings/tokens with `repo` and `notifications` (read) scope.",
		Fields: []Field{
			{Key: "token", Label: "Personal access token", Type: "password", Placeholder: "ghp_…"},
		},
		Fetch: fetchGitHub,
	}
}

var issueRef = regexp.MustCompile(`repos/([^/]+/[^/]+)/issues/(\d+)`)

func fetchGitHub(ctx context.Context, settings map[string]string, w Window) ([]Item, []Event, error) {
	token := settings["token"]
	if token == "" {
		return nil, nil, fmt.Errorf("no token set")
	}

	get := func(path string, out any) error {
		req, err := newRequest(ctx, "GET", "https://api.github.com"+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		res, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, _ := readAll(res.Body)
		if res.StatusCode != 200 {
			return fmt.Errorf("github %d: %s", res.StatusCode, clip(string(body), 160))
		}
		return json.Unmarshal(body, out)
	}

	var me struct {
		Login string `json:"login"`
	}
	if err := get("/user", &me); err != nil {
		return nil, nil, err
	}

	type issue struct {
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		URL         string    `json:"url"`
		UpdatedAt   time.Time `json:"updated_at"`
		Draft       bool      `json:"draft"`
		PullRequest *struct{} `json:"pull_request"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}

	search := func(q string) ([]issue, error) {
		var out struct {
			Items []issue `json:"items"`
		}
		path := "/search/issues?sort=updated&per_page=20&q=" + url.QueryEscape(q)
		return out.Items, get(path, &out)
	}

	items := []Item{}
	add := func(kind string, issues []issue) {
		for _, is := range issues {
			ref := ""
			if m := issueRef.FindStringSubmatch(is.URL); m != nil {
				ref = fmt.Sprintf("%s#%s", m[1], m[2])
			}
			tags := []string{}
			if is.PullRequest != nil {
				tags = append(tags, "pr")
			}
			if is.Draft {
				tags = append(tags, "draft")
			}
			for _, l := range is.Labels {
				tags = append(tags, l.Name)
			}
			items = append(items, Item{
				Kind: kind, Title: clip(ref+" "+is.Title, 200), Body: clip(Readable(is.Body), 400),
				URL: is.HTMLURL, Time: is.UpdatedAt, Tags: tags, Origin: repoOf(is.HTMLURL),
			})
		}
	}

	// A failure on one query shouldn't lose the others.
	if r, err := search("is:open is:pr review-requested:" + me.Login + " archived:false"); err == nil {
		add("github.review_requested", r)
	}
	inspect(items, get)
	items = groupBumps(items)
	if r, err := search("is:open assignee:" + me.Login + " archived:false"); err == nil {
		add("github.assigned", r)
	}
	if r, err := search("is:open is:pr author:" + me.Login + " archived:false"); err == nil {
		add("github.my_open_pr", r)
	}

	var notifications []struct {
		Reason     string    `json:"reason"`
		UpdatedAt  time.Time `json:"updated_at"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Subject struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Type  string `json:"type"`
		} `json:"subject"`
	}
	since := w.Since
	if cur := w.Cursors.Get("github:notifications"); cur.Since.After(since) {
		since = cur.Since
	}
	if err := get("/notifications?participating=true&per_page=30&since="+since.Format(time.RFC3339), &notifications); err == nil {
		w.Cursors.Advance("github:notifications", "", w.Now, len(notifications) > 0)
		for _, n := range notifications {
			items = append(items, Item{
				Kind:  "github.notification",
				Title: clip(n.Repository.FullName+": "+n.Subject.Title, 200),
				Body:  "reason: " + n.Reason,
				URL:   webURL(n.Subject.URL),
				Time:  n.UpdatedAt,
				Tags:  []string{n.Subject.Type, n.Reason},
			})
		}
	}

	return newestFirst(items, 40), nil, nil
}

// The notifications API hands back API URLs; the web URL is close enough to be
// clickable.
func webURL(api string) string {
	if api == "" {
		return ""
	}
	s := regexp.MustCompile(`^https://api\.github\.com/repos/`).ReplaceAllString(api, "https://github.com/")
	return regexp.MustCompile(`/pulls/(\d+)$`).ReplaceAllString(s, "/pull/$1")
}

// A robot opening one pull request per dependency produces a dozen items that
// differ by a package name, and a brief cannot say twelve useful things about
// them. It can say one: how many there are, which repo, and which of them is
// worth opening first. Twelve near-identical signals also crowd out everything
// else, because the per-source cap counts them all.
var bumpTitle = regexp.MustCompile(`(?i)\b(bump|update|upgrade)\b.*\bfrom\b.*\bto\b`)

// A changelog carries a "Security" heading on about every other release, so
// the bare word means nothing: matching it flagged three of five bumps as
// security fixes when one had a CVE. Name the thing, or say nothing.
var realCVE = regexp.MustCompile(`(?i)\bcve-\d{4}-\d{4,}|\bghsa-[a-z0-9-]{10,}|\bvulnerabilit|\bsecurity (?:advisory|fix|patch|release)\b`)

// notable marks a bump that deserves naming inside the group: a fix for a
// named vulnerability, or a major version jump that will not merge quietly.
func notable(it Item) string {
	switch {
	case realCVE.MatchString(it.Body + " " + it.Title):
		return "security"
	case majorJump(it.Title):
		return "major"
	}
	return ""
}

var versionPair = regexp.MustCompile(`\bfrom (\d+)\.\S* to (\d+)\.`)

func majorJump(title string) bool {
	m := versionPair.FindStringSubmatch(title)
	return m != nil && m[1] != m[2]
}

// groupBumps folds a repo's dependency pull requests into one item, leaving
// everything else exactly as it was.
func groupBumps(items []Item) []Item {
	byRepo := map[string][]Item{}
	kept := []Item{}
	for _, it := range items {
		repo := repoOf(it.URL)
		if it.Kind != "github.review_requested" || repo == "" || !bumpTitle.MatchString(it.Title) {
			kept = append(kept, it)
			continue
		}
		// A critical or high advisory never joins the pile.
		if level := taggedSeverity(it); severe(level) {
			it.Tags = append(it.Tags, "security")
			it.Body = strings.ToUpper(level) + " severity advisory. " + it.Body
			kept = append(kept, it)
			continue
		}
		byRepo[repo] = append(byRepo[repo], it)
	}

	for repo, bumps := range byRepo {
		// One on its own is not a pile, so it stays itself.
		if len(bumps) < 2 {
			kept = append(kept, bumps...)
			continue
		}

		lines := []string{}
		newest := bumps[0].Time
		for _, b := range bumps {
			if b.Time.After(newest) {
				newest = b.Time
			}
			label := strings.TrimPrefix(b.Title, repo)
			switch notable(b) {
			case "security":
				lines = append(lines, "SECURITY: "+clip(label, 120))
			case "major":
				lines = append(lines, "major version: "+clip(label, 120))
			default:
				lines = append(lines, clip(label, 90))
			}
		}
		sort.SliceStable(lines, func(i, j int) bool {
			return rank(lines[i]) < rank(lines[j])
		})

		kept = append(kept, Item{
			Kind:   "github.review_requested",
			Title:  fmt.Sprintf("%s — %d dependency pull requests waiting on your review", repo, len(bumps)),
			Body:   clip(strings.Join(lines, " | "), 700),
			URL:    "https://github.com/" + repo + "/pulls",
			Time:   newest,
			Tags:   []string{"pr", "dependencies", "grouped"},
			Origin: repo,
		})
	}
	return kept
}

// rank puts the ones worth opening first at the front of the list.
func rank(line string) int {
	switch {
	case strings.HasPrefix(line, "SECURITY:"):
		return 0
	case strings.HasPrefix(line, "major version:"):
		return 1
	}
	return 2
}

// Matches with or without a trailing path, so a bare repo link works too.
var repoPath = regexp.MustCompile(`github\.com/([^/\s]+/[^/\s]+)`)

func repoOf(link string) string {
	if m := repoPath.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

// A critical advisory is not one of twelve chores. Folding it into "five
// dependency pull requests" buries the one thing that actually needed today,
// so the severe ones are pulled back out and stand on their own.
// Dependabot does not write "critical severity". It writes "CVSS v4 Critical
// (9.1)", and the GitHub advisory database writes "high severity", and older
// PRs write "Severity: moderate". Guessing at one of those and shipping it is
// how the split silently did nothing: every real body said CVSS and the regex
// was looking for the words I had imagined.
var severityWord = regexp.MustCompile(`(?i)\bcvss[^()\n]{0,12}\b(critical|high|moderate|medium|low)\b|\b(critical|high|moderate|medium|low)[\s-]+(?:severity|risk)\b|\bseverity[:\s]+(critical|high|moderate|medium|low)\b`)

func severityOf(body string) string {
	m := severityWord.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	found := ""
	for _, group := range m[1:] {
		if group != "" {
			found = strings.ToLower(group)
			break
		}
	}
	if found == "medium" {
		return "moderate"
	}
	return found
}

// taggedSeverity reads the level inspect worked out from the whole body,
// falling back to whatever the clipped copy happens to say.
func taggedSeverity(it Item) string {
	for _, tag := range it.Tags {
		switch tag {
		case "critical", "high", "moderate", "low":
			return tag
		}
	}
	return severityOf(it.Body)
}

// severe reports whether an advisory is bad enough to stand on its own.
func severe(level string) bool { return level == "critical" || level == "high" }

// ── What state a pull request is actually in ────────────
//
// "Three PRs want your review" is a chore. "Three PRs want your review, checks
// are green, they need your sign-off" is ten seconds of work you can decide to
// do. The difference is entirely in whether anyone bothered to look, and the
// search endpoint does not say: it returns issues, and an issue knows nothing
// about its own checks.

const maxInspected = 16

var pullRef = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// inspect fills in how each pull request is doing. Only the ones that could
// become a to-do are looked at, and a failure on any of them leaves that item
// exactly as it was.
func inspect(items []Item, get func(string, any) error) {
	var wg sync.WaitGroup
	looked := 0

	for i := range items {
		if items[i].Kind != "github.review_requested" {
			continue
		}
		m := pullRef.FindStringSubmatch(items[i].URL)
		if m == nil {
			continue
		}
		if looked++; looked > maxInspected {
			break
		}

		wg.Add(1)
		go func(it *Item, owner, repo, number string) {
			defer wg.Done()
			state, level := pullState(get, owner, repo, number)
			if level != "" {
				it.Tags = append(it.Tags, level)
			}
			if state != "" {
				it.Body = state + " " + it.Body
			}
		}(&items[i], m[1], m[2], m[3])
	}
	wg.Wait()
}

// pullState is one sentence about whether this thing is ready for you, plus
// the advisory level if it is a security fix. The severity lives thousands of
// characters into the body, well past where the item's copy was clipped, so it
// can only be read here where the whole thing is in hand.
func pullState(get func(string, any) error, owner, repo, number string) (string, string) {
	var pr struct {
		Draft          bool   `json:"draft"`
		Body           string `json:"body"`
		MergeableState string `json:"mergeable_state"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if get("/repos/"+owner+"/"+repo+"/pulls/"+number, &pr) != nil {
		return "", ""
	}
	level := severityOf(pr.Body)
	if pr.Draft {
		return "Still a draft.", level
	}

	checks := checkRuns(get, owner, repo, pr.Head.SHA)

	// mergeable_state is GitHub's own summary and it is the useful part:
	// "blocked" is nearly always "waiting on a review", which is the whole
	// reason this landed in front of you.
	switch pr.MergeableState {
	case "dirty":
		return "Conflicts with the base branch." + checks, level
	case "behind":
		return "Behind the base branch and needs updating." + checks, level
	case "blocked":
		if checks == " Checks are green." {
			return "Checks are green: it is waiting on your review.", level
		}
		return "Waiting on a review." + checks, level
	case "clean":
		return "Green and mergeable." + checks, level
	case "unstable":
		return "Mergeable, but not every check passed." + checks, level
	}
	return strings.TrimSpace(checks), level
}

// checkRuns turns the check runs for a commit into a short clause, or nothing
// if the repository does not use them.
func checkRuns(get func(string, any) error, owner, repo, sha string) string {
	if sha == "" {
		return ""
	}
	var out struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if get("/repos/"+owner+"/"+repo+"/commits/"+sha+"/check-runs?per_page=100", &out) != nil {
		return ""
	}
	if out.Total == 0 {
		return ""
	}

	failed, running := 0, 0
	for _, run := range out.Runs {
		switch {
		case run.Status != "completed":
			running++
		case run.Conclusion == "failure", run.Conclusion == "timed_out",
			run.Conclusion == "cancelled", run.Conclusion == "action_required":
			failed++
		}
	}
	switch {
	case failed > 0:
		return fmt.Sprintf(" %d of %d checks failing.", failed, out.Total)
	case running > 0:
		return fmt.Sprintf(" %d checks still running.", running)
	}
	return " Checks are green."
}
