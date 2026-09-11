package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Two ways to reach Claude.
//
// "subscription" shells out to the local claude CLI. That is Claude Code being
// Claude Code, so your plan covers it and every model is available, which the
// raw Messages API will not do for a subscription token.
//
// "apikey" posts to the Messages API and bills per token.

// Tools stay off. The prompt carries untrusted text from Slack, GitHub and your
// calendar, so a model that can reach Bash is a model that can be talked into
// running something. Measured: an empty allow-list does NOT disable them.
var disallowedTools = []string{
	"Bash", "Read", "Write", "Edit", "Glob", "Grep",
	"ToolSearch", "WebSearch", "WebFetch", "Task", "TodoWrite",
}

// Smaller models to fall back to, in order.
var fallbackModels = []string{"claude-sonnet-5", "claude-haiku-4-5"}

// Ask is one question for Claude. The brief is the big one; a profile guess or
// a triage pass is a small one that should never ride the Opus chain with a
// 16k output budget and a brief-shaped schema forced onto it, which is exactly
// what every call did before this existed.
type Ask struct {
	System    string
	Prompt    string
	Schema    map[string]any // structured output; nil means plain text
	Models    []string       // chain to try; nil means the configured model and its fallbacks
	MaxTokens int            // 0 means 16000
	Effort    string         // "" means the configured effort
	Think     bool           // adaptive thinking, ignored on models without it
	Purpose   string         // write | triage | guess, for the usage ledger
}

// Usage is what one call cost, in tokens and where possible in dollars. It is
// recorded because the numbers are already on the wire and were being thrown
// away, and a system that cannot say what it spends cannot be made cheaper.
type Usage struct {
	Model      string    `json:"model"`
	Purpose    string    `json:"purpose"`
	Input      int       `json:"input"`
	Output     int       `json:"output"`
	CacheRead  int       `json:"cacheRead"`
	CacheWrite int       `json:"cacheWrite"`
	CostUSD    float64   `json:"costUSD,omitempty"`
	DurationMS int       `json:"durationMs"`
	At         time.Time `json:"at"`
}

type Written struct {
	JSON           json.RawMessage
	Model          string
	DowngradedFrom string
	Usage          Usage
}

// What `claude -p --output-format json` prints. With --json-schema the object
// arrives in structured_output and result is empty; without it, result holds
// prose with JSON somewhere inside. Both are handled.
type cliResult struct {
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	DurationMS       int             `json:"duration_ms"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		Input      int `json:"input_tokens"`
		Output     int `json:"output_tokens"`
		CacheRead  int `json:"cache_read_input_tokens"`
		CacheWrite int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func modelChain(preferred string) []string {
	chain := []string{preferred}
	for _, m := range fallbackModels {
		if m != preferred {
			chain = append(chain, m)
		}
	}
	return chain
}

// Write asks Claude for the brief. Kept as the name every caller knows.
func Write(ctx context.Context, cfg *Config, system, prompt string) (*Written, error) {
	return AskClaude(ctx, cfg, Ask{
		System: system, Prompt: prompt, Schema: BriefSchema(), Think: true, Purpose: "write",
	})
}

// AskClaude runs one Ask down its model chain, stepping down when a model is
// rate limited or refuses, and stopping on anything that looks like a broken
// setup rather than a busy one.
func AskClaude(ctx context.Context, cfg *Config, ask Ask) (*Written, error) {
	chain := ask.Models
	if len(chain) == 0 {
		chain = modelChain(cfg.Claude.Model)
	}
	if ask.Effort == "" {
		ask.Effort = cfg.Claude.Effort
	}
	if ask.MaxTokens == 0 {
		ask.MaxTokens = 16000
	}

	var lastErr error
	for _, model := range chain {
		var reply answer
		var err error
		if cfg.Claude.Mode == "apikey" {
			reply, err = askViaAPI(ctx, cfg, ask, model)
		} else if hostedMode && cfg.Claude.OAuthToken == "" {
			// Never the machine's own login for a stranger's brief.
			return nil, errors.New("this server needs your own Claude Code token or an API key; add one in settings")
		} else {
			reply, err = askViaCLI(ctx, ask, model, cfg.Claude.OAuthToken)
		}
		if err != nil {
			lastErr = err
			if !worthFallingBack(err) {
				return nil, err
			}
			continue
		}

		body := reply.structured
		if len(body) == 0 {
			if body, err = extractJSON(reply.text); err != nil {
				lastErr = err
				continue
			}
		}
		reply.usage.Model, reply.usage.Purpose, reply.usage.At = model, ask.Purpose, time.Now()
		out := &Written{JSON: body, Model: model, Usage: reply.usage}
		if model != chain[0] {
			out.DowngradedFrom = chain[0]
		}
		return out, nil
	}
	return nil, fmt.Errorf("no model could answer: %w", lastErr)
}

// answer is what either path hands back before it is turned into a Written.
type answer struct {
	text       string
	structured json.RawMessage
	usage      Usage
}

// A refusal or a rate limit is worth trying the next model for; a broken setup
// is not.
func worthFallingBack(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"rate limit", "429", "overloaded", "not supported", "unavailable", "500", "529"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// ── The subscription path ───────────────────────────────

// cliArgs is the whole command line, as a pure function so the ordering
// rules can be tested without spawning anything. Two of them bite:
// --disallowed-tools is variadic and swallows everything up to the next flag,
// so it must be followed by a flag and never by the prompt; and the prompt
// itself is positional and must come last.
func cliArgs(ask Ask, model string) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		// Do not write a transcript. Without this the CLI saves the whole
		// prompt to ~/.claude/projects/<cwd>/<session>.jsonl in plaintext,
		// forever: every DM, every calendar entry, every issue body. That would
		// make encrypting our own data directory pointless.
		"--no-session-persistence",
		// Don't inherit CLAUDE.md, project settings or MCP servers: a brief
		// should read the same on any machine, and none of that belongs in it.
		"--setting-sources", "",
	}
	if ask.Schema != nil {
		if raw, err := json.Marshal(ask.Schema); err == nil {
			args = append(args, "--json-schema", string(raw))
		}
	}
	if ask.Effort != "" && model != "claude-haiku-4-5" {
		args = append(args, "--effort", ask.Effort)
	}
	args = append(args, "--disallowed-tools")
	args = append(args, disallowedTools...)
	args = append(args, "--append-system-prompt", ask.System, ask.Prompt)
	return args
}

func askViaCLI(ctx context.Context, ask Ask, model, oauthToken string) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// exec, not a shell: the prompt is full of quotes, backticks and angle
	// brackets from other people's messages, and none of it is ever parsed.
	cmd := exec.CommandContext(ctx, "claude", cliArgs(ask, model)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// The account's own Claude Code credential, if it gave one, goes in the
	// environment of this one process and nowhere else: never an argument,
	// never a file. Without one the CLI uses whatever login the machine has,
	// which on a laptop is the reader's own.
	cmd.Env = append(os.Environ(), "DISABLE_AUTOUPDATER=1")
	if oauthToken != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return answer{}, fmt.Errorf("claude took longer than 5 minutes")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return answer{}, fmt.Errorf("claude cli: %s", truncate(detail, 300))
	}

	var res cliResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return answer{}, fmt.Errorf("claude returned something that wasn't JSON: %s", truncate(stdout.String(), 200))
	}
	if res.IsError {
		return answer{}, fmt.Errorf("claude: %s", truncate(res.Result, 300))
	}

	structured := res.StructuredOutput
	if string(structured) == "null" {
		structured = nil
	}
	return answer{
		text:       res.Result,
		structured: structured,
		usage: Usage{
			Input: res.Usage.Input, Output: res.Usage.Output,
			CacheRead: res.Usage.CacheRead, CacheWrite: res.Usage.CacheWrite,
			CostUSD: res.TotalCostUSD, DurationMS: res.DurationMS,
		},
	}, nil
}

// ── The API-key path ────────────────────────────────────

// apiBody is the request, as a pure function. The system prompt goes as a
// content block marked for caching: it is byte-identical from one call to the
// next, and a retry or a rewrite within a few minutes reads it back for a
// tenth of the price. Haiku's minimum cacheable prefix is larger than the
// prompt, so on Haiku the marker is simply ignored.
func apiBody(ask Ask, model string) map[string]any {
	body := map[string]any{
		"model":      model,
		"max_tokens": ask.MaxTokens,
		"system": []any{map[string]any{
			"type": "text", "text": ask.System,
			"cache_control": map[string]string{"type": "ephemeral"},
		}},
		"messages": []any{map[string]string{"role": "user", "content": ask.Prompt}},
	}
	if ask.Schema != nil {
		body["output_config"] = map[string]any{
			"format": map[string]any{"type": "json_schema", "schema": ask.Schema},
		}
	}
	// Adaptive thinking and effort exist on current models only; Haiku 4.5
	// returns a 400 for either.
	if model != "claude-haiku-4-5" {
		if ask.Think {
			body["thinking"] = map[string]string{"type": "adaptive"}
		}
		if ask.Effort != "" {
			cfg, _ := body["output_config"].(map[string]any)
			if cfg == nil {
				cfg = map[string]any{}
				body["output_config"] = cfg
			}
			cfg["effort"] = ask.Effort
		}
	}
	return body
}

func askViaAPI(ctx context.Context, cfg *Config, ask Ask, model string) (answer, error) {
	raw, err := json.Marshal(apiBody(ask, model))
	if err != nil {
		return answer{}, err
	}
	req, err := newRequest(ctx, "POST", "https://api.anthropic.com/v1/messages", raw)
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", cfg.Claude.APIKey)

	started := time.Now()
	res, err := httpClient.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer res.Body.Close()

	payload, _ := readAll(res.Body)
	if res.StatusCode != 200 {
		return answer{}, fmt.Errorf("messages api %d: %s", res.StatusCode, apiErrorMessage(payload))
	}

	var parsed struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return answer{}, err
	}
	if parsed.StopReason == "refusal" {
		return answer{}, errors.New("claude declined to answer")
	}
	usage := Usage{
		Input: parsed.Usage.Input, Output: parsed.Usage.Output,
		CacheRead: parsed.Usage.CacheRead, CacheWrite: parsed.Usage.CacheWrite,
		DurationMS: int(time.Since(started).Milliseconds()),
	}
	for i := len(parsed.Content) - 1; i >= 0; i-- {
		if parsed.Content[i].Type == "text" {
			return answer{text: parsed.Content[i].Text, usage: usage}, nil
		}
	}
	return answer{}, errors.New("claude returned no text")
}

func apiErrorMessage(payload []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return truncate(string(payload), 200)
}

// ── Getting JSON back out ───────────────────────────────

// The CLI returns prose, so the brief arrives as JSON inside it, sometimes
// fenced. Take the outermost object.
func extractJSON(text string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(text)
	if fenced := fencedBlock(trimmed); fenced != "" {
		trimmed = fenced
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object in the reply: %s", truncate(trimmed, 160))
	}
	candidate := trimmed[start : end+1]
	if !json.Valid([]byte(candidate)) {
		return nil, fmt.Errorf("the reply's JSON didn't parse: %s", truncate(candidate, 160))
	}
	return json.RawMessage(candidate), nil
}

func fencedBlock(text string) string {
	open := strings.Index(text, "```")
	if open == -1 {
		return ""
	}
	rest := text[open+3:]
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		rest = rest[nl+1:]
	}
	if close := strings.Index(rest, "```"); close != -1 {
		return strings.TrimSpace(rest[:close])
	}
	return ""
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
