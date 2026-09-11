package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIBodyOmitsSchemaWhenNil(t *testing.T) {
	body := apiBody(Ask{System: "s", Prompt: "p", MaxTokens: 512}, "claude-opus-5")
	if _, has := body["output_config"]; has {
		t.Errorf("a plain-text ask carried output_config: %v", body["output_config"])
	}
	if body["max_tokens"] != 512 {
		t.Errorf("max_tokens = %v, want 512", body["max_tokens"])
	}
}

func TestAPIBodyCarriesTheSchemaAndEffort(t *testing.T) {
	body := apiBody(Ask{System: "s", Prompt: "p", Schema: map[string]any{"type": "object"}, Effort: "high", Think: true}, "claude-opus-5")
	cfg, _ := body["output_config"].(map[string]any)
	if cfg == nil {
		t.Fatal("no output_config")
	}
	format, _ := cfg["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("format = %v", format)
	}
	if cfg["effort"] != "high" {
		t.Errorf("effort = %v", cfg["effort"])
	}
	if _, has := body["thinking"]; !has {
		t.Error("adaptive thinking was asked for and not sent")
	}
}

// Haiku 4.5 returns 400 for thinking and for effort; the body must not carry
// either, whatever the ask said.
func TestAPIBodyOmitsThinkingOnHaiku(t *testing.T) {
	body := apiBody(Ask{System: "s", Prompt: "p", Schema: map[string]any{"type": "object"}, Effort: "high", Think: true}, "claude-haiku-4-5")
	if _, has := body["thinking"]; has {
		t.Error("thinking sent to haiku")
	}
	cfg, _ := body["output_config"].(map[string]any)
	if _, has := cfg["effort"]; has {
		t.Error("effort sent to haiku")
	}
	if cfg["format"] == nil {
		t.Error("the schema was dropped along with effort")
	}
}

// The system prompt goes as a cacheable block, not a bare string.
func TestAPIBodyMarksSystemForCaching(t *testing.T) {
	body := apiBody(Ask{System: "the same every day", Prompt: "p"}, "claude-opus-5")
	blocks, _ := body["system"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("system = %v, want one block", body["system"])
	}
	block, _ := blocks[0].(map[string]any)
	if block["text"] != "the same every day" {
		t.Errorf("block text = %v", block["text"])
	}
	if block["cache_control"] == nil {
		t.Error("system block is not marked for caching")
	}
}

// --disallowed-tools is variadic: everything after it up to the next flag is
// read as a tool name. The prompt must therefore never follow it directly, and
// must always be last.
func TestCLIArgsKeepPromptLast(t *testing.T) {
	args := cliArgs(Ask{System: "SYS", Prompt: "THE PROMPT", Schema: map[string]any{"type": "object"}, Effort: "high"}, "claude-opus-5")

	if args[len(args)-1] != "THE PROMPT" {
		t.Fatalf("prompt is not last: %v", args)
	}
	if args[len(args)-3] != "--append-system-prompt" || args[len(args)-2] != "SYS" {
		t.Errorf("system prompt is not right before the prompt: %v", args[len(args)-4:])
	}

	// Whatever follows --disallowed-tools must be tool names then a flag.
	at := indexOfArg(args, "--disallowed-tools")
	if at < 0 {
		t.Fatal("no --disallowed-tools")
	}
	for i := at + 1; i < len(args); i++ {
		if args[i] == "--append-system-prompt" {
			break
		}
		if args[i][0] == '-' || args[i] == "THE PROMPT" {
			t.Fatalf("something other than a tool name followed --disallowed-tools: %q", args[i])
		}
	}

	// The schema and effort flags come before it, never after.
	for _, flag := range []string{"--json-schema", "--effort"} {
		if i := indexOfArg(args, flag); i < 0 || i > at {
			t.Errorf("%s at %d is not before --disallowed-tools at %d", flag, i, at)
		}
	}
}

func TestCLIArgsSkipEffortOnHaikuAndSchemaWhenNil(t *testing.T) {
	args := cliArgs(Ask{System: "s", Prompt: "p", Effort: "high"}, "claude-haiku-4-5")
	if indexOfArg(args, "--effort") >= 0 {
		t.Error("effort sent to haiku")
	}
	if indexOfArg(args, "--json-schema") >= 0 {
		t.Error("json-schema sent with no schema")
	}
}

// The real shape of `claude -p --output-format json --json-schema …`, captured
// by hand: the object is in structured_output and result is empty.
func TestCLIResultParsesTheRealShape(t *testing.T) {
	raw := `{"is_error":false,"result":"","structured_output":{"name":"Sebastian","role":"Infra"},
	  "duration_ms":4120,"total_cost_usd":0.0271,
	  "usage":{"input_tokens":16,"output_tokens":263,"cache_read_input_tokens":15032,"cache_creation_input_tokens":19027},
	  "modelUsage":{"claude-haiku-4-5":{"inputTokens":16,"outputTokens":263}}}`
	var res cliResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	var out struct{ Name, Role string }
	if err := json.Unmarshal(res.StructuredOutput, &out); err != nil || out.Name != "Sebastian" {
		t.Errorf("structured_output = %s", res.StructuredOutput)
	}
	if res.Usage.CacheRead != 15032 || res.Usage.Output != 263 || res.TotalCostUSD == 0 {
		t.Errorf("usage not parsed: %+v cost=%v", res.Usage, res.TotalCostUSD)
	}
}

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// The system prompt is the cache prefix. If anything in it depends on the
// clock, the prefix changes every day and caching pays for nothing.
func TestSystemPromptIsByteStableAcrossDays(t *testing.T) {
	a := systemPrompt
	b := systemPrompt
	if a != b {
		t.Fatal("system prompt differs between reads")
	}
	// Weekday names appear as examples ("last Tuesday") and are fine; a real
	// date or a "Today is" line would change the prefix every morning.
	for _, leak := range []string{"2026", "2027", "Today is", "Local time now"} {
		if strings.Contains(a, leak) {
			t.Errorf("system prompt mentions %q, which changes with the date", leak)
		}
	}
	if !strings.Contains(a, `"push_forward"`) {
		t.Error("the schema did not move into the cached prefix")
	}
}
