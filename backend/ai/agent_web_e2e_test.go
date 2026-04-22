package ai

import (
	"fmt"
	"strings"
	"testing"
)

// TestAgentRunWeb_ThinkingExtraction tests that AgentRunWeb correctly
// strips tool call syntax from intermediate assistant messages.
func TestAgentRunWeb_ThinkingExtraction(t *testing.T) {
	svc := NewService()

	// Load web auth from disk (user has already logged in to deepseek)
	webID := svc.GetWebAuth()
	if webID == "" {
		t.Skip("No web auth found on disk — skipping e2e test")
	}
	t.Logf("Using web provider: %s", webID)

	rootPath := "/Volumes/workspace/project/dt-pay"
	result := svc.AgentRunWeb("帮我把AGENT.md删掉", rootPath, 5)

	t.Logf("Done=%v, Iterations=%d, Error=%s", result.Done, result.Iterations, result.Error)

	// Check that intermediate assistant messages have tool call syntax stripped
	for i, m := range result.Messages {
		role := m.Role
		content := m.Content
		if content == "" {
			content = "(empty)"
		}
		// Truncate for readability
		display := content
		if len(display) > 200 {
			display = display[:200] + "..."
		}
		t.Logf("msg[%d] role=%s content=%q", i, role, display)

		// Verify: intermediate assistant messages should NOT contain tool call syntax
		if role == "assistant" {
			if strings.Contains(content, "```json") || strings.Contains(content, "```tool") {
				t.Errorf("msg[%d] assistant still contains tool call syntax: %q", i, content[:min(100, len(content))])
			}
			if strings.Contains(content, "{\"name\"") && strings.Contains(content, "\"args\"") {
				t.Errorf("msg[%d] assistant still contains raw tool JSON: %q", i, content[:min(100, len(content))])
			}
		}
	}

	// Print thinking content specifically
	for i, m := range result.Messages {
		if m.Role == "assistant" {
			fmt.Printf("\n=== Thinking (msg[%d]) ===\n%s\n=== End ===\n", i, m.Content)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
