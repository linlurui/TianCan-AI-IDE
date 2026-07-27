package ai

import (
	"strings"
	"testing"
)

func TestChatMessagesToCLIPromptCompactsToolSchemas(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a coding assistant.\n\n# Relevant Memories\nPrefer focused fixes.\n\n# Available Tools\n- **read_file**: read a file\n  Input schema: {\"path\":\"string\"}\n\n## Tool Calling Protocol\nlarge protocol\n\nProject root: /tmp/project"},
		{Role: "user", Content: "修复恢复文件功能"},
	}

	prompt := chatMessagesToCLIPrompt(messages)
	if strings.Contains(prompt, "# Available Tools") || strings.Contains(prompt, "Input schema") {
		t.Fatalf("CLI prompt still contains duplicated tool schemas: %s", prompt)
	}
	for _, expected := range []string{"Prefer focused fixes.", "Project root: /tmp/project", "修复恢复文件功能"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("CLI prompt lost required context %q: %s", expected, prompt)
		}
	}
}

func TestChatMessagesToCLIPromptKeepsRecentConversationWithinLimit(t *testing.T) {
	messages := []ChatMessage{{Role: "system", Content: strings.Repeat("system ", 4000)}}
	for i := 0; i < 10; i++ {
		messages = append(messages, ChatMessage{Role: "user", Content: strings.Repeat("旧消息", 3000)})
	}
	messages = append(messages, ChatMessage{Role: "user", Content: "LATEST_REQUEST"})

	prompt := chatMessagesToCLIPrompt(messages)
	if len([]rune(prompt)) > cliPromptMaxRunes {
		t.Fatalf("CLI prompt exceeded limit: %d", len([]rune(prompt)))
	}
	if !strings.Contains(prompt, "LATEST_REQUEST") {
		t.Fatal("CLI prompt dropped the latest request")
	}
}
