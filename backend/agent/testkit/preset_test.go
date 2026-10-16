package testkit

import (
	"context"
	"testing"

	agentpkg "github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/preset"
)

// runProbeTool drives the real agent loop through a single call to the
// non-readonly probe tool and returns whether the tool actually executed plus
// the run result (so callers can assert on the tool-result text).
func runProbeTool(t *testing.T, agent *agentpkg.Agent) (*bool, *agentpkg.AgentRunResult) {
	t.Helper()
	executed := registerApprovalProbe(t, agent)
	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: approvalProbeName, Args: map[string]interface{}{"target": "prod"}}),
		TextResponse("done"),
	)
	res := agent.Run(context.Background(), "部署到生产", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if adapter.Pending() != 0 {
		t.Fatalf("模型调用次数与脚本不符（剩余 %d 条未消费）", adapter.Pending())
	}
	return executed, &res
}

// TestAgentRun_PresetApprovalAllow_ExecutesTool G10 核心：预设 approval=allow 必须
// 真正把批准系统切成 permissive，探针工具得以执行，结果进入对话。
func TestAgentRun_PresetApprovalAllow_ExecutesTool(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")

	changed, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOn, Approval: preset.ApprovalAllow})
	if err != nil {
		t.Fatalf("ApplyKnobs error: %v", err)
	}
	if !changed {
		t.Fatalf("switching to {on,allow} should be a real change")
	}

	executed, res := runProbeTool(t, agent)
	if !*executed {
		t.Fatalf("approval=allow 预设必须允许工具执行，但工具未被调用（接线失效）")
	}
	if !hasToolResult(res, "deployed-ok") {
		t.Fatalf("工具结果未进入对话: %v", res.Messages)
	}
}

// TestAgentRun_PresetApprovalNever_DeniesAll G10 核心：预设 approval=never 必须切成
// deny-all，任何工具都被拒、绝不执行。
func TestAgentRun_PresetApprovalNever_DeniesAll(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")

	if _, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOn, Approval: preset.ApprovalNever}); err != nil {
		t.Fatalf("ApplyKnobs error: %v", err)
	}

	executed, res := runProbeTool(t, agent)
	if *executed {
		t.Fatalf("approval=never 预设必须拒绝一切，但工具被调用了")
	}
	if !hasToolResult(res, "Permission denied") {
		t.Fatalf("approval=never 必须以 Permission denied 形式拒绝: %v", res.Messages)
	}
}

// TestAgentRun_PresetApprovalAsk_DeniedFailClosed G10 核心：预设 approval=ask（默认，
// 与初始 auto 元组相同 → 无写入）必须保持失败关闭，拒绝非交互调用。
func TestAgentRun_PresetApprovalAsk_DeniedFailClosed(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")

	// 初始状态就是 auto={on,ask}，再 apply 一次应为无操作（changed=false）。
	changed, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOn, Approval: preset.ApprovalAsk})
	if err != nil {
		t.Fatalf("ApplyKnobs error: %v", err)
	}
	if changed {
		t.Fatalf("re-applying the initial auto tuple must be a no-op (changed=false)")
	}

	executed, res := runProbeTool(t, agent)
	if *executed {
		t.Fatalf("approval=ask 预设必须失败关闭、拒绝执行，但工具被调用了")
	}
	if !hasToolResult(res, "Permission denied") {
		t.Fatalf("approval=ask 必须落到 fail-closed 拒绝: %v", res.Messages)
	}
}

// TestAgent_PresetSandboxKnobRecordedAndCustomName G10 核心：sandbox 旋钮被记录；
// 偏离任何具名预设的元组解析为 `custom`；重复 apply 同元组不重复写入。
func TestAgent_PresetSandboxKnobRecordedAndCustomName(t *testing.T) {
	agent := newIsolatedAgent(t)

	// {off, allow} 不匹配任何具名预设 → active name 应为 custom。
	changed, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOff, Approval: preset.ApprovalAllow})
	if err != nil {
		t.Fatalf("ApplyKnobs error: %v", err)
	}
	if !changed {
		t.Fatalf("switching to {off,allow} should be a real change")
	}
	if agent.SandboxMode() != preset.SandboxOff {
		t.Fatalf("sandbox 旋钮未生效: %q", agent.SandboxMode())
	}
	if agent.ActivePresetName() != preset.PresetCustom {
		t.Fatalf("偏离元组的 active name 应为 custom，实际 %q", agent.ActivePresetName())
	}

	// 再次 apply 同一元组 → 无写入（changed=false）。
	changedAgain, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOff, Approval: preset.ApprovalAllow})
	if err != nil {
		t.Fatalf("ApplyKnobs error: %v", err)
	}
	if changedAgain {
		t.Fatalf("重复 apply 同元组必须为无操作（changed=false）")
	}
}

// TestAgent_PresetReturnToAuto G10 核心：从偏离元组切回 auto 是真实变更，active name 回到 auto。
func TestAgent_PresetReturnToAuto(t *testing.T) {
	agent := newIsolatedAgent(t)
	if _, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOff, Approval: preset.ApprovalAllow}); err != nil {
		t.Fatal(err)
	}
	changed, err := agent.ApplyKnobs(preset.KnobsAuto)
	if err != nil {
		t.Fatalf("ApplyKnobs error: %v", err)
	}
	if !changed {
		t.Fatalf("切回 auto 应是真实变更")
	}
	if agent.ActivePresetName() != preset.PresetAuto {
		t.Fatalf("切回后 active name 应为 auto，实际 %q", agent.ActivePresetName())
	}
}

// TestAgent_PresetApprovalKnobSwitchRoundTrip 反证/行为：allow→never 的实时切换必须
// 立即改变工具执行结果——证明预设确实重配置而非只改状态。若 ApplyApproval 退化成
// 空操作，则 allow 之后的 never 仍会放行，本测试失败。
func TestAgent_PresetApprovalKnobSwitchRoundTrip(t *testing.T) {
	agent := newIsolatedAgent(t)
	t.Setenv("TIANCAN_BYPASS_PERMISSIONS", "0")
	t.Setenv("TIANCAN_AUTO_MODE", "0")

	// allow → 工具执行
	if _, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOn, Approval: preset.ApprovalAllow}); err != nil {
		t.Fatal(err)
	}
	execAllow, _ := runProbeTool(t, agent)
	if !*execAllow {
		t.Fatalf("allow 阶段工具应执行")
	}

	// never → 同一工具被拒
	if _, err := agent.ApplyKnobs(preset.Knobs{Sandbox: preset.SandboxOn, Approval: preset.ApprovalNever}); err != nil {
		t.Fatal(err)
	}
	execNever, res := runProbeTool(t, agent)
	if *execNever {
		t.Fatalf("never 阶段工具必须被拒（实时切换失效）")
	}
	if !hasToolResult(res, "Permission denied") {
		t.Fatalf("never 阶段应被拒: %v", res.Messages)
	}
}
