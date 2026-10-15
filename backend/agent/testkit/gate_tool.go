package testkit

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// GateTool is a controllable types.Tool for scheduling/concurrency tests.
//
// 行为契约：
//   - Execute 进入时并发计数 +1，退出时 -1，并记录历史峰值（MaxConcurrency）。
//   - 默认阻塞在门闩上，直到 Release/ReleaseAll 放行，或 ctx 取消（返回 ctx 错误）。
//   - SetAutoRelease(true) 后不阻塞，仅记录并发峰值。
//   - 并发安全声明（IsConcurrencySafe）与只读声明（IsReadOnly）均可配置，
//     用于验证调度器对"并行/排他"声明的尊重程度。
//
// 所有计数走 atomic，-race 下可直接复用。
type GateTool struct {
	name            string
	description     string
	concurrencySafe bool
	readOnly        bool

	autoRelease atomic.Bool
	releaseCh   chan struct{} // buffered; each value releases one blocked call

	calls   atomic.Int32
	cur     atomic.Int32
	maxSeen atomic.Int32
}

// GateToolOption customizes a GateTool at construction.
type GateToolOption func(*GateTool)

// WithConcurrencySafe sets the IsConcurrencySafe declaration (parallel vs exclusive).
func WithConcurrencySafe(safe bool) GateToolOption {
	return func(t *GateTool) { t.concurrencySafe = safe }
}

// WithReadOnly sets the IsReadOnly declaration.
func WithReadOnly(ro bool) GateToolOption {
	return func(t *GateTool) { t.readOnly = ro }
}

// WithAutoRelease makes Execute return immediately without blocking on the gate.
func WithAutoRelease() GateToolOption {
	return func(t *GateTool) { t.autoRelease.Store(true) }
}

// NewGateTool creates a gated tool with the given name.
func NewGateTool(name string, opts ...GateToolOption) *GateTool {
	t := &GateTool{
		name:        name,
		description: "testkit gated tool " + name,
		releaseCh:   make(chan struct{}, 64),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *GateTool) Name() string        { return t.name }
func (t *GateTool) Description() string { return t.description }

func (t *GateTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *GateTool) IsReadOnly() bool        { return t.readOnly }
func (t *GateTool) IsConcurrencySafe() bool { return t.concurrencySafe }

// Execute blocks on the gate (unless auto-release), then reports the
// concurrency level observed while this call was running.
func (t *GateTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	t.calls.Add(1)
	cur := t.cur.Add(1)
	for {
		old := t.maxSeen.Load()
		if cur <= old || t.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}
	defer t.cur.Add(-1)

	if !t.autoRelease.Load() {
		select {
		case <-ctx.Done():
			return types.ToolResult{Success: false, Error: ctx.Err().Error()}, ctx.Err()
		case <-t.releaseCh:
		}
	}
	return types.ToolResult{
		Content: fmt.Sprintf("%s ok (concurrency=%d)", t.name, cur),
		Success: true,
	}, nil
}

// Release allows n currently-blocked (or future) calls to proceed.
func (t *GateTool) Release(n int) {
	for i := 0; i < n; i++ {
		t.releaseCh <- struct{}{}
	}
}

// CallCount returns how many times Execute was entered.
func (t *GateTool) CallCount() int { return int(t.calls.Load()) }

// MaxConcurrency returns the peak number of calls observed inside Execute simultaneously.
func (t *GateTool) MaxConcurrency() int { return int(t.maxSeen.Load()) }
