package testkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentpkg "github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/approval"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// approvalProbeTool is a non-readonly custom tool that triggers DecisionAsk in
// the default safety configuration (no deny/ask/allow rule, not read-only).
// It records whether Execute actually ran.
type approvalProbeTool struct {
	name     string
	executed *bool
	result   types.ToolResult
}

func (t *approvalProbeTool) Name() string                         { return t.name }
func (t *approvalProbeTool) Description() string                  { return "probe tool for approval tests" }
func (t *approvalProbeTool) InputSchema() map[string]interface{} { return map[string]interface{}{} }
func (t *approvalProbeTool) IsReadOnly() bool                    { return false }
func (t *approvalProbeTool) IsConcurrencySafe() bool             { return false }
func (t *approvalProbeTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	*t.executed = true
	return t.result, nil
}

const approvalProbeName = "deploy_widget"

// registerApprovalProbe registers a fresh probe tool and returns the executed flag.
func registerApprovalProbe(t *testing.T, agent *agentpkg.Agent) *bool {
	t.Helper()
	executed := new(bool)
	agent.Registry().Register(&approvalProbeTool{
		name:     approvalProbeName,
		executed: executed,
		result:   types.ToolResult{Content: "deployed-ok", Success: true},
	})
	return executed
}

func hasToolResult(res *agentpkg.AgentRunResult, needle string) bool {
	for _, m := range res.Messages {
		if m.Role == types.RoleTool && strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

// TestAgentRun_ApprovalAllowedOnce_ExecutesTool G8 核心：允许 → 工具真正执行，
// 结果进入对话，循环正常收尾。
func TestAgentRun_ApprovalAllowedOnce_ExecutesTool(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	executed := registerApprovalProbe(t, agent)
	agent.SetApprovalSystem(approval.NewApprovalSystem(approval.NewPermissiveAnswerer()))

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("done"),
	)
	res := agent.Run(context.Background(), "部署到生产", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if !*executed {
		t.Fatalf("allowed-once 必须真正执行工具，但工具未被调用")
	}
	if !hasToolResult(&res, "deployed-ok") {
		t.Fatalf("工具结果未进入对话: %v", res.Messages)
	}
	if adapter.Pending() != 0 {
		t.Fatalf("模型调用次数与脚本不符（剩余 %d 条未消费）", adapter.Pending())
	}
}

// TestAgentRun_ApprovalUnavailable_DeniedFailClosed G8 核心：非交互默认（unavailable）
// 必须失败关闭 → 拒绝，且工具绝不执行。
func TestAgentRun_ApprovalUnavailable_DeniedFailClosed(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	executed := registerApprovalProbe(t, agent)
	agent.SetApprovalSystem(approval.NewDefault()) // rejecting answerer → Unavailable

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("ok"),
	)
	res := agent.Run(context.Background(), "部署到生产", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if *executed {
		t.Fatalf("unavailable 必须失败关闭、拒绝执行，但工具被调用了")
	}
	if !hasToolResult(&res, "Permission denied") {
		t.Fatalf("unavailable 必须以 Permission denied 形式拒绝: %v", res.Messages)
	}
}

// TestAgentRun_ApprovalNever_BeforeDispatch G8 核心：never 策略在任何 dispatch 之前
// 决定 → 即便 answerer 是 permissive 也必须被拒，且工具不执行（answerer 未被问询）。
func TestAgentRun_ApprovalNever_BeforeDispatch(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	executed := registerApprovalProbe(t, agent)

	sys := approval.NewApprovalSystem(approval.NewPermissiveAnswerer()) // 若 never 失效则会放行
	sys.SetNever([]string{approvalProbeName})
	agent.SetApprovalSystem(sys)

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("ok"),
	)
	res := agent.Run(context.Background(), "部署到生产", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if *executed {
		t.Fatalf("never 策略必须在 dispatch 之前拒绝，但工具被调用了")
	}
	if !hasToolResult(&res, "Permission denied") {
		t.Fatalf("never 必须拒绝（最终落到 fail-closed 文案）: %v", res.Messages)
	}
}

// TestAgentRun_ApprovalAnswererError_FailClosed G8 核心：answerer 抛错 → 强制
// unavailable（失败关闭），工具不执行。
func TestAgentRun_ApprovalAnswererError_FailClosed(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	executed := registerApprovalProbe(t, agent)
	agent.SetApprovalSystem(approval.NewApprovalSystem(approval.ErroringAnswerer{Err: errors.New("boom")}))

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("ok"),
	)
	res := agent.Run(context.Background(), "部署到生产", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if *executed {
		t.Fatalf("answerer 出错必须失败关闭，但工具被调用了")
	}
	if !hasToolResult(&res, "Permission denied") {
		t.Fatalf("answerer 错误必须落到 fail-closed 拒绝: %v", res.Messages)
	}
}

// TestAgentRun_ApprovalOutOfSetCoerced 反证：若 answerer 返回闭集外的值，必须被
// 强制收口为 unavailable（失败关闭），闭集不可被撑大。
func TestAgentRun_ApprovalOutOfSetCoerced(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	executed := registerApprovalProbe(t, agent)

	sys := approval.NewApprovalSystem(weirdApprovalAnswerer{out: "maybe-allow"})
	agent.SetApprovalSystem(sys)

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("ok"),
	)
	res := agent.Run(context.Background(), "部署到生产", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if *executed {
		t.Fatalf("闭集外的值必须收口为拒绝，但工具被调用了（闭集被撑大！）")
	}
	if !hasToolResult(&res, "Permission denied") {
		t.Fatalf("闭集外值必须失败关闭: %v", res.Messages)
	}
}

// TestAgentRunStream_ApprovalAllowedOnce_ExecutesTool 验证 RunStream 路径走同一
// 共享 executeRegisteredTool，允许时工具同样执行（路径一致性契约）。
func TestAgentRunStream_ApprovalAllowedOnce_ExecutesTool(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	executed := registerApprovalProbe(t, agent)
	agent.SetApprovalSystem(approval.NewApprovalSystem(approval.NewPermissiveAnswerer()))

	streamFn := func(systemPrompt string, messages []types.Message, onToken func(string)) (string, error) {
		// 第一次返回工具调用，第二次（已带工具结果）返回最终文本
		for _, m := range messages {
			if m.Role == types.RoleTool && m.ToolName == approvalProbeName {
				onToken("done")
				return "done", nil
			}
		}
		// 渲染一次工具调用（匹配 webai/parser）
		return "```json\n" + `{"name":"deploy_widget","arguments":{"target":"prod"}}` + "\n```\n", nil
	}

	ch, err := agent.RunStream(context.Background(), "部署到生产", 5, streamFn)
	if err != nil {
		t.Fatal(err)
	}
	var sawToolResult, sawToolCall bool
	for ev := range ch {
		if ev.Type == "tool_result" {
			sawToolResult = true
			if !strings.Contains(ev.Content, "deployed-ok") {
				t.Fatalf("RunStream 工具结果异常: %q", ev.Content)
			}
		}
		if ev.Type == "tool_call" && ev.Name == approvalProbeName {
			sawToolCall = true
		}
	}
	if !*executed {
		t.Fatalf("RunStream 下 allowed-once 也必须执行工具")
	}
	if !sawToolCall || !sawToolResult {
		t.Fatalf("RunStream 应同时出现 tool_call 与 tool_result（got call=%v result=%v）", sawToolCall, sawToolResult)
	}
}

// weirdApprovalAnswerer returns a value outside the closed set.
type weirdApprovalAnswerer struct{ out string }

func (a weirdApprovalAnswerer) Answer(ctx context.Context, req approval.Request) (approval.Outcome, error) {
	return approval.Outcome(a.out), nil
}
