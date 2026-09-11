package main

import "encoding/json"

// The contract between Claude and the brief page.
//
// The API-key path enforces this server-side via output_config.format. The
// subscription path goes through the CLI, which has no such parameter, so the
// same schema is shown to the model in the prompt and validated on the way in.
func BriefSchema() map[string]any {
	str := map[string]any{"type": "string"}
	strs := map[string]any{"type": "array", "items": str}

	object := func(props map[string]any) map[string]any {
		required := make([]string, 0, len(props))
		for k := range props {
			required = append(required, k)
		}
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             required,
			"properties":           props,
		}
	}

	sourced := func(props map[string]any) map[string]any {
		props["source_url"] = str
		props["source_name"] = str
		props["source_icon_key"] = str
		return props
	}

	meeting := object(map[string]any{
		"time":        str,
		"end_time":    str,
		"title":       str,
		"note":        str,
		"prep_prompt": str,
	})

	return object(map[string]any{
		"header": object(map[string]any{"greeting": str}),
		"push_forward": object(map[string]any{
			"title": str, "body": str, "cta_prompt": str, "source_url": str, "draft": str,
		}),
		"top_todos": map[string]any{"type": "array", "items": object(sourced(map[string]any{
			"label": str, "label_style": map[string]any{"type": "string", "enum": []string{"active", "outline"}},
			"title": str, "body": str, "due": str,
		}))},
		"new_updates": map[string]any{"type": "array", "items": object(sourced(map[string]any{
			"label": str, "label_style": map[string]any{"type": "string", "enum": []string{"active", "outline"}},
			"title": str, "body": str, "bullets": strs,
			"when": str, "where": str, "link_url": str,
		}))},
		"your_day": object(map[string]any{
			"morning":   map[string]any{"type": "array", "items": meeting},
			"afternoon": map[string]any{"type": "array", "items": meeting},
		}),
		"looking_ahead": object(map[string]any{"blurb": str}),
		"remember":      strs,
	})
}

// A compact, commented shape for the prompt. The raw JSON Schema is precise but
// unreadable; this is the version a model actually follows.
func BriefSchemaJSON() string {
	return `{
  "header": { "greeting": "One or two sentences to them by name, about the shape of today." },
  "push_forward": {
    "title": "The one thing worth moving today that nobody asked for: usually where two sources meet, like something shipped that a room they own has not heard, or a question in their room nobody answered. Never a to-do restated. Empty string if nothing earns it.",
    "body": "2-4 sentences: what it is, why now, what you already know.",
    "cta_prompt": "A first message to Claude that would start this work, written as them.",
    "source_url": "Verbatim from the item it rests on, or empty string.",
    "draft": "If the push is a message to send, an announcement, a reply, a note, the first draft of it in their voice, ready to paste. Empty otherwise."
  },
  "top_todos": [{
    "label": "One or two words: the project or area.",
    "label_style": "active | outline  (active = urgent or blocking)",
    "title": "Imperative and specific, not a summary of the source.",
    "body": "2-3 sentences: what happened, who is waiting, what is blocked.",
    "due": "YYYY-MM-DD. Resolve relative dates against the day the source was posted: a Tuesday message about the sync happening that day is due that Tuesday. Empty only when there is genuinely no date to land on. Never invent one.",
    "source_url": "Verbatim from the item, or empty string.",
    "source_name": "Slack | GitHub | Calendar | Linear | ...",
    "source_icon_key": "The icon key given with that source."
  }],
  "new_updates": [{
    "label": "One or two words: the project or area.",
    "label_style": "outline",
    "title": "Something that changed that they would otherwise go and ask about.",
    "body": "1-3 sentences: what changed, and what it means for them.",
    "bullets": ["0-3 short supporting facts"],
    "when": "The item's when line, copied exactly. Never work a time out. Empty if it has none.",
    "where": "Where it was said, as #channel. Empty if not meaningful.",
    "link_url": "The one destination this is about, verbatim from the item: an event page, a doc, a board. Not the message announcing it, which is source_url. Usually empty.",
    "source_url": "", "source_name": "", "source_icon_key": ""
  }],
  "your_day": {
    "morning":   [{ "time": "9:30 AM", "end_time": "9:45 AM", "title": "", "note": "What it is for and what you would want in hand.", "prep_prompt": "A message to Claude that would prepare them. Empty if it needs none." }],
    "afternoon": [{ "time": "2:00 PM", "end_time": "", "title": "", "note": "", "prep_prompt": "" }]
  },
  "looking_ahead": { "blurb": "One sentence on tomorrow, only if the calendar warrants it. Else empty." },
  "remember": ["0-3 durable facts worth carrying into future mornings. Usually empty."]
}

At most 5 top_todos and 3 new_updates, usually fewer. Every key must be present.`
}

// Validate checks the model gave us the keys the page needs before we store it.
func ValidateBrief(raw json.RawMessage) error {
	var probe struct {
		Header *struct {
			Greeting string `json:"greeting"`
		} `json:"header"`
		PushForward *json.RawMessage `json:"push_forward"`
		TopTodos    *json.RawMessage `json:"top_todos"`
		YourDay     *json.RawMessage `json:"your_day"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	switch {
	case probe.Header == nil:
		return errMissing("header")
	case probe.PushForward == nil:
		return errMissing("push_forward")
	case probe.TopTodos == nil:
		return errMissing("top_todos")
	case probe.YourDay == nil:
		return errMissing("your_day")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "the brief is missing " + string(e) }
