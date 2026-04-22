package sse

import (
	"strings"
	"testing"
)

func TestParseSSEDeepSeek_CumulativeThinkNoDup(t *testing.T) {
	// Cumulative THINK fragments should NOT cause duplicate words
	sse := strings.Join([]string{
		`data: {"v":{"response":{"fragments":[{"type":"THINK","content":"Let me"}]}}}`,
		`data: {"v":{"response":{"fragments":[{"type":"THINK","content":"Let me check"}]}}}`,
		`data: {"v":{"response":{"fragments":[{"type":"RESPONSE","content":"The file"}]}}}`,
		`data: {"v":{"response":{"fragments":[{"type":"RESPONSE","content":"The file does not exist"}]}}}`,
	}, "\n")

	result := ParseSSEDeepSeek(sse)

	// Thinking should not have duplicated "Let me"
	thinkStart := "\u003Cthink\u003E\n"
	thinkEnd := "\n\u003C/think\u003E"
	startIdx := strings.Index(result, thinkStart)
	if startIdx == -1 {
		t.Fatalf("expected thinking section in result, got: %q", result)
	}
	afterStart := startIdx + len(thinkStart)
	endIdx := strings.Index(result[afterStart:], thinkEnd)
	if endIdx == -1 {
		t.Fatalf("expected thinking end marker in result, got: %q", result)
	}
	thinking := result[afterStart : afterStart+endIdx]

	if strings.Contains(thinking, "Let meLet me") || strings.Contains(thinking, "Let me Let me") {
		t.Fatalf("thinking has duplicated words: %q", thinking)
	}
	if !strings.Contains(thinking, "Let me check") {
		t.Fatalf("thinking should contain 'Let me check', got: %q", thinking)
	}

	// Answer should not contain thinking content
	answerStart := afterStart + endIdx + len(thinkEnd)
	answer := strings.TrimLeft(result[answerStart:], "\n")
	if strings.Contains(answer, "Let me") {
		t.Fatalf("answer should not contain thinking content, got: %q", answer)
	}
	if !strings.Contains(answer, "The file does not exist") {
		t.Fatalf("answer should contain 'The file does not exist', got: %q", answer)
	}
}

func TestParseSSEDeepSeek_JSONPatchRoutedToCorrectBuilder(t *testing.T) {
	// JSON Patch APPEND during THINK phase should go to thinking, not content
	sse := strings.Join([]string{
		`data: {"v":{"response":{"fragments":[{"type":"THINK","content":"I need"}]}}}`,
		`data: {"o":"APPEND","v":" to check","p":"response/fragments/-1/content"}`,
		`data: {"v":{"response":{"fragments":[{"type":"RESPONSE","content":"Done"}]}}}`,
	}, "\n")

	result := ParseSSEDeepSeek(sse)

	// The APPEND " to check" should go to thinking, not content
	if !strings.Contains(result, "I need to check") {
		t.Fatalf("thinking should contain 'I need to check', got: %q", result)
	}
	// Answer should only have "Done"
	thinkEnd := "\n\u003C/think\u003E"
	endIdx := strings.Index(result, thinkEnd)
	if endIdx == -1 {
		t.Fatalf("expected thinking end marker")
	}
	answer := strings.TrimLeft(result[endIdx+len(thinkEnd):], "\n")
	if !strings.Contains(answer, "Done") {
		t.Fatalf("answer should contain 'Done', got: %q", answer)
	}
	if strings.Contains(answer, "to check") {
		t.Fatalf("answer should NOT contain thinking APPEND content, got: %q", answer)
	}
}

func TestParseSSEDeepSeek_DeltaOverlapRemoved(t *testing.T) {
	// Delta THINK fragments that overlap with existing content should not cause duplicates
	sse := strings.Join([]string{
		`data: {"v":{"response":{"fragments":[{"type":"THINK","content":"我们有一个"}]}}}`,
		`data: {"v":{"response":{"fragments":[{"type":"THINK","content":"有一个工具叫"}]}}}`,
		`data: {"v":{"response":{"fragments":[{"type":"RESPONSE","content":"好的"}]}}}`,
	}, "\n")

	result := ParseSSEDeepSeek(sse)

	// Should NOT have "有一个有一个"
	if strings.Contains(result, "有一个有一个") {
		t.Fatalf("thinking has duplicated words: %q", result)
	}
	// Should have the full content without duplication
	if !strings.Contains(result, "我们有一个工具叫") {
		t.Fatalf("thinking should contain '我们有一个工具叫', got: %q", result)
	}
}

func TestParseSSEDeepSeek_ResponseOnly(t *testing.T) {
	sse := `data: {"v":{"response":{"fragments":[{"type":"RESPONSE","content":"Hello world"}]}}}`

	result := ParseSSEDeepSeek(sse)

	if result != "Hello world" {
		t.Fatalf("expected 'Hello world', got: %q", result)
	}
}
