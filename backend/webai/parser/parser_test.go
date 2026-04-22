package parser

import (
	"encoding/json"
	"testing"
)

func TestParseInferredToolCalls_BashCommand(t *testing.T) {
	input := "I'll delete the file for you.\n```json\n{\"command\": \"rm AGENT.md\", \"timeout\": 30}\n```\nDeleted AGENT.md."
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatalf("failed to unmarshal args: %v", err)
	}
	if args["command"] != "rm AGENT.md" {
		t.Fatalf("expected command='rm AGENT.md', got %v", args["command"])
	}
}

func TestParseInferredToolCalls_FileWrite(t *testing.T) {
	input := "```json\n{\"path\": \"/tmp/test.txt\", \"content\": \"hello\"}\n```"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "file_write" {
		t.Fatalf("expected tool name 'file_write', got %q", calls[0].Name)
	}
}

func TestParseInferredToolCalls_BareJSON(t *testing.T) {
	input := `I need to run rm AGENT.md. {"command": "rm AGENT.md", "timeout": 30} Done.`
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
}

func TestParseInferredToolCalls_SkipsIfNamePresent(t *testing.T) {
	input := "```json\n{\"name\": \"bash\", \"arguments\": {\"command\": \"rm AGENT.md\"}}\n```"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	// Should be parsed by parseCodeBlocks, not parseInferredToolCalls
	// Both may match but dedup should reduce to 1
	if len(calls) > 2 {
		t.Fatalf("expected at most 2 (before dedup), got %d", len(calls))
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
}

func TestParseInferredToolCalls_NoMatchForUnknownKeys(t *testing.T) {
	input := "```json\n{\"foo\": \"bar\", \"baz\": 123}\n```"
	calls := ParseToolCalls(input)
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls for unknown keys, got %d", len(calls))
	}
}

func TestParseEmojiToolCalls_BareJSON(t *testing.T) {
	input := "🔧\n{\n  \"command\": \"rm -f AGENT.md\",\n  \"timeout\": 30\n}"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
}

func TestParseEmojiToolCalls_CodeBlock(t *testing.T) {
	input := "🔧\n```json\n{\"command\": \"ls -la\", \"timeout\": 30}\n```"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
}

func TestParseEmojiToolCalls_Glob(t *testing.T) {
	input := "🔧\n{\"pattern\": \"AGENTS.md\", \"path\": \"/Volumes/workspace/project/dt-pay\"}"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "glob" {
		t.Fatalf("expected tool name 'glob', got %q", calls[0].Name)
	}
}

func TestParseToolCodeBlock(t *testing.T) {
	input := "```tool\n{\"name\": \"bash\", \"arguments\": {\"command\": \"rm AGENT.md\"}}\n```"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
}

func TestParseBareToolLabel_ToolJSON(t *testing.T) {
	input := "tool\n{\"name\": \"glob\", \"args\": {\"pattern\": \"AGENTS.md\", \"path\": \"/Volumes/workspace/project/dt-pay\"}}"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "glob" {
		t.Fatalf("expected tool name 'glob', got %q", calls[0].Name)
	}
}

func TestParseBareToolLabel_BashJSON(t *testing.T) {
	input := "bash\n{\"command\": \"rm -f AGENT.md && echo Done\", \"timeout\": 30}"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
}

func TestExtractNameAndArgs_ArgsKey(t *testing.T) {
	input := "{\"name\": \"bash\", \"args\": {\"command\": \"ls\"}}"
	calls := ParseToolCalls(input)
	if len(calls) == 0 {
		t.Fatal("expected at least 1 tool call, got 0")
	}
	if calls[0].Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Name)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatalf("failed to unmarshal args: %v", err)
	}
	if cmd, _ := args["command"].(string); cmd != "ls" {
		t.Fatalf("expected command='ls', got %v", args["command"])
	}
}
