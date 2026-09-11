package main

import "testing"

// A real dependabot body, shortened. Twelve of these filled two thirds of one
// morning's prompt, almost entirely with markup.
const dependabot = `Bumps [js-yaml](https://github.com/nodeca/js-yaml) from 4.3.1 to 5.4.1.
<details>
<summary>Changelog</summary>
<p><em>Sourced from <a href="https://github.com/nodeca/js-yaml/blob/master/CHANGELOG.md">js-yaml's changelog</a>.</em></p>
<blockquote>
<h2>[5.4.1] - 2026-08-26</h2>
<h3>Changed</h3>
<ul>
<li>Hard-limit merge sequence size to 100.</li>
</ul>
</blockquote>
</details>
<br />

[![Dependabot compatibility score](https://dependabot.com/badges/x)](https://docs.github.com/y)

Dependabot will resolve any conflicts with this PR as long as you don't alter it yourself.

Dependabot commands and options
You can trigger Dependabot actions by commenting on this PR.`

func TestReadableKeepsTheFactsAndDropsTheMarkup(t *testing.T) {
	got := Readable(dependabot)

	for _, want := range []string{
		"Bumps js-yaml from 4.3.1 to 5.4.1",
		"Hard-limit merge sequence size to 100",
		"5.4.1",
	} {
		if !contains(got, want) {
			t.Errorf("lost %q, got %q", want, got)
		}
	}
	for _, gone := range []string{
		"<details>", "<blockquote>", "<h2>", "href=", "https://github.com/nodeca",
		"compatibility score", "Dependabot will resolve", "Dependabot commands",
	} {
		if contains(got, gone) {
			t.Errorf("kept %q, got %q", gone, got)
		}
	}
	if len(got) > len(dependabot)/2 {
		t.Errorf("only got it down to %d chars from %d", len(got), len(dependabot))
	}
}

func TestReadableLeavesPlainProseAlone(t *testing.T) {
	plain := "The auth flow breaks when the token expires mid-request. Repro in the issue."
	if got := Readable(plain); got != plain {
		t.Errorf("got %q, want it untouched", got)
	}
}

func TestReadableUnescapesEntities(t *testing.T) {
	if got := Readable("a &amp; b &lt;tag&gt; &quot;q&quot;"); got != `a & b <tag> "q"` {
		t.Errorf("got %q", got)
	}
}

func TestReadableSurvivesJunk(t *testing.T) {
	for _, in := range []string{"", "<", "<<<>>>", "[unclosed](", "<a href=", "![img]("} {
		Readable(in) // must not panic
	}
}

// Markers are only markers at the start of a line. A naive strip turned
// "a > b" into "a b" and "8 - 3" into "8 3".
func TestReadableKeepsMidSentencePunctuation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"latency went 40ms -> 12ms", "latency went 40ms -> 12ms"},
		{"upgrade 4.3.1 - 5.4.1 is breaking", "upgrade 4.3.1 - 5.4.1 is breaking"},
		{"if a > b then bail", "if a > b then bail"},
		{"- first\n- second", "first second"},
		{"> quoted line", "quoted line"},
		{"1. step one\n2. step two", "step one step two"},
	}
	for _, c := range cases {
		if got := Readable(c.in); got != c.want {
			t.Errorf("Readable(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
