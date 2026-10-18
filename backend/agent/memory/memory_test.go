package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// newTestMem points the memory dir at a temp dir so nothing touches the real
// ~/.tiancan/memory while tests run.
func newTestMem(t *testing.T) *MemorySystem {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TIANCAN_MEMORY_DIR", dir)
	return NewMemorySystem(dir)
}

// msg builds a user message with a UUID (the extraction cursor relies on UUIDs).
func msg(uuid, content string) types.Message {
	return types.Message{Role: types.RoleUser, UUID: uuid, Content: content, Timestamp: time.Now()}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestExtractMemoriesDoesNotDuplicate: the same "记住" line must land in the
// daily log exactly once even though ExtractMemories runs every turn end
// (it used to rescan the whole history and append it again every time).
func TestExtractMemoriesDoesNotDuplicate(t *testing.T) {
	m := newTestMem(t)
	msgs := []types.Message{msg("u1", "请记住我喜欢用 tab 缩进"), msg("u2", "随便聊点别的")}

	if err := m.ExtractMemories(msgs); err != nil {
		t.Fatal(err)
	}
	if err := m.ExtractMemories(msgs); err != nil {
		t.Fatal(err)
	}
	// A later turn appends new messages; only those may produce new entries.
	msgs = append(msgs, msg("u3", "记住另一个偏好：深色主题"))
	if err := m.ExtractMemories(msgs); err != nil {
		t.Fatal(err)
	}

	log := readFile(t, m.GetDailyLogPath(time.Now()))
	if n := strings.Count(log, "tab 缩进"); n != 1 {
		t.Fatalf("the same remember-line was appended %d times:\n%s", n, log)
	}
	if n := strings.Count(log, "深色主题"); n != 1 {
		t.Fatalf("new remember-line missing or duplicated (%d):\n%s", n, log)
	}
}

// TestShouldExtractMemoryThresholds: below the init threshold nothing fires;
// once initialized, growth below the update threshold still must not fire.
func TestShouldExtractMemoryThresholds(t *testing.T) {
	m := newTestMem(t)

	small := []types.Message{msg("a", "短消息")}
	if EstimateTokens(small) >= m.config.MinimumMessageTokensToInit {
		t.Fatalf("fixture too big: %d tokens", EstimateTokens(small))
	}
	if m.ShouldExtractMemory(small) {
		t.Fatal("must not extract before reaching the init threshold")
	}

	big := make([]types.Message, 0, 400)
	for i := 0; i < 400; i++ {
		big = append(big, msg(string(rune('a'+i%26))+time.Duration(i).String(), strings.Repeat("x", 200)))
	}
	if !m.ShouldExtractMemory(big) {
		t.Fatal("should extract once past the init threshold with no tool calls")
	}
	// Right after an extraction, no growth → no second trigger.
	if m.ShouldExtractMemory(big) {
		t.Fatal("must not re-extract without token growth")
	}
}

// TestExtractionStateSurvivesRebuild: the agent (and MemorySystem) is rebuilt
// per user message, so the cursor must survive on disk or thresholds never fire.
func TestExtractionStateSurvivesRebuild(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TIANCAN_MEMORY_DIR", dir)
	m1 := NewMemorySystem(dir)
	if !m1.IsEnabled() {
		t.Fatal("memory system must be enabled")
	}
	big := make([]types.Message, 0, 400)
	for i := 0; i < 400; i++ {
		big = append(big, msg(string(rune('a'+i%26))+time.Duration(i).String(), strings.Repeat("y", 200)))
	}
	if !m1.ShouldExtractMemory(big) {
		t.Fatal("first instance should trigger")
	}

	m2 := NewMemorySystem(dir) // rebuilt like every message does
	if m2.ShouldExtractMemory(big) {
		t.Fatal("restored cursor must stop a second extraction for the same growth")
	}
	if m2.lastSummarizedMsgUUID == "" {
		t.Fatal("cursor not restored from disk")
	}
	if _, err := os.Stat(m2.stateFilePath()); err != nil {
		t.Fatal("state file not written:", err)
	}
}

// TestManualExtractionWritesNotes: the forked call returns updated notes, and
// we must persist them (chatFn has no file tools, so nobody else will).
func TestManualExtractionWritesNotes(t *testing.T) {
	m := newTestMem(t)
	if err := m.SetupSessionMemoryFile(); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, m.GetSessionMemoryPath())

	updated := before + "\n## Key results\n\n- implemented memory extraction\n"
	chatFn := func(string, []types.Message) (string, error) {
		return "<updated_notes>\n" + updated + "\n</updated_notes>", nil
	}
	if err := m.ManuallyExtractSessionMemory([]types.Message{msg("u1", hi)}, chatFn); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, m.GetSessionMemoryPath())
	if !strings.Contains(after, "implemented memory extraction") {
		t.Fatalf("notes were not written:\n%s", after)
	}

	// A follow-up extraction must build on what is there now.
	seen := false
	chatFn2 := func(string, []types.Message) (string, error) {
		seen = true
		return "<updated_notes>\n" + after + "\n- next step\n\n</updated_notes>", nil
	}
	if err := m.ManuallyExtractSessionMemory([]types.Message{msg("u2", hi)}, chatFn2); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("second extraction did not run")
	}
}

// TestExtractionRejectsMangledNotes: if the model drops the template skeleton
// we keep the old file instead of overwriting it with something unusable.
func TestExtractionRejectsMangledNotes(t *testing.T) {
	m := newTestMem(t)
	if err := m.SetupSessionMemoryFile(); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, m.GetSessionMemoryPath())

	chatFn := func(string, []types.Message) (string, error) {
		return "<updated_notes>\n# 只有一个标题\n丢了其它章节\n</updated_notes>", nil
	}
	if err := m.ManuallyExtractSessionMemory([]types.Message{msg("u1", hi)}, chatFn); err == nil {
		t.Fatal("extracting notes without the full skeleton must fail")
	}
	if got := readFile(t, m.GetSessionMemoryPath()); got != before {
		t.Fatal("the previous notes file must survive a rejected extraction")
	}
}

// TestExtractionRejectsEmptyNotes guards against wiping SESSION_MEMORY.md when
// the model replies with nothing usable.
func TestExtractionRejectsEmptyNotes(t *testing.T) {
	m := newTestMem(t)
	if err := m.SetupSessionMemoryFile(); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, m.GetSessionMemoryPath())

	for _, reply := range []string{"", "抱歉，我无法完成。", "```\nno headings here\n```"} {
		if err := m.writeSessionMemoryFromOutput(reply); err == nil {
			t.Fatalf("expected a rejection for %q", reply)
		}
	}
	if got := readFile(t, m.GetSessionMemoryPath()); got != before {
		t.Fatal("notes file must be untouched after rejected write attempts")
	}
}

// TestSessionMemoryForCompact: compaction must be offered real notes (and not
// an untouched template). Guards the "ok means content exists" contract —
// GetSessionMemoryForCompact's second result means wasTruncated instead.
func TestSessionMemoryForCompact(t *testing.T) {
	m := newTestMem(t)
	if _, ok := m.SessionMemoryForCompact(); ok {
		t.Fatal("nothing to inject before the notes file exists")
	}
	if err := m.SetupSessionMemoryFile(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.SessionMemoryForCompact(); ok {
		t.Fatal("an untouched template is not real content")
	}
	updated := loadTemplate(t, m) + "\n## Key results\n\n- did the thing\n"
	if err := m.writeSessionMemoryFromOutput("<updated_notes>\n" + updated + "\n</updated_notes>"); err != nil {
		t.Fatal(err)
	}
	content, ok := m.SessionMemoryForCompact()
	if !ok || !strings.Contains(content, "did the thing") {
		t.Fatalf("expected real content for compaction, got ok=%v %q", ok, content)
	}

	// GetSessionMemoryForCompact must agree on the content; its second result
	// only tells us whether truncation happened (here: it did not).
	alt, wasTruncated := m.GetSessionMemoryForCompact()
	if alt != content {
		t.Fatal("both accessors must return the same notes")
	}
	if wasTruncated {
		t.Fatal("short notes must not be reported as truncated")
	}
}

func loadTemplate(t *testing.T, m *MemorySystem) string {
	t.Helper()
	return readFile(t, m.GetSessionMemoryPath())
}

// TestDailyLogLayout covers the documented path shape
// (<mem>/logs/YYYY/MM/YYYY-MM-DD.md) that the system prompt points the model at.
func TestDailyLogLayout(t *testing.T) {
	m := newTestMem(t)
	now := time.Now()
	want := filepath.Join(m.GetAutoMemPath(), "logs",
		now.Format("2006"), now.Format("01"), now.Format("2006-01-02")+".md")
	if got := m.GetDailyLogPath(now); got != want {
		t.Fatalf("daily log path mismatch:\n got %s\nwant %s", got, want)
	}
	if err := m.AppendToDailyLog("一条手动记录"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, want); !strings.Contains(got, "一条手动记录") {
		t.Fatalf("entry missing: %q", got)
	}
}

// TestDisabledMemoryIsSilent: with the kill switch on, nothing must touch disk.
func TestDisabledMemoryIsSilent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TIANCAN_MEMORY_DIR", dir)
	t.Setenv("TIANCAN_DISABLE_AUTO_MEMORY", "1")
	m := NewMemorySystem(dir)
	if m.IsEnabled() {
		t.Fatal("expected disabled")
	}
	if err := m.AppendToDailyLog("x"); err != nil {
		t.Fatal(err)
	}
	if err := m.ExtractMemories([]types.Message{msg("u1", "记住这个")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "memory")); err == nil {
		t.Fatal("disabled memory system must not create files")
	}
}

const hi = "hi there"
