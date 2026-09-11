package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// The escape hatch: any URL returning JSON, RSS/Atom, or plain text. This is
// how Pomona stays useful beyond the built-ins: a status page, a changelog, a
// Notion query, your own API.

func fetchCustom(ctx context.Context, src CustomSource) ([]Item, error) {
	if src.URL == "" {
		return nil, fmt.Errorf("no URL set")
	}
	method := src.Method
	if method == "" {
		method = "GET"
	}

	var body []byte
	if method != "GET" && method != "HEAD" && src.Body != "" {
		body = []byte(src.Body)
	}
	req, err := newRequest(ctx, method, src.URL, body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(src.Headers) != "" {
		headers := map[string]string{}
		if err := json.Unmarshal([]byte(src.Headers), &headers); err != nil {
			return nil, fmt.Errorf(`headers must be a JSON object, e.g. {"Authorization": "Bearer …"}`)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := readAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("%s returned %d", hostOf(src.URL), res.StatusCode)
	}

	name := src.Name
	if name == "" {
		name = "source"
	}

	var payload any
	if json.Unmarshal(raw, &payload) == nil {
		return fromJSON(payload, src, name), nil
	}
	if regexp.MustCompile(`(?i)<(rss|feed|item|entry)\b`).Match(raw) {
		return newestFirst(fromFeed(string(raw), name), 20), nil
	}
	return []Item{{Kind: "custom." + name, Title: name, Body: clip(stripTags(string(raw)), 1500), URL: src.URL}}, nil
}

func fromJSON(payload any, src CustomSource, name string) []Item {
	target := payload
	for _, key := range strings.Split(src.ItemsPath, ".") {
		if key == "" {
			continue
		}
		obj, ok := target.(map[string]any)
		if !ok {
			break
		}
		target = obj[key]
	}

	list, ok := target.([]any)
	if !ok {
		encoded, _ := json.Marshal(target)
		return []Item{{Kind: "custom." + name, Title: name, Body: clip(string(encoded), 1500), URL: src.URL}}
	}

	items := []Item{}
	for _, entry := range list {
		record, ok := entry.(map[string]any)
		if !ok {
			items = append(items, Item{Kind: "custom." + name, Title: clip(fmt.Sprint(entry), 140)})
			continue
		}
		items = append(items, Item{
			Kind:  "custom." + name,
			Title: firstString(record, "title", "name", "subject", "summary", "headline", "id"),
			Body:  clip(firstString(record, "body", "description", "text", "content", "summary", "message"), 500),
			URL:   firstString(record, "url", "html_url", "link", "permalink", "href"),
			Time:  parseLooseTime(firstString(record, "updated_at", "updatedAt", "created_at", "createdAt", "date", "published", "time")),
		})
	}
	return newestFirst(items, 20)
}

func firstString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := record[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func parseLooseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// A small RSS/Atom reader. Pulling in an XML library for five fields isn't
// worth it, and feeds in the wild are rarely well-formed anyway.
func fromFeed(xml, name string) []Item {
	entryRe := regexp.MustCompile(`(?s)<(item|entry)\b.*?</(item|entry)>`)
	items := []Item{}
	for _, block := range entryRe.FindAllString(xml, -1) {
		tag := func(names ...string) string {
			for _, n := range names {
				if m := regexp.MustCompile(`(?is)<` + n + `\b[^>]*>(.*?)</` + n + `>`).FindStringSubmatch(block); m != nil {
					return stripTags(m[1])
				}
				if m := regexp.MustCompile(`(?is)<` + n + `\b[^>]*href=["']([^"']+)["']`).FindStringSubmatch(block); m != nil {
					return m[1]
				}
			}
			return ""
		}
		items = append(items, Item{
			Kind: "custom." + name, Title: tag("title"), Body: clip(tag("description", "summary", "content"), 500),
			URL: tag("link", "id"), Time: parseLooseTime(tag("pubDate", "published", "updated")),
		})
	}
	return items
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = regexp.MustCompile(`(?s)<!\[CDATA\[(.*?)\]\]>`).ReplaceAllString(s, "$1")
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, " ")))
}
