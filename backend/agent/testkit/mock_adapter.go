// Package testkit provides the shared test infrastructure for the agent core
// (G1: 测试基座与契约框架).
//
// 包含两类资产：
//   - MockAdapter：脚本化的 LLM 应答器，实现 types.ChatFunc，无需网络即可驱动
//     agent 主循环；逐条消费预录响应，耗尽后报错（用于捕获"多调了一次模型"这类回归）。
//   - GateTool：门控工具，实现 types.Tool，可阻塞/放行、记录并发峰值，
//     用于验证调度器的并行/排他行为。
//
// 设计契约（溯源）：对标 dsh 测试基座的"脚本化模型 + 可控工具"模式；
// 本包只依赖 types 与标准库，不依赖任何 provider 实现，保证零网络、可 -race。
package testkit

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// MockToolCall describes one scripted tool invocation the fake model should emit.
type MockToolCall struct {
	Name string
	Args map[string]interface{}
}

// MockResponse is one scripted model reply. Exactly one of the fields is used:
//   - ToolCalls non-empty: reply renders tool-call JSON blocks for the parser
//   - Err non-nil: reply fails with this error
//   - otherwise: reply is Text (final answer, no tool calls)
type MockResponse struct {
	Text      string
	ToolCalls []MockToolCall
	Err       error
}

// TextResponse builds a plain final-answer response.
func TextResponse(text string) MockResponse {
	return MockResponse{Text: text}
}

// ToolCallResponse builds a response carrying the given tool calls.
func ToolCallResponse(calls ...MockToolCall) MockResponse {
	return MockResponse{ToolCalls: calls}
}

// ErrorResponse builds a response that fails the model call.
func ErrorResponse(err error) MockResponse {
	return MockResponse{Err: err}
}

// RecordedCall is a snapshot of one invocation of the adapter,
// taken at call time so later mutations of the conversation cannot rewrite history.
type RecordedCall struct {
	SystemPrompt string
	// MessageCount is len(messages) at call time.
	MessageCount int
	// Contents holds message contents in order ("<role>:<content>"),
	// enough for contract assertions without pinning full structs.
	Contents []string
}

// MockAdapter is a scripted, thread-safe ChatFunc.
type MockAdapter struct {
	mu    sync.Mutex
	queue []MockResponse
	calls []RecordedCall
}

// NewMockAdapter creates an adapter that will serve responses in order.
func NewMockAdapter(responses ...MockResponse) *MockAdapter {
	q := make([]MockResponse, len(responses))
	copy(q, responses)
	return &MockAdapter{queue: q}
}

// Func returns a types.ChatFunc backed by this adapter.
// Each call pops one scripted response; when the queue is empty it returns an
// error, so tests fail loudly if the loop calls the model more than scripted.
func (m *MockAdapter) Func() types.ChatFunc {
	return func(systemPrompt string, messages []types.Message) (string, error) {
		m.mu.Lock()
		contents := make([]string, len(messages))
		for i, msg := range messages {
			contents[i] = string(msg.Role) + ":" + msg.Content
		}
		m.calls = append(m.calls, RecordedCall{
			SystemPrompt: systemPrompt,
			MessageCount: len(messages),
			Contents:     contents,
		})
		callNo := len(m.calls)
		if len(m.queue) == 0 {
			m.mu.Unlock()
			return "", fmt.Errorf("MockAdapter: no scripted response left (call #%d)", callNo)
		}
		resp := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()

		if resp.Err != nil {
			return "", resp.Err
		}
		if len(resp.ToolCalls) > 0 {
			return renderToolCalls(resp.ToolCalls)
		}
		return resp.Text, nil
	}
}

// Calls returns a copy of all recorded invocations.
func (m *MockAdapter) Calls() []RecordedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecordedCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// Pending returns how many scripted responses were never consumed.
// A finished test should see 0; non-zero means the loop stopped early
// (or never called the model at all).
func (m *MockAdapter) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

// renderToolCalls renders scripted tool calls as fenced JSON blocks in the
// explicit {"name":..., "arguments":...} form that webai/parser recognizes.
func renderToolCalls(calls []MockToolCall) (string, error) {
	var b strings.Builder
	for i, c := range calls {
		if c.Name == "" {
			return "", fmt.Errorf("MockAdapter: scripted tool call #%d has empty name", i)
		}
		payload := map[string]interface{}{
			"name":      c.Name,
			"arguments": c.Args,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("MockAdapter: marshal tool call %q: %w", c.Name, err)
		}
		b.WriteString("```json\n")
		b.Write(data)
		b.WriteString("\n```\n")
	}
	return b.String(), nil
}
