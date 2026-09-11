package main

import "testing"

// The plate is a wide band filled from the bottom, so a tall work shows up as
// a strip of its own lower edge: for a manuscript page, the blank margin.
func TestLandscapeNeedsEvidence(t *testing.T) {
	cases := []struct {
		name string
		img  plateImage
		want bool
	}{
		{"wide", plateImage{Width: "1600", Height: "1000"}, true},
		{"just wide enough", plateImage{Width: "1150", Height: "1000"}, true},
		{"square", plateImage{Width: "1000", Height: "1000"}, false},
		{"tall", plateImage{Width: "1000", Height: "1600"}, false},
		{"no dimensions", plateImage{URL: "x"}, false},
		{"junk dimensions", plateImage{Width: "wide", Height: "tall"}, false},
		{"zero height", plateImage{Width: "1000", Height: "0"}, false},
	}
	for _, c := range cases {
		if got := landscape(c.img); got != c.want {
			t.Errorf("%s: landscape() = %v, want %v", c.name, got, c.want)
		}
	}
}
