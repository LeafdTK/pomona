package main

import (
	"regexp"
	"testing"
)

// Every page links to the files beside it by bare name, because that is what
// works when Chrome serves the extension directly. Served by us, those names
// have to become paths, and the rule has to be general: naming the files one
// by one meant a new page served itself unstyled until somebody remembered.
func TestRelativeAssetRewriting(t *testing.T) {
	rewrite := func(html, base string) string {
		return relativeAsset.ReplaceAllString(html, `$1="/`+base+`$2"`)
	}

	cases := []struct{ in, want string }{
		{`<link rel="stylesheet" href="welcome.css">`, `<link rel="stylesheet" href="/src/welcome/welcome.css">`},
		{`<script type="module" src="welcome.js"></script>`, `<script type="module" src="/src/welcome/welcome.js"></script>`},

		// Left alone: already absolute, elsewhere, or not an asset at all.
		{`<link href="/src/brief/brief.css">`, `<link href="/src/brief/brief.css">`},
		{`<script src="https://cdn.test/x.js">`, `<script src="https://cdn.test/x.js">`},
		{`<a href="#top">top</a>`, `<a href="#top">top</a>`},
		{`<a href="/settings">settings</a>`, `<a href="/settings">settings</a>`},
		{`<img src="../icons/48.png">`, `<img src="../icons/48.png">`},
	}
	for _, c := range cases {
		if got := rewrite(c.in, "src/welcome/"); got != c.want {
			t.Errorf("rewrite(%q)\n  = %q\n  want %q", c.in, got, c.want)
		}
	}
}

// Guards the regex itself: it must never match a scheme or an absolute path.
func TestRelativeAssetNeverMatchesURLs(t *testing.T) {
	for _, in := range []string{
		`href="http://x.test/a.css"`,
		`href="//x.test/a.css"`,
		`src="/a.js"`,
		`href="data:text/css,body{}"`,
	} {
		if regexp.MustCompile(relativeAsset.String()).MatchString(in) {
			t.Errorf("matched %q, which it must leave alone", in)
		}
	}
}
