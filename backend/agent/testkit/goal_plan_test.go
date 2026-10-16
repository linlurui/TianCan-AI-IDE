package testkit

import (
	"context"
	"strings"
	"testing"

	agentpkg "github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/goal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/preset"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// roProbeTool is a read-only custom tool used to prove plan mode does NOT blanket-
// block tools — it still executes under deny-all because it never reaches the
// approval path (read-only tools get DecisionAllow).
type roProbeTool struct {
	name     string
	executed *bool
	result   types.ToolResult
}

func (t *roProbeTool) Name() string                         { return t.name }
func (t *roProbeTool) Description() string                  { return "read-only probe" }
func (t *roProbeTool) InputSchema() map[string]interface{} { return map[string]interface{}{} }
func (t *roProbeTool) IsReadOnly() bool                    { return true }
func (t *roProbeTool) IsConcurrencySafe() bool             { return true }
func (t *roProbeTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	*t.executed = true
	return t.result, nil
}

// runNamedProbe registers a fresh non-readonly probe under a unique name and drives
// the real loop through one approval-requiring call. Using a unique name per run
// avoids any registry-replace/append ambiguity across repeated runs on one agent.
func runNamedProbe(t *testing.T, agent *agentpkg.Agent, name string) (*bool, *agentpkg.AgentRunResult) {
	t.Helper()
	executed := new(bool)
	agent.Registry().Register(&approvalProbeTool{
		name:     name,
		executed: executed,
		result:   types.ToolResult{Content: "deployed-ok", Success: true},
	})
	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: name, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("done"),
	)
	res := agent.Run(context.Background(), "deploy", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run not done: %v", res.Error)
	}
	if adapter.Pending() != 0 {
		t.Fatalf("model calls != scripted (pending %d)", adapter.Pending())
	}
	return executed, &res
}

// TestAgent_PlanMode_DeniesApprovalToolViaApprovalSystem G12 核心：计划态不靠拦截工具，
// 而是把批准系统切到 deny-all。证明链：审批=allow 时工具执行 → 进入计划态被拒 →
// 退出计划态审批恢复 allow 工具再次执行。
func TestAgent_PlanMode_DeniesApprovalToolViaApprovalSystem(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")
	// 先把预设切成 allow，使计划态的效果能被单独观察到（否则默认 ask 本身就会拒）。
	if _, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOn, Approval: preset.ApprovalAllow}); err != nil {
		t.Fatal(err)
	}

	// 计划态前：工具执行。
	e1, r1 := runNamedProbe(t, agent, "probe_pre")
	if !*e1 || !hasToolResult(r1, "deployed-ok") {
		t.Fatalf("precondition: tool must execute outside plan mode (approval=allow)")
	}

	// 进入计划态。
	if err := agent.EnterPlanMode("refactor auth"); err != nil {
		t.Fatal(err)
	}
	if agent.GetGoal().Mode != goal.ModePlan {
		t.Fatalf("goal mode must be plan, got %q", agent.GetGoal().Mode)
	}

	// 计划态中：同一工具必须被拒（经由批准 deny-all，而非工具拦截）。
	e2, r2 := runNamedProbe(t, agent, "probe_plan")
	if *e2 {
		t.Fatalf("plan mode must deny the approval-required tool via deny-all, but it executed")
	}
	if !hasToolResult(r2, "Permission denied") {
		t.Fatalf("plan mode denial must surface as Permission denied: %v", r2.Messages)
	}

	// 退出计划态 → 审批恢复 allow → 工具再次执行。
	if err := agent.ExitPlanMode(); err != nil {
		t.Fatal(err)
	}
	if agent.GetGoal().Mode != goal.ModeNone {
		t.Fatalf("goal mode must revert to none, got %q", agent.GetGoal().Mode)
	}
	e3, r3 := runNamedProbe(t, agent, "probe_post")
	if !*e3 || !hasToolResult(r3, "deployed-ok") {
		t.Fatalf("after exiting plan mode the tool must execute again (approval restored)")
	}
}

// TestAgent_PlanMode_SystemPromptParagraph G12：计划态段落只在计划态出现，退出后消失。
func TestAgent_PlanMode_SystemPromptParagraph(t *testing.T) {
	agent := newIsolatedAgent(t)

	runAndCheckPrompt := func(wantPlan bool) {
		adapter := NewMockAdapter(TextResponse("ok"))
		_ = agent.Run(context.Background(), "hi", 3, adapter.Func())
		calls := adapter.Calls()
		if len(calls) == 0 {
			t.Fatal("expected a model call")
		}
		has := strings.Contains(calls[0].SystemPrompt, "Plan Mode")
		if has != wantPlan {
			t.Fatalf("plan paragraph present=%v want %v\nprompt:\n%s", has, wantPlan, calls[0].SystemPrompt)
		}
	}
	runAndCheckPrompt(false)

	if err := agent.EnterPlanMode("draft a plan"); err != nil {
		t.Fatal(err)
	}
	runAndCheckPrompt(true)

	if err := agent.ExitPlanMode(); err != nil {
		t.Fatal(err)
	}
	runAndCheckPrompt(false)
}

// TestAgent_PlanMode_ReadOnlyToolStillRuns G12 反证：计划态不靠「拦截工具」实现，
// 因此只读工具（走 DecisionAllow、根本不进批准路径）在计划态仍执行。若计划态是
// 在工具层做拦截，这个只读工具也会被挡住——本测试会失败，从而钉死「独立执法」语义。
func TestAgent_PlanMode_ReadOnlyToolStillRuns(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")

	exec := new(bool)
	agent.Registry().Register(&roProbeTool{
		name:     "read_cfg",
		executed: exec,
		result:   types.ToolResult{Content: "cfg-ok", Success: true},
	})
	if err := agent.EnterPlanMode("review config"); err != nil {
		t.Fatal(err)
	}

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: "read_cfg", Args: map[string]interface{}{}}),
		TextResponse("done"),
	)
	res := agent.Run(context.Background(), "read config", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run not done: %v", res.Error)
	}
	if !*exec {
		t.Fatalf("read-only tool must still execute in plan mode (enforcement is via approval deny-all, not tool interception)")
	}
	if !hasToolResult(&res, "cfg-ok") {
		t.Fatalf("read-only tool result must be present: %v", res.Messages)
	}
}

// TestAgent_GoalBudgetExhausted_StopsLoop G12：轮次预算耗尽 → 主循环失败关闭停止。
func TestAgent_GoalBudgetExhausted_StopsLoop(t *testing.T) {
	agent := newIsolatedAgent(t)
	if err := agent.SetRoundBudget(1); err != nil {
		t.Fatal(err)
	}
	// 模型会持续发工具调用；预算=1 表示只跑一个轮次即停。
	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: "deploy_widget", Args: map[string]interface{}{"target": "x"}}),
		ToolCallResponse(MockToolCall{Name: "deploy_widget", Args: map[string]interface{}{"target": "y"}}),
		TextResponse("done"),
	)
	res := agent.Run(context.Background(), "do stuff", 10, adapter.Func())
	if res.Error != "goal round budget exhausted" {
		t.Fatalf("expected budget-exhausted stop, got err=%q done=%v", res.Error, res.Done)
	}
	if !res.Done {
		t.Fatal("loop must terminate (fail-closed) when budget exhausted")
	}
}

// TestAgent_GoalBudgetUnlimited_NoStop G12 反证：默认预算=0（无限）时，主循环不应因
// 轮次预算而停止；若 ConsumeRound 被错误地无条件拦截，本测试会失败。这里只验证预算闸门
// 不误触发（工具是否执行取决于审批，与预算无关）。
func TestAgent_GoalBudgetUnlimited_NoStop(t *testing.T) {
	agent := newIsolatedAgent(t)
	// 不调用 SetRoundBudget → 默认 MaxRounds==0（无限）。
	registerApprovalProbe(t, agent)
	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("done"),
	)
	res := agent.Run(context.Background(), "deploy", 5, adapter.Func())
	if res.Error != "" {
		t.Fatalf("unlimited budget must not stop the loop, got err=%q", res.Error)
	}
	if !res.Done {
		t.Fatal("loop should finish normally")
	}
	// 两条脚本应答都应被消费——证明循环跑满两轮、预算闸门未提前拦截。
	if adapter.Pending() != 0 {
		t.Fatalf("both scripted model calls must be consumed (no early round stop), pending=%d", adapter.Pending())
	}
}
