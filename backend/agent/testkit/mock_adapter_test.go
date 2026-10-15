package testkit

import (
	"errors"
	"strings"
	"testing"

	webparser "github.com/linlurui/tiancan-ai-ide/backend/webai/parser"
)

// TestMockAdapter_TableDriven covers the MockAdapter contract:
// scripted order, recording, rendering, and exhaustion errors.
//
// 契约溯源：对标 dsh「脚本化模型应答」测试基座——应答必须按脚本顺序消费、
// 且每次调用的入参可回溯断言。
func TestMockAdapter_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		responses []MockResponse
		// 每条脚本依次调用一次，逐条断言
		wantOutputs []string
		wantTool    []string // 期望解析出的首个工具名（空串=纯文本应答）
		wantErrAt   int      // 第几次调用应报错（0=不报错）
	}{
		{
			name:        "纯文本应答按序返回",
			responses:   []MockResponse{TextResponse("hello"), TextResponse("world")},
			wantOutputs: []string{"hello", "world"},
			wantTool:    []string{"", ""},
		},
		{
			name: "工具调用应答可被 parser 解析",
			responses: []MockResponse{
				ToolCallResponse(MockToolCall{Name: "probe_tool", Args: map[string]interface{}{"value": "x"}}),
				TextResponse("done"),
			},
			wantOutputs: []string{"", "done"},
			wantTool:    []string{"probe_tool", ""},
		},
		{
			name:      "错误应答原样返回",
			responses: []MockResponse{ErrorResponse(errors.New("boom"))},
			wantErrAt: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewMockAdapter(tt.responses...)
			fn := adapter.Func()
			for i := range tt.responses {
				callNo := i + 1
				out, err := fn("sys", nil)
				if tt.wantErrAt == callNo {
					if err == nil {
						t.Fatalf("call %d: expected error, got nil", callNo)
					}
					continue
				}
				if err != nil {
					t.Fatalf("call %d: unexpected error: %v", callNo, err)
				}
				wantTool := tt.wantTool[i]
				if wantTool == "" {
					if out != tt.wantOutputs[i] {
						t.Fatalf("call %d: output=%q want %q", callNo, out, tt.wantOutputs[i])
					}
					continue
				}
				calls := webparser.ParseToolCalls(out)
				if len(calls) == 0 {
					t.Fatalf("call %d: expected parser to find tool call, output=%q", callNo, out)
				}
				if calls[0].Name != wantTool {
					t.Fatalf("call %d: tool=%q want %q", callNo, calls[0].Name, wantTool)
				}
			}
			if adapter.Pending() != 0 {
				t.Fatalf("pending=%d want 0", adapter.Pending())
			}
			if got := len(adapter.Calls()); got != len(tt.responses) {
				t.Fatalf("recorded calls=%d want %d", got, len(tt.responses))
			}
		})
	}
}

// TestMockAdapter_RecordsSnapshots 验证每次调用的消息快照被独立记录，
// 后续会话变化不会改写历史记录。
func TestMockAdapter_RecordsSnapshots(t *testing.T) {
	adapter := NewMockAdapter(TextResponse("a"), TextResponse("b"))
	fn := adapter.Func()

	if _, err := fn("sys1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fn("sys2", nil); err != nil {
		t.Fatal(err)
	}

	calls := adapter.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls=%d want 2", len(calls))
	}
	if calls[0].SystemPrompt != "sys1" || calls[1].SystemPrompt != "sys2" {
		t.Fatalf("system prompts recorded incorrectly: %+v", calls)
	}
}

// TestMockAdapter_ExhaustedFails 反证测试：
// 脚本耗尽后继续调用必须报错。若有人把「耗尽报错」改成「返回空串/重复最后一条」，
// 本测试必然失败——证明 MockAdapter 的防呆闸门是真的。
func TestMockAdapter_ExhaustedFails(t *testing.T) {
	adapter := NewMockAdapter(TextResponse("only-once"))
	fn := adapter.Func()

	if _, err := fn("sys", nil); err != nil {
		t.Fatalf("first call should succeed: %v", err)
	}
	_, err := fn("sys", nil)
	if err == nil {
		t.Fatal("second call must fail when script is exhausted")
	}
	if !strings.Contains(err.Error(), "no scripted response left") {
		t.Fatalf("error should explain exhaustion, got: %v", err)
	}
}

// TestMockAdapter_EmptyToolNameFails 反证测试：
// 脚本里写空工具名属于作者笔误，必须在渲染期报错而不是静默产出
// 一个 parser 解析不出的字符串（否则上层测试会「假绿」——以为模型没调工具）。
func TestMockAdapter_EmptyToolNameFails(t *testing.T) {
	adapter := NewMockAdapter(ToolCallResponse(MockToolCall{Name: "", Args: nil}))
	_, err := adapter.Func()("sys", nil)
	if err == nil {
		t.Fatal("empty tool name must fail at render time")
	}
}
