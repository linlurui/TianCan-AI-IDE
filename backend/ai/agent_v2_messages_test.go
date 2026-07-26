package ai

import (
	"strings"
	"testing"

	"github.com/rocky233/tiancan-ai-ide/backend/agent"
)

func TestAgentMessagesToChatMessagesIncludesToolResults(t *testing.T) {
	messages := agentMessagesToChatMessages("system prompt", []agent.Message{
		{Role: agent.RoleUser, Content: "看一下项目"},
		{Role: agent.RoleAssistant, Content: "```tool\n{\"name\":\"list_directory\",\"args\":{\"path\":\".\"}}\n```"},
		{Role: agent.RoleTool, ToolName: "list_directory", Content: "README.md\nmain.go"},
	})

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	last := messages[len(messages)-1]
	if last.Role != "user" {
		t.Fatalf("tool result should be sent back as user context, got role %q", last.Role)
	}
	if !strings.Contains(last.Content, "工具 list_directory 执行结果") || !strings.Contains(last.Content, "README.md") {
		t.Fatalf("tool result was not preserved in model context: %q", last.Content)
	}
}
