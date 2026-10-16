package tools

import (
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/events"
)

// TestAutoResearchTrigger_EmitsWithoutWails 行为证据（G3 核心验收点）：
// 内核工具在无 Wails App 的环境下，经注入的 Sink 发射事件。
//
// 契约溯源：G3 前 autoresearch_trigger 直连 t.app.Event.Emit，
// 无 App 时事件完全丢失；现为 Sink 抽象。
func TestAutoResearchTrigger_EmitsWithoutWails(t *testing.T) {
	rec := events.NewRecorder()
	tr := NewAutoResearchTrigger(nil, rec) // llm=nil：OnEvent 不触达 LLM

	tr.OnEvent(AutoResearchEvent{
		Type:      "perf_bottleneck",
		Source:    "test",
		Details:   "probe",
		Timestamp: time.Now(),
	})

	if rec.Count("autoresearch:event") != 1 {
		t.Fatalf("expected 1 autoresearch:event, got %d", rec.Count("autoresearch:event"))
	}
}

// TestAutoResearchTrigger_NilSinkIsSilent 反证测试：
// sink=nil 且未设包级默认时，OnEvent 不得 panic（静默丢弃）。
// 若构造器或 emit 丢掉 nil 保护，本测试必然 panic。
func TestAutoResearchTrigger_NilSinkIsSilent(t *testing.T) {
	restore := events.SetDefault(nil) // 确保包级默认也是 Nop
	defer restore()

	tr := NewAutoResearchTrigger(nil, nil)
	tr.OnEvent(AutoResearchEvent{Type: "slow_test", Timestamp: time.Now()}) // 不应 panic
}
