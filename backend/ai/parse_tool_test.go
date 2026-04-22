package ai

import (
	"testing"
)

func TestParseToolCall_ThinkingExtraction(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantTool     string
		wantThinking string
		wantHasTool  bool
	}{
		{
			name: "thinking_before_and_after_code_block",
			// Typical DeepSeek output: thinking text, then tool call in code block, then more text
			input:        "我将帮你删除AGENT.md文件。让我先检查文件是否存在。\n\n```json\n{\"name\": \"bash\", \"args\": {\"command\": \"ls /Volumes/workspace/project/dt-pay/AGENT.md\"}}\n```\n\n请稍等，我来执行这个操作。",
			wantTool:     "bash",
			wantThinking: "我将帮你删除AGENT.md文件。让我先检查文件是否存在。\n\n请稍等，我来执行这个操作。",
			wantHasTool:  true,
		},
		{
			name: "only_prefix_thinking",
			// Short thinking before tool call
			input:        "我将\n```json\n{\"name\": \"bash\", \"args\": {\"command\": \"rm /Volumes/workspace/project/dt-pay/AGENT.md\"}}\n```",
			wantTool:     "bash",
			wantThinking: "我将",
			wantHasTool:  true,
		},
		{
			name:         "no_thinking_just_tool",
			input:        "```json\n{\"name\": \"bash\", \"args\": {\"command\": \"ls\"}}\n```",
			wantTool:     "bash",
			wantThinking: "",
			wantHasTool:  true,
		},
		{
			name:         "xml_tool_call_format",
			input:        "让我来检查一下文件。\n<tool_call>\n{\"name\": \"bash\", \"args\": {\"command\": \"ls AGENT.md\"}}\n</tool_call>\n执行中...",
			wantTool:     "bash",
			wantThinking: "让我来检查一下文件。\n\n执行中...",
			wantHasTool:  true,
		},
		{
			name:         "no_tool_call",
			input:        "AGENT.md文件不存在，无法删除。",
			wantTool:     "",
			wantThinking: "",
			wantHasTool:  false,
		},
		{
			name:         "old_tool_format",
			input:        "我来帮你删除这个文件。\n```tool\n{\"name\": \"bash\", \"args\": {\"command\": \"rm AGENT.md\"}}\n```\n删除完成。",
			wantTool:     "bash",
			wantThinking: "我来帮你删除这个文件。\n\n删除完成。",
			wantHasTool:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolName, _, hasTool := parseToolCall(tt.input)
			if hasTool != tt.wantHasTool {
				t.Errorf("hasTool = %v, want %v", hasTool, tt.wantHasTool)
			}
			if toolName != tt.wantTool {
				t.Errorf("toolName = %q, want %q", toolName, tt.wantTool)
			}
			// Note: parseToolCall no longer returns thinkingContent;
			// thinking extraction is handled by splitThinkingContent separately.
		})
	}
}
