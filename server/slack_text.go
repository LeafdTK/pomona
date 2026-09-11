package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Slack hands us machine text, not readable text. A message that reads
//
//	<!channel> Happy Tuesday, @U093FC28A82 has the deck: <https://docs.google.com/…|slides>
//
// has to reach the model as
//
//	@channel Happy Tuesday, @Rebeka has the deck: slides (https://docs.google.com/…)
//
// or the brief ends up quoting raw user ids at the reader, which is exactly
// what it was doing.

// slackDirectory resolves ids to names one at a time, on demand, and caches
// them. The obvious implementation is users.list, but that pages through every
// member of the workspace: on a 100k-person Slack the first page is an
// arbitrary 1000 people who are almost never the ones who wrote to you, and
// pulling the rest would mean downloading the whole staff directory to render
// a dozen names. Asking for the handful of ids actually mentioned is faster,
// and it means the server never holds a copy of the member list.
type slackDirectory struct {
	client *slackClient

	mu       sync.Mutex
	users    map[string]string
	channels map[string]string
	groups   map[string]string
	bots     map[string]bool
}

func newSlackDirectory(client *slackClient) *slackDirectory {
	return &slackDirectory{
		client:   client,
		users:    map[string]string{},
		channels: map[string]string{},
		groups:   map[string]string{},
		bots:     map[string]bool{},
	}
}

// lookup runs the cache-miss path for one id. Two workers can race to fetch the
// same id, which costs one extra call and is cheaper than holding the lock
// across the network.
func (d *slackDirectory) lookup(cache map[string]string, id, scope string, fetch func() string) string {
	d.mu.Lock()
	name, known := cache[id]
	d.mu.Unlock()
	if known {
		return name
	}

	name = ""
	if d.client.knowsScopes() && scope != "" && !d.client.has(scope) {
		// No scope for this, so cache the miss and stop asking.
	} else {
		name = fetch()
	}

	d.mu.Lock()
	cache[id] = name
	d.mu.Unlock()
	return name
}

// User is the display name for an id, or "" if it can't be resolved.
func (d *slackDirectory) User(id string) string {
	if id == "" {
		return ""
	}
	return d.lookup(d.users, id, "users:read", func() string {
		var out struct {
			User struct {
				Name    string `json:"name"`
				Profile struct {
					DisplayName string `json:"display_name"`
					RealName    string `json:"real_name"`
				} `json:"profile"`
			} `json:"user"`
		}
		if d.client.call("users.info", url.Values{"user": {id}}, &out) != nil {
			return ""
		}
		switch {
		case out.User.Profile.DisplayName != "":
			return out.User.Profile.DisplayName
		case out.User.Profile.RealName != "":
			return out.User.Profile.RealName
		}
		return out.User.Name
	})
}

// Channel is the #name for a channel id, or "" if it can't be resolved.
func (d *slackDirectory) Channel(id string) string {
	if id == "" {
		return ""
	}
	return d.lookup(d.channels, id, "", func() string {
		var out struct {
			Channel struct {
				Name string `json:"name"`
			} `json:"channel"`
		}
		if d.client.call("conversations.info", url.Values{"channel": {id}}, &out) != nil {
			return ""
		}
		return out.Channel.Name
	})
}

// Group is the handle for a user group id, or "" if it can't be resolved.
// Slack usually inlines the handle after a pipe, so this rarely runs.
func (d *slackDirectory) Group(id string) string {
	if id == "" {
		return ""
	}
	return d.lookup(d.groups, id, "usergroups:read", func() string {
		var out struct {
			Usergroups []struct {
				ID     string `json:"id"`
				Handle string `json:"handle"`
				Name   string `json:"name"`
			} `json:"usergroups"`
		}
		if d.client.call("usergroups.list", nil, &out) != nil {
			return ""
		}
		found := ""
		d.mu.Lock()
		for _, g := range out.Usergroups {
			label := g.Handle
			if label == "" {
				label = g.Name
			}
			d.groups[g.ID] = label // one call answers every group
			if g.ID == id {
				found = label
			}
		}
		d.mu.Unlock()
		return found
	})
}

// IsBot says whether an id belongs to an app rather than a person. Cached
// alongside the names; a miss is treated as a person, because "a person
// answered" is the safe way to be wrong about an unanswered question.
func (d *slackDirectory) IsBot(id string) bool {
	if id == "" || strings.HasPrefix(id, "B") {
		return true // Slack bot ids start with B; a blank author is an app
	}
	d.mu.Lock()
	known, seen := d.bots[id]
	d.mu.Unlock()
	if seen {
		return known
	}
	var out struct {
		User struct {
			IsBot bool `json:"is_bot"`
		} `json:"user"`
	}
	bot := d.client.call("users.info", url.Values{"user": {id}}, &out) == nil && out.User.IsBot
	d.mu.Lock()
	d.bots[id] = bot
	d.mu.Unlock()
	return bot
}

// Who names the author of a message. Bot posts carry a username and no id.
func (d *slackDirectory) Who(user, username string) string {
	if name := d.User(user); name != "" {
		return name
	}
	if username != "" {
		return username
	}
	if user != "" {
		return "someone"
	}
	return "someone"
}

// Slack wraps every entity in angle brackets: <@U1>, <#C1|general>, <!here>,
// <https://x|text>. Anything not in brackets is already plain text.
var slackEntity = regexp.MustCompile(`<([^<>]*)>`)

// Render turns Slack's wire markup into something a person, or a model writing
// for a person, can read.
func (d *slackDirectory) Render(text string) string {
	out := slackEntity.ReplaceAllStringFunc(text, func(match string) string {
		return d.entity(match[1 : len(match)-1])
	})
	return strings.TrimSpace(unescapeSlack(out))
}

func (d *slackDirectory) entity(body string) string {
	label := ""
	if pipe := strings.Index(body, "|"); pipe >= 0 {
		label, body = body[pipe+1:], body[:pipe]
	}

	switch {
	case strings.HasPrefix(body, "@"):
		if label != "" {
			return "@" + strings.TrimPrefix(label, "@")
		}
		if name := d.User(body[1:]); name != "" {
			return "@" + name
		}
		return "@someone"

	case strings.HasPrefix(body, "#"):
		if label != "" {
			return "#" + strings.TrimPrefix(label, "#")
		}
		if name := d.Channel(body[1:]); name != "" {
			return "#" + name
		}
		return "a channel"

	case strings.HasPrefix(body, "!subteam^"):
		if label != "" {
			return "@" + strings.TrimPrefix(label, "@")
		}
		if name := d.Group(strings.TrimPrefix(body, "!subteam^")); name != "" {
			return "@" + name
		}
		return "@group"

	case body == "!here", body == "!channel", body == "!everyone":
		return "@" + body[1:]

	case strings.HasPrefix(body, "!date^"):
		// <!date^1699…^{date_short}|Nov 5, 2023>: the fallback is the readable part.
		if label != "" {
			return label
		}
		return ""

	case strings.HasPrefix(body, "!"):
		if label != "" {
			return label
		}
		return ""
	}

	// A link. Keep the words and the target, because the model is asked to
	// copy urls verbatim and a bare label would strip it of one.
	if label != "" && label != body {
		return label + " (" + body + ")"
	}
	return body
}

var slackEscapes = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")

func unescapeSlack(s string) string { return slackEscapes.Replace(s) }

// meaningful reports whether a rendered message says anything at all. A bare
// group ping with no words is not a signal, it is a notification, and putting
// it in front of the model only invites it to write a paragraph about how there
// is nothing to act on.
func meaningful(text string) bool {
	stripped := text
	for _, token := range []string{"@here", "@channel", "@everyone", "@group", "@someone"} {
		stripped = strings.ReplaceAll(stripped, token, " ")
	}
	// Whatever is left has to be more than punctuation and a stray handle.
	words := 0
	for _, field := range strings.Fields(stripped) {
		field = strings.Trim(field, ".,:;!?()[]\"'")
		if strings.HasPrefix(field, "@") || field == "" {
			continue
		}
		words++
	}
	return words >= 3
}

// Not every message keeps its words in `text`. Anything posted by an app, and
// anything with formatting Slack could not flatten, arrives with an empty text
// field and the actual content in blocks or attachments. Reading only `text`
// is why the brief kept describing real messages as "no message attached".
func messageWords(text string, blocks, attachments json.RawMessage) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	parts := []string{}
	if words := strings.TrimSpace(walkBlocks(blocks)); words != "" {
		parts = append(parts, words)
	}
	if words := strings.TrimSpace(walkBlocks(attachments)); words != "" {
		parts = append(parts, words)
	}
	return strings.Join(parts, " ")
}

// walkBlocks pulls the readable strings out of Block Kit without modelling it.
// The format nests differently for every block type and gains new ones over
// time, so this walks whatever arrived and keeps the parts that read as words.
func walkBlocks(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var tree any
	if json.Unmarshal(raw, &tree) != nil {
		return ""
	}
	var out []string
	collect(tree, &out)
	return strings.Join(out, " ")
}

func collect(node any, out *[]string) {
	switch value := node.(type) {
	case []any:
		for _, child := range value {
			collect(child, out)
		}

	case map[string]any:
		// Entities keep their id, not their name, so re-emit them in wire form
		// and let Render resolve them the same way it does inline mentions.
		kind, _ := value["type"].(string)
		switch kind {
		case "user":
			if id, ok := value["user_id"].(string); ok {
				*out = append(*out, "<@"+id+">")
				return
			}
		case "usergroup":
			if id, ok := value["usergroup_id"].(string); ok {
				*out = append(*out, "<!subteam^"+id+">")
				return
			}
		case "broadcast":
			if scope, ok := value["range"].(string); ok {
				*out = append(*out, "<!"+scope+">")
				return
			}
		case "channel":
			if id, ok := value["channel_id"].(string); ok {
				*out = append(*out, "<#"+id+">")
				return
			}
		case "emoji", "image":
			return
		}

		for _, key := range []string{"text", "value", "title", "pretext", "fallback"} {
			if words, ok := value[key].(string); ok && strings.TrimSpace(words) != "" {
				*out = append(*out, words)
			}
		}
		if kind == "link" {
			if href, ok := value["url"].(string); ok && len(*out) > 0 {
				*out = append(*out, "("+href+")")
			}
		}
		for _, key := range []string{"elements", "blocks", "fields", "attachments"} {
			collect(value[key], out)
		}
		// A nested {type: mrkdwn, text: "..."} hangs off "text" as an object.
		if _, isString := value["text"].(string); !isString {
			collect(value["text"], out)
		}
	}
}
