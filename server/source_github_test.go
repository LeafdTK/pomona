package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func bump(repo, title, body string, min int) Item {
	return Item{
		Kind: "github.review_requested", Title: title, Body: body,
		URL:  "https://github.com/" + repo + "/pull/1",
		Time: time.Date(2026, 9, 9, 2, min, 0, 0, time.UTC),
	}
}

// A robot opening one PR per dependency produced twelve near-identical signals
// that crowded everything else out of the per-source cap.
func TestGroupBumpsFoldsARepoIntoOneItem(t *testing.T) {
	items := []Item{
		bump("hackclub/orchard", "hackclub/orchard#28 chore(deps): bump nodemailer from 9.1.0 to 9.1.1", "routine", 1),
		bump("hackclub/orchard", "hackclub/orchard#29 chore(deps): bump multer from 2.2.0 to 2.3.0", "fixes CVE-2026-77078", 2),
		bump("hackclub/orchard", "hackclub/orchard#30 chore(deps): bump js-yaml from 4.3.1 to 5.4.1", "hard-limit merge sequence", 3),
		{Kind: "github.review_requested", Title: "hackclub/orchard#31 Rewrite the auth flow",
			URL: "https://github.com/hackclub/orchard/pull/31"},
		{Kind: "github.assigned", Title: "hackclub/site#4 chore(deps): bump react from 1.0.0 to 2.0.0",
			URL: "https://github.com/hackclub/site/pull/4"},
	}

	got := groupBumps(items)
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3 (one group, one real PR, one assigned)", len(got))
	}

	var group *Item
	for i := range got {
		if contains(got[i].Title, "dependency pull requests") {
			group = &got[i]
		}
	}
	if group == nil {
		t.Fatal("the bumps were not folded together")
	}
	if !contains(group.Title, "hackclub/orchard") || !contains(group.Title, "3 dependency") {
		t.Errorf("group title reads badly: %q", group.Title)
	}
	// The one worth opening first has to come first.
	if !contains(group.Body[:40], "SECURITY") {
		t.Errorf("the CVE was not led with: %q", group.Body)
	}
	if !contains(group.Body, "major version") {
		t.Errorf("the major jump was not called out: %q", group.Body)
	}
	if group.URL != "https://github.com/hackclub/orchard/pulls" {
		t.Errorf("group points at %q", group.URL)
	}

	// Everything that is not a dependency bump is left exactly alone.
	for _, it := range got {
		if contains(it.Title, "Rewrite the auth flow") && it.Kind != "github.review_requested" {
			t.Error("a real PR was altered")
		}
		if contains(it.Title, "hackclub/site#4") && !contains(it.Title, "react") {
			t.Error("an assigned issue was folded in")
		}
	}
}

// One bump on its own is not a pile.
func TestGroupBumpsLeavesASingleOneAlone(t *testing.T) {
	one := []Item{bump("hackclub/orchard", "orchard#28 bump nodemailer from 9.1.0 to 9.1.1", "", 1)}
	got := groupBumps(one)
	if len(got) != 1 || contains(got[0].Title, "dependency pull requests") {
		t.Errorf("a lone bump was grouped: %+v", got)
	}
}

func TestMajorJump(t *testing.T) {
	cases := map[string]bool{
		"bump js-yaml from 4.3.1 to 5.4.1":    true,
		"bump nodemailer from 9.1.0 to 9.1.1": false,
		"bump uuid from 13.0.0 to 14.0.0":     true,
		"bump multer from 2.2.0 to 2.3.0":     false,
		"something with no versions in it":    false,
	}
	for title, want := range cases {
		if got := majorJump(title); got != want {
			t.Errorf("majorJump(%q) = %v, want %v", title, got, want)
		}
	}
}

// Half of all changelogs have a "Security" heading. Matching the bare word
// flagged three of five bumps as security fixes when exactly one had a CVE.
func TestNotableNeedsANamedVulnerability(t *testing.T) {
	cases := []struct {
		name, title, body, want string
	}{
		{"a real CVE", "bump multer from 2.2.0 to 2.3.0", "fixes CVE-2026-77078", "security"},
		{"a GHSA id", "bump x from 1.0.1 to 1.0.2", "see GHSA-abcd-efgh-ijkl", "security"},
		{"the word vulnerability", "bump x from 1.0.1 to 1.0.2", "fixes a vulnerability in parsing", "security"},
		{"a changelog heading only", "bump js-yaml from 4.3.1 to 5.4.1", "Changed Hard-limit merge. Security Count empty mapping", "major"},
		{"a heading with no major jump", "bump nodemailer from 9.1.0 to 9.1.1", "Bug Fixes Security notes", ""},
		{"nothing special", "bump nodemailer from 9.1.0 to 9.1.1", "routine", ""},
	}
	for _, c := range cases {
		got := notable(Item{Title: c.title, Body: c.body})
		if got != c.want {
			t.Errorf("%s: notable() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSeverityOf(t *testing.T) {
	cases := map[string]string{
		// What dependabot actually writes, which is not what I first guessed.
		"This update fixes a CVSS v4 Low (2.0) security vulnerability in x": "low",
		"CVSS v4 Critical (9.1) issue":                                      "critical",
		"CVSS v3 High (7.5)":                                                "high",
		"This advisory is critical severity and affects astro":              "critical",
		"Severity: High":                                                    "high",
		"moderate severity vulnerability in tar":                            "moderate",
		"Medium severity issue":                                             "moderate",
		"low risk, cosmetic":                                                "low",
		"Bumps nodemailer from 9.1.0 to 9.1.1":                              "",
		"the severity of the outage was unclear":                            "",
	}
	for body, want := range cases {
		if got := severityOf(body); got != want {
			t.Errorf("severityOf(%q) = %q, want %q", body, got, want)
		}
	}
	if !severe("critical") || !severe("high") {
		t.Error("critical and high must stand alone")
	}
	if severe("moderate") || severe("low") || severe("") {
		t.Error("only critical and high stand alone")
	}
}

// The one that actually needed today must not be buried inside "five
// dependency pull requests".
func TestSevereAdvisoriesAreNotGrouped(t *testing.T) {
	items := []Item{
		bump("hackclub/orchard", "orchard#1 bump a from 1.0.0 to 1.0.1", "routine", 1),
		bump("hackclub/orchard", "orchard#2 bump b from 2.0.0 to 2.0.1", "routine", 2),
		bump("hackclub/orchard", "orchard#3 bump astro from 4.0.0 to 4.0.2", "This is a critical severity advisory", 3),
		bump("hackclub/orchard", "orchard#4 bump tar from 1.0.0 to 1.0.2", "moderate severity issue", 4),
	}

	got := groupBumps(items)

	var critical, group *Item
	for i := range got {
		switch {
		case contains(got[i].Title, "astro"):
			critical = &got[i]
		case contains(got[i].Title, "dependency pull requests"):
			group = &got[i]
		}
	}
	if critical == nil {
		t.Fatal("the critical advisory was folded into the group")
	}
	if group == nil {
		t.Fatal("the routine bumps were not grouped")
	}
	if contains(group.Body, "astro") {
		t.Error("the critical one is in the group as well")
	}
	if !contains(critical.Body, "CRITICAL severity") {
		t.Errorf("severity was not called out: %q", critical.Body)
	}

	// The moderate one is still a chore and stays in the pile.
	if !contains(group.Body, "tar") {
		t.Errorf("a moderate advisory was promoted: %q", group.Body)
	}

	// And it has to outrank a direct message, which is the whole point.
	if weight(*critical) <= weight(Item{Kind: "slack.dm"}) {
		t.Error("a critical advisory did not outrank a DM")
	}
}

// A stub GitHub: returns whatever was registered for a path, and errors for
// anything else, so a missing call shows up as a missing sentence.
func stubGitHub(t *testing.T, answers map[string]string) func(string, any) error {
	t.Helper()
	return func(path string, out any) error {
		body, found := answers[path]
		if !found {
			return errStub
		}
		return json.Unmarshal([]byte(body), out)
	}
}

var errStub = errors.New("not stubbed")

func TestPullStateSaysSomethingUseful(t *testing.T) {
	green := `{"total_count":3,"check_runs":[
	  {"status":"completed","conclusion":"success"},
	  {"status":"completed","conclusion":"success"},
	  {"status":"completed","conclusion":"success"}]}`
	broken := `{"total_count":3,"check_runs":[
	  {"status":"completed","conclusion":"failure"},
	  {"status":"completed","conclusion":"success"},
	  {"status":"completed","conclusion":"success"}]}`
	running := `{"total_count":2,"check_runs":[
	  {"status":"in_progress","conclusion":""},
	  {"status":"completed","conclusion":"success"}]}`

	cases := []struct {
		name, pr, checks, want string
	}{
		{
			name:   "waiting on you with green checks",
			pr:     `{"draft":false,"mergeable_state":"blocked","head":{"sha":"abc"}}`,
			checks: green,
			want:   "Checks are green: it is waiting on your review.",
		},
		{
			name:   "waiting on you with a failure",
			pr:     `{"draft":false,"mergeable_state":"blocked","head":{"sha":"abc"}}`,
			checks: broken,
			want:   "Waiting on a review. 1 of 3 checks failing.",
		},
		{
			name:   "conflicts",
			pr:     `{"draft":false,"mergeable_state":"dirty","head":{"sha":"abc"}}`,
			checks: green,
			want:   "Conflicts with the base branch. Checks are green.",
		},
		{
			name:   "still running",
			pr:     `{"draft":false,"mergeable_state":"clean","head":{"sha":"abc"}}`,
			checks: running,
			want:   "Green and mergeable. 1 checks still running.",
		},
		{
			name:   "a draft is never your problem yet",
			pr:     `{"draft":true,"mergeable_state":"blocked","head":{"sha":"abc"}}`,
			checks: green,
			want:   "Still a draft.",
		},
		{
			name:   "a repo with no checks at all",
			pr:     `{"draft":false,"mergeable_state":"clean","head":{"sha":"abc"}}`,
			checks: `{"total_count":0,"check_runs":[]}`,
			want:   "Green and mergeable.",
		},
	}

	for _, c := range cases {
		get := stubGitHub(t, map[string]string{
			"/repos/o/r/pulls/1":                             c.pr,
			"/repos/o/r/commits/abc/check-runs?per_page=100": c.checks,
		})
		got, _ := pullState(get, "o", "r", "1")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// GitHub not answering must leave the item exactly as it was, not blank it.
func TestPullStateStaysQuietWhenGitHubWillNotSay(t *testing.T) {
	silent := func(string, any) error { return errStub }
	if got, _ := pullState(silent, "o", "r", "1"); got != "" {
		t.Errorf("got %q, want nothing", got)
	}

	items := []Item{{Kind: "github.review_requested", Title: "t", Body: "the original body",
		URL: "https://github.com/o/r/pull/1"}}
	inspect(items, silent)
	if items[0].Body != "the original body" {
		t.Errorf("body was damaged: %q", items[0].Body)
	}
}

func TestInspectOnlyLooksAtReviewRequests(t *testing.T) {
	calls := 0
	get := func(path string, out any) error {
		calls++
		return errStub
	}
	inspect([]Item{
		{Kind: "github.notification", URL: "https://github.com/o/r/pull/1"},
		{Kind: "github.my_open_pr", URL: "https://github.com/o/r/pull/2"},
		{Kind: "github.review_requested", URL: "https://github.com/o/r/issues/3"},
	}, get)
	if calls != 0 {
		t.Errorf("made %d calls, want none", calls)
	}
}
