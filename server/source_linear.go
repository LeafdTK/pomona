package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const linearQuery = `
query BriefIssues($since: DateTimeOrDuration!) {
  assigned: issues(first: 25, filter: { assignee: { isMe: { eq: true } },
    state: { type: { nin: ["completed", "canceled"] } } }, orderBy: updatedAt) {
    nodes { identifier title description url updatedAt priorityLabel state { name } team { key } }
  }
  recent: issues(first: 20, filter: { subscribers: { isMe: { eq: true } },
    updatedAt: { gt: $since } }, orderBy: updatedAt) {
    nodes { identifier title description url updatedAt state { name } team { key } }
  }
}`

func linearCollector() Collector {
	return Collector{
		ID: "linear", Name: "Linear", IconKey: "provider:linear",
		Blurb:  "Issues assigned to you and anything you follow that moved.",
		Help:   "Linear → Settings → Security & access → Personal API keys.",
		Fields: []Field{{Key: "apiKey", Label: "API key", Type: "password", Placeholder: "lin_api_…"}},
		Fetch:  fetchLinear,
	}
}

func fetchLinear(ctx context.Context, settings map[string]string, w Window) ([]Item, []Event, error) {
	key := settings["apiKey"]
	if key == "" {
		return nil, nil, fmt.Errorf("no API key set")
	}

	payload, err := json.Marshal(map[string]any{
		"query":     linearQuery,
		"variables": map[string]any{"since": linearSince(w).Format("2006-01-02T15:04:05Z07:00")},
	})
	if err != nil {
		return nil, nil, err
	}

	req, err := newRequest(ctx, "POST", "https://api.linear.app/graphql", payload)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", key)
	req.Header.Set("Content-Type", "application/json")

	res, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	body, _ := readAll(res.Body)

	type node struct {
		Identifier    string                `json:"identifier"`
		Title         string                `json:"title"`
		Description   string                `json:"description"`
		URL           string                `json:"url"`
		UpdatedAt     string                `json:"updatedAt"`
		PriorityLabel string                `json:"priorityLabel"`
		State         struct{ Name string } `json:"state"`
		Team          struct{ Key string }  `json:"team"`
	}
	var out struct {
		Data struct {
			Assigned struct{ Nodes []node } `json:"assigned"`
			Recent   struct{ Nodes []node } `json:"recent"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, nil, fmt.Errorf("linear: %s", clip(string(body), 160))
	}
	if len(out.Errors) > 0 {
		return nil, nil, fmt.Errorf("linear: %s", out.Errors[0].Message)
	}

	items := []Item{}
	add := func(kind string, nodes []node) {
		for _, n := range nodes {
			at, _ := time.Parse(time.RFC3339, n.UpdatedAt)
			items = append(items, Item{
				Kind: kind, Title: n.Identifier + " " + n.Title, Body: clip(n.Description, 400),
				URL: n.URL, Time: at, Tags: []string{n.Team.Key, n.State.Name, n.PriorityLabel},
			})
		}
	}
	add("linear.assigned", out.Data.Assigned.Nodes)
	add("linear.updated", out.Data.Recent.Nodes)
	return newestFirst(items, 30), nil, nil
}

// linearSince is the window start, or the last read if that is later.
func linearSince(w Window) time.Time {
	if cur := w.Cursors.Get("linear"); cur.Since.After(w.Since) {
		return cur.Since
	}
	return w.Since
}
