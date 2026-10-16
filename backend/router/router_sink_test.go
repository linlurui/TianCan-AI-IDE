package router

import (
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/events"
)

// TestSmartRouterEmit_SinkPreferred 行为证据（G3）：
// 注入 Sink 后，SmartRouter 事件不再依赖 Wails App 即可送达。
func TestSmartRouterEmit_SinkPreferred(t *testing.T) {
	rec := events.NewRecorder()
	sr := NewSmartRouter(SmartRouterConfig{Sink: rec})
	sr.emit("router:test", "payload-1")

	if rec.Count("router:test") != 1 {
		t.Fatalf("sink did not receive event; got %d", rec.Count("router:test"))
	}
	if got := rec.Events()[0].Payload; got != "payload-1" {
		t.Fatalf("payload mismatch: %v", got)
	}
}

// TestSmartRouterEmit_NoEmitterIsSilent 反证测试：
// 无 Sink 且无 App 时必须静默（走 events.Default 的 Nop），不得 panic。
// 若有人把 emit 改回直连 s.App.Event.Emit 而无空值守卫，本测试必然 panic。
func TestSmartRouterEmit_NoEmitterIsSilent(t *testing.T) {
	sr := NewSmartRouter(SmartRouterConfig{})
	sr.emit("router:test", nil) // 不应 panic
}

// TestHardwareMonitorEmit_Sink 验证 HardwareMonitor 的 Sink 注入路径。
func TestHardwareMonitorEmit_Sink(t *testing.T) {
	rec := events.NewRecorder()
	hm := NewHardwareMonitor()
	hm.SetSink(rec)
	hm.emit("router:hardware-changed", &HardwareProfile{CPUCores: 8})

	if rec.Count("router:hardware-changed") != 1 {
		t.Fatalf("monitor sink did not receive event")
	}
	p, ok := rec.Events()[0].Payload.(*HardwareProfile)
	if !ok || p.CPUCores != 8 {
		t.Fatalf("payload mismatch: %+v", rec.Events()[0].Payload)
	}
}
