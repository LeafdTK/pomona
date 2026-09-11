package main

import (
	"html"
	"regexp"
	"strings"
)

// GitHub bodies arrive as raw markdown with HTML mixed in, and a dependabot PR
// is almost entirely the HTML: a dozen of them filled two thirds of one
// morning's prompt with <details>, <blockquote> and <a href="…"> and perhaps a
// sentence of fact between them. The model pays for every angle bracket and
// none of them mean anything to it.

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlAnchor  = regexp.MustCompile(`(?is)<a\b[^>]*>(.*?)</a>`)
	htmlTag     = regexp.MustCompile(`(?s)<[^<>]*>`)
	mdLink      = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)[^)]*\)`)
	mdImage     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdHeading   = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	// Anchored to the line: a dash or an angle bracket mid-sentence is
	// punctuation, and stripping those turned "a > b" into "a b".
	mdMarker = regexp.MustCompile(`(?m)^\s*(?:[*+-]|>|\d+\.)\s+`)
	mdRule   = regexp.MustCompile(`(?m)^\s*(?:-{3,}|={3,}|\*{3,})\s*$`)
	badge    = regexp.MustCompile(`(?i)\[!\[[^\]]*\]\([^)]*\)\]\([^)]*\)`)
)

// Dependabot signs off with instructions addressed to itself. They are the same
// on every PR, they are not about the change, and they are longer than most of
// the changes.
var boilerplate = []string{
	"Dependabot will resolve any conflicts",
	"You can trigger Dependabot actions",
	"Dependabot commands and options",
	"---\nUpdates ", // the per-package footer table
	"[//]: # (dependabot-",
	"<!-- rebase-check -->",
	"Signed-off-by: dependabot",
}

// Readable turns a forge's markup into the sentences underneath it. Link text
// survives, link targets do not: the model is told to copy urls from the item's
// own url field, so a hundred inline hrefs are pure cost.
func Readable(body string) string {
	body = htmlComment.ReplaceAllString(body, " ")
	body = badge.ReplaceAllString(body, " ")
	body = mdImage.ReplaceAllString(body, " ")

	// Cut the parts that are the same on every pull request before doing any
	// more work on them.
	for _, marker := range boilerplate {
		if at := strings.Index(body, marker); at >= 0 {
			body = body[:at]
		}
	}

	body = htmlAnchor.ReplaceAllString(body, "$1")
	body = htmlTag.ReplaceAllString(body, " ")
	body = mdLink.ReplaceAllString(body, "$1")
	body = mdHeading.ReplaceAllString(body, "")
	body = html.UnescapeString(body)

	// Bullets and rules carry no information once the structure is gone.
	body = mdRule.ReplaceAllString(body, " ")
	body = mdMarker.ReplaceAllString(body, "")
	body = strings.ReplaceAll(body, "`", "")

	return strings.Join(strings.Fields(body), " ")
}
