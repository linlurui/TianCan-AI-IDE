package testkit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agentpkg "github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// newIsolatedAgent 构建一个副作用受控的 Agent：HOME 指向临时目录（记忆系统落盘隔离）、
// 禁用 Ralph 自纠错循环（避免测试任务被关键词分流）。
func newIsolatedAgent(t *testing.T) *agentpkg.Agent {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TIANCAN_DISABLE_RALPH", "1")
	return agentpkg.NewAgent(t.TempDir())
}

// loopExpectation 描述对一次 agent 主循环运行的契约期望。
type loopExpectation struct {
	wantDone          bool
	wantErrEmpty      bool
	wantModelCalls    int      // 模型被调用的次数
	wantToolName      string   // 期望被执行的工具（空=纯答复）
	wantToolSuccess   bool     // 工具结果消息的 Success 值
	wantFinalContent  string   // 最后一条 assistant 消息内容
	mustFeedBackTool  bool     // 是否要求第二次模型调用的入参里包含工具结果
	notWantToolResult string   // 消息轨迹中不得出现的工具名
}

// assertLoopContract 校验契约；返回 error 而非直接 t.Fatal，
// 以便反证测试能验证「这个检查器本身会抓错」。
func assertLoopContract(res agentpkg.AgentRunResult, adapter *MockAdapter, tool *GateTool, exp loopExpectation) error {
	if res.Done != exp.wantDone {
		return fmt.Errorf("Done=%v want %v", res.Done, exp.wantDone)
	}
	if exp.wantErrEmpty && res.Error != "" {
		return fmt.Errorf("Error=%q want empty", res.Error)
	}
	if got := len(adapter.Calls()); got != exp.wantModelCalls {
		return fmt.Errorf("model calls=%d want %d", got, exp.wantModelCalls)
	}
	if adapter.Pending() != 0 {
		return fmt.Errorf("pending scripted responses=%d want 0 (loop stopped early)", adapter.Pending())
	}

	var toolMsgs []types.Message
	var lastAssistant string
	for _, m := range res.Messages {
		if m.Role == types.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
		if m.Role == types.RoleAssistant {
			lastAssistant = m.Content
		}
	}

	if exp.wantToolName == "" {
		if len(toolMsgs) != 0 {
			return fmt.Errorf("expected no tool messages, got %d", len(toolMsgs))
		}
	} else {
		var hit *types.Message
		for i := range toolMsgs {
			if toolMsgs[i].ToolName == exp.wantToolName {
				hit = &toolMsgs[i]
			}
		}
		if hit == nil {
			return fmt.Errorf("no tool result message for %q (tool messages: %d)", exp.wantToolName, len(toolMsgs))
		}
		if hit.Success != exp.wantToolSuccess {
			return fmt.Errorf("tool %q result Success=%v want %v (content=%q)",
				exp.wantToolName, hit.Success, exp.wantToolSuccess, hit.Content)
		}
		if exp.wantToolSuccess && tool != nil && tool.CallCount() != 1 {
			return fmt.Errorf("tool %q executed %d times want 1", exp.wantToolName, tool.CallCount())
		}
	}

	if exp.notWantToolResult != "" {
		for _, m := range toolMsgs {
			if m.ToolName == exp.notWantToolResult {
				return fmt.Errorf("unexpected tool result for %q", exp.notWantToolResult)
			}
		}
	}

	if lastAssistant != exp.wantFinalContent {
		return fmt.Errorf("final assistant content=%q want %q", lastAssistant, exp.wantFinalContent)
	}

	if exp.mustFeedBackTool {
		calls := adapter.Calls()
		if len(calls) < 2 {
			return fmt.Errorf("need ≥2 model calls to check tool-result feedback, got %d", len(calls))
		}
		fed := false
		for _, c := range calls[1].Contents {
			if strings.HasPrefix(c, "tool:") && strings.Contains(c, exp.wantToolName) {
				fed = true
			}
		}
		if !fed {
			return fmt.Errorf("second model call did not receive tool result for %q", exp.wantToolName)
		}
	}
	return nil
}

// TestAgentLoop_Contract 主循环契约测试（表驱动）：
// 经真实 Agent.Run + MockAdapter + GateTool 跑通
// 「模型发工具调用 → 工具执行 → 结果消息落轨迹 → 结果回灌模型 → 最终答复」全链路。
//
// 契约溯源：agent.go Run 主循环的消息追加顺序（user → assistant → tool → assistant）
// 与 parseToolCallFromResponse 对显式 JSON 工具调用的识别。
func TestAgentLoop_Contract(t *testing.T) {
	tests := []struct {
		name     string
		toolName string // 空 = 不注册工具、纯答复场景
		script   []MockResponse
		exp      loopExpectation
	}{
		{
			name:     "单工具调用并回灌",
			toolName: "probe_tool",
			script: []MockResponse{
				ToolCallResponse(MockToolCall{Name: "probe_tool", Args: map[string]interface{}{"value": "x"}}),
				TextResponse("探针完成"),
			},
			exp: loopExpectation{
				wantDone:         true,
				wantErrEmpty:     true,
				wantModelCalls:   2,
				wantToolName:     "probe_tool",
				wantToolSuccess:  true,
				wantFinalContent: "探针完成",
				mustFeedBackTool: true,
			},
		},
		{
			name:     "未知工具落失败消息",
			toolName: "",
			script: []MockResponse{
				ToolCallResponse(MockToolCall{Name: "no_such_tool", Args: nil}),
				TextResponse("工具不存在"),
			},
			exp: loopExpectation{
				wantDone:          true,
				wantErrEmpty:      true,
				wantModelCalls:    2,
				wantToolName:      "no_such_tool",
				wantToolSuccess:   false,
				wantFinalContent:  "工具不存在",
				mustFeedBackTool:  true,
				notWantToolResult: "probe_tool",
			},
		},
		{
			name:     "无工具调用直接答复",
			toolName: "probe_tool",
			script:   []MockResponse{TextResponse("直接回答")},
			exp: loopExpectation{
				wantDone:         true,
				wantErrEmpty:     true,
				wantModelCalls:   1,
				wantFinalContent: "直接回答",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := newIsolatedAgent(t)
			var gate *GateTool
			if tt.toolName != "" {
				gate = NewGateTool(tt.toolName, WithConcurrencySafe(true), WithReadOnly(true), WithAutoRelease())
				agent.Registry().Register(gate)
			}
			adapter := NewMockAdapter(tt.script...)

			res := agent.Run(context.Background(), "请看一下这个探针", 5, adapter.Func())

			if err := assertLoopContract(res, adapter, gate, tt.exp); err != nil {
				t.Fatalf("contract violated: %v", err)
			}
		})
	}
}

// TestAgentLoop_Counterexample_ContractCatchesMissingTool 反证测试：
// 契约检查器必须能抓「模型没调工具 / 工具没执行」。这里脚本只有纯文本应答，
// 却用「期望工具执行成功」的契约去验——检查器必须报错。
// 若有人把 assertLoopContract 改成「只查 Done」，本测试必然失败。
func TestAgentLoop_Counterexample_ContractCatchesMissingTool(t *testing.T) {
	agent := newIsolatedAgent(t)
	gate := NewGateTool("probe_tool", WithConcurrencySafe(true), WithAutoRelease())
	agent.Registry().Register(gate)
	adapter := NewMockAdapter(TextResponse("没调工具就回答了"))

	res := agent.Run(context.Background(), "请看一下这个探针", 5, adapter.Func())

	err := assertLoopContract(res, adapter, gate, loopExpectation{
		wantDone:         true,
		wantErrEmpty:     true,
		wantModelCalls:   1,
		wantToolName:     "probe_tool", // 故意期望一次根本没发生的工具执行
		wantToolSuccess:  true,
		wantFinalContent: "没调工具就回答了",
	})
	if err == nil {
		t.Fatal("contract checker must catch a missing tool execution, but it passed")
	}
}
