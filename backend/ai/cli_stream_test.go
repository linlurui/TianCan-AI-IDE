package ai

import "testing"

func TestCLIFinalText(t *testing.T) {
	claude := map[string]interface{}{"type": "result", "result": "Claude answer"}
	if got := cliFinalText(claude); got != "Claude answer" {
		t.Fatalf("unexpected Claude final text: %q", got)
	}
	codex := map[string]interface{}{
		"type": "item.completed",
		"item": map[string]interface{}{"type": "agent_message", "text": "Codex answer"},
	}
	if got := cliFinalText(codex); got != "Codex answer" {
		t.Fatalf("unexpected Codex final text: %q", got)
	}
}

func TestNormalizeCLIToolName(t *testing.T) {
	tests := map[string]string{
		"Read":        "read_file",
		"Edit":        "edit_file",
		"MultiEdit":   "edit_file",
		"Bash":        "bash",
		"Grep":        "grep",
		"custom_tool": "custom_tool",
	}
	for input, expected := range tests {
		if got := normalizeCLIToolName(input); got != expected {
			t.Fatalf("normalizeCLIToolName(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestClaudeToolResultText(t *testing.T) {
	content := []interface{}{
		map[string]interface{}{"type": "text", "text": "first"},
		map[string]interface{}{"type": "text", "text": "second"},
	}
	if got := claudeToolResultText(content); got != "first\nsecond" {
		t.Fatalf("unexpected tool result: %q", got)
	}
}

func TestCLIToolDecisionSummaryIncludesActualFile(t *testing.T) {
	summary := cliToolDecisionSummary("Read", map[string]interface{}{"file_path": "/tmp/project/go.mod"})
	if summary != "需要确认项目中的实际实现，模型决定读取 /tmp/project/go.mod。" {
		t.Fatalf("unexpected decision summary: %q", summary)
	}
}
