// Package events — G3 ProgressSink 抽象：内核事件发射的 Wails 解耦层。
//
// 契约：
//   - 内核/服务层只依赖本包的 Sink 接口发射事件，不直接引用 Wails application.App。
//   - Wails 是 Sink 的一种实现（由 SetApp 桥接注册）；PC 端/测试可注入自己的 Sink。
//   - 本包是叶子包（仅依赖标准库），任何包可安全导入。
//
// 溯源：G3 前事件散落为 s.App.Event.Emit 直连（27 处），内核无法在无 GUI 环境复用。
package events

import "sync"

// Sink 接收命名事件及其负载。实现必须并发安全。
type Sink interface {
	Emit(event string, payload interface{})
}

// Func 把普通函数适配为 Sink。
type Func func(event string, payload interface{})

// Emit implements Sink.
func (f Func) Emit(event string, payload interface{}) { f(event, payload) }

// nopSink 丢弃所有事件（默认行为：未注入 Sink 时静默而非 panic）。
type nopSink struct{}

func (nopSink) Emit(string, interface{}) {}

// Nop 返回丢弃型 Sink。
func Nop() Sink { return nopSink{} }

// ─── 默认 Sink（包级注入口）─────────────────────────────────────
// 供"不好逐一注入"的角落使用；优先走各服务的 SetSink 显式注入。

var (
	defaultMu   sync.RWMutex
	defaultSink Sink = nopSink{}
)

// SetDefault 设置包级默认 Sink；传 nil 重置为 Nop。返回恢复函数（便于测试）。
func SetDefault(s Sink) func() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	old := defaultSink
	if s == nil {
		defaultSink = nopSink{}
	} else {
		defaultSink = s
	}
	return func() { SetDefault(old) }
}

// Default 返回当前默认 Sink（永不为 nil）。
func Default() Sink {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultSink
}

// ─── Recorder：测试用 Sink ─────────────────────────────────────

// Recorded 是一次被记录的事件。
type Recorded struct {
	Event   string
	Payload interface{}
}

// Recorder 记录全部事件，供测试断言。并发安全。
type Recorder struct {
	mu     sync.Mutex
	events []Recorded
}

// NewRecorder 创建记录器。
func NewRecorder() *Recorder { return &Recorder{} }

// Emit implements Sink.
func (r *Recorder) Emit(event string, payload interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Recorded{Event: event, Payload: payload})
}

// Events 返回已记录事件的拷贝。
func (r *Recorder) Events() []Recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Recorded, len(r.events))
	copy(out, r.events)
	return out
}

// Count 返回某事件名被发射的次数。
func (r *Recorder) Count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Event == event {
			n++
		}
	}
	return n
}
