package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent"
)

// latestDailyLog returns today's daily log (logs/YYYY/MM/YYYY-MM-DD.md).
func latestDailyLog(t *testing.T, memDir string) string {
	t.Helper()
	found := ""
	err := filepath.Walk(filepath.Join(memDir, "logs"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, time.Now().Format("2006-01-02")+".md") {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk logs: %v", err)
	}
	return found
}

// TestFinishChatTurnRunsMemoryPipeline: the UI's chat loops never enter
// agent.Agent.Run, so the Service-side turn hook is the only path that keeps
// Auto Memory alive. Guard both halves of it: the pipeline must actually run
// (a "记住" line reaches today's log) and the transcript must stay reachable
// and identifiable for later actions.
func TestFinishChatTurnRunsMemoryPipeline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TIANCAN_MEMORY_DIR", dir)
	s := &Service{}

	s.finishChatTurn("/tmp/project-x", "local", []ChatMessage{
		{Role: "system", Content: "你是一个编程助手"},
		{Role: "user", Content: "请记住这个项目用 tab 缩进"},
		{Role: "assistant", Content: "好的"},
	})

	logPath := latestDailyLog(t, filepath.Join(dir, "memory"))
	if logPath == "" {
		t.Fatal("no daily log was written — the memory pipeline did not run")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tab 缩进") {
		t.Fatalf("explicit remember-line was not recorded:\n%s", data)
	}

	conv := s.lastConversation("/tmp/project-x")
	if len(conv) != 2 {
		t.Fatalf("expected the system prompt dropped and 2 kept messages, got %d", len(conv))
	}
	for i, m := range conv {
		if m.UUID == "" {
			t.Fatalf("message %d has no UUID: the extraction cursor would stay empty", i)
		}
		if m.Timestamp.IsZero() {
			t.Fatalf("message %d has no timestamp", i)
		}
	}
}

// TestIdentifyMessagesGuards: conversions must not rewrite identities that are
// already set, must fill the missing ones, and must never mutate the caller's
// slice (the chat loops still read it afterwards).
func TestIdentifyMessagesGuards(t *testing.T) {
	in := []agent.Message{
		{Role: agent.RoleUser, Content: "a", UUID: "keep-me"},
		{Role: agent.RoleAssistant, Content: "b"},
	}
	out := identifyMessages(in)
	if in[1].UUID != "" {
		t.Fatal("identifyMessages must not mutate its input")
	}
	if out[0].UUID != "keep-me" {
		t.Fatalf("existing UUID was overwritten: %q", out[0].UUID)
	}
	if out[1].UUID == "" || out[1].UUID == out[0].UUID {
		t.Fatalf("missing UUID was not filled: %q", out[1].UUID)
	}
	if identifyMessages(nil) != nil {
		t.Fatal("empty input should stay empty")
	}
}
