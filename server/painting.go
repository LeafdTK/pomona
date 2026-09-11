package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"strconv"
	"strings"
)

// A different plate every morning, from the Cleveland Museum of Art's open
// access collection: CC0, no key, and an image host that will serve a browser.
//
// The date alone chooses it: same day, same plate, every time it is asked.
// That is what lets the page show the day's painting while the brief is still
// being written, and land on that same painting when it finishes. Random per
// request would mean the plate that developed during the wait was not the one
// you ended up with.

var plateTerms = []string{
	"landscape", "still life flowers", "sea", "portrait", "garden", "night",
	"harbor", "interior", "river", "mountain", "orchard", "snow",
	"storm", "forest", "market", "bridge", "coast", "valley",
	"window", "reader", "musician", "horses", "fields", "dusk",
	"canal", "ruins", "moonlight", "hillside", "village", "sunrise",
}

type plateResults struct {
	Data []plateArt `json:"data"`
}

func searchPlates(ctx context.Context, term string, skip int) (plateResults, error) {
	var payload plateResults

	endpoint, _ := url.Parse("https://openaccess-api.clevelandart.org/api/artworks/")
	q := endpoint.Query()
	q.Set("q", term)
	q.Set("type", "Painting")
	q.Set("has_image", "1")
	q.Set("cc0", "1")
	q.Set("limit", "50")
	if skip > 0 {
		q.Set("skip", strconv.Itoa(skip))
	}
	q.Set("fields", "id,title,creators,creation_date,technique,images,url")
	endpoint.RawQuery = q.Encode()

	req, err := newRequest(ctx, "GET", endpoint.String(), nil)
	if err != nil {
		return payload, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return payload, err
	}
	defer res.Body.Close()
	body, _ := readAll(res.Body)

	err = json.Unmarshal(body, &payload)
	return payload, err
}

// plateArt is one artwork as the museum describes it.
type plateArt struct {
	Title        string `json:"title"`
	CreationDate string `json:"creation_date"`
	Technique    string `json:"technique"`
	URL          string `json:"url"`
	Creators     []struct {
		Description string `json:"description"`
	} `json:"creators"`
	Images struct {
		Web   plateImage `json:"web"`
		Print plateImage `json:"print"`
	} `json:"images"`
}

// plateImage is one rendition, with the shape we need to judge it by.
type plateImage struct {
	URL    string `json:"url"`
	Width  string `json:"width"`
	Height string `json:"height"`
}

// landscape reports whether an image is at least a little wider than it is
// tall. Unknown dimensions count as no, so a work only qualifies on evidence.
func landscape(img plateImage) bool {
	w, errW := strconv.Atoi(img.Width)
	h, errH := strconv.Atoi(img.Height)
	if errW != nil || errH != nil || h == 0 {
		return false
	}
	return float64(w)/float64(h) >= 1.15
}

func PickPainting(ctx context.Context, dayKey string) (*Painting, error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(dayKey))
	seed := int(h.Sum32())

	term := plateTerms[seed%len(plateTerms)]

	// Days that land on the same subject should still not land on the same
	// painting, so each one starts from a different point in the results. A
	// narrow subject can have fewer works than the offset, though, and asking
	// past the end returns nothing at all: without the retry the plate simply
	// vanishes on those mornings, which is how two days in six lost their
	// artwork entirely.
	payload, err := searchPlates(ctx, term, (seed/7)%40)
	if err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 {
		if payload, err = searchPlates(ctx, term, 0); err != nil {
			return nil, err
		}
	}

	usable := payload.Data[:0]
	for _, art := range payload.Data {
		if art.Images.Print.URL != "" || art.Images.Web.URL != "" {
			usable = append(usable, art)
		}
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("no public domain plate found")
	}

	// The plate is a wide band and the painting fills it from the bottom, so a
	// tall work arrives as a strip of its own lower edge: for a manuscript page
	// or a portrait that is the blank margin, and nothing else. Prefer the ones
	// shaped anything like the frame, and only fall back if none are.
	wide := []plateArt{}
	for _, art := range usable {
		if landscape(art.Images.Print) || landscape(art.Images.Web) {
			wide = append(wide, art)
		}
	}
	if len(wide) > 0 {
		usable = wide
	}

	art := usable[(seed/len(plateTerms))%len(usable)]
	image := art.Images.Print.URL
	if image == "" {
		image = art.Images.Web.URL
	}

	// "Jan Gossaert (Flemish, c. 1475/78-1532)" -> "Jan Gossaert"
	artist := ""
	if len(art.Creators) > 0 {
		artist = strings.TrimSpace(strings.SplitN(art.Creators[0].Description, " (", 2)[0])
	}

	parts := []string{}
	for _, p := range []string{art.Title, artist, art.CreationDate} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	caption := strings.Join(parts, ", ")
	if art.Technique != "" {
		caption += ". " + strings.ToLower(art.Technique)
	}

	return &Painting{Caption: caption, Image: image, Credit: "Cleveland Museum of Art", Link: art.URL}, nil
}
