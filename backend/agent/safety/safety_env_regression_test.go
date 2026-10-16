package safety

import (
	"context"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// dummyTool 是回归测试用的最小工具实现。
type dummyTool struct{ name string }

func (d dummyTool) Name() string                       { return d.name }
func (d dummyTool) Description() string                { return "dummy" }
func (d dummyTool) InputSchema() map[string]interface{} { return nil }
func (d dummyTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	return types.ToolResult{Success: true}, nil
}
func (d dummyTool) IsReadOnly() bool        { return true }
func (d dummyTool) IsConcurrencySafe() bool { return true }

// TestEnvDenyTools_Regression 行为回归（G2）：
// TIANCAN_DENY_TOOLS 经 envconfig.EnvList 收敛后语义不变——被点名工具必须被 deny。
//
// 契约溯源：G2 前 safety.go loadRulesFromEnv 直接 os.Getenv 解析逗号列表。
func TestEnvDenyTools_Regression(t *testing.T) {
	t.Setenv("TIANCAN_DENY_TOOLS", "probe_tool, another_tool")

	s := NewSafetySystem()
	res, _ := s.CheckPermission(dummyTool{"probe_tool"}, nil, t.TempDir())
	if res.Decision != types.DecisionDeny {
		t.Fatalf("probe_tool decision=%q want deny", res.Decision)
	}

	res2, _ := s.CheckPermission(dummyTool{"another_tool"}, nil, t.TempDir())
	if res2.Decision != types.DecisionDeny {
		t.Fatalf("another_tool decision=%q want deny", res2.Decision)
	}
}

// TestEnvDenyTools_Counterexample 反证测试：
// 未点名的工具不得被误伤；env 未设置时不得凭空产生 deny 规则。
// 若 EnvList 把空串解析成 [""] 或收敛改错了 key，本测试必然失败。
func TestEnvDenyTools_Counterexample(t *testing.T) {
	t.Setenv("TIANCAN_DENY_TOOLS", "probe_tool")

	s := NewSafetySystem()
	res, _ := s.CheckPermission(dummyTool{"innocent_tool"}, nil, t.TempDir())
	if res.Decision == types.DecisionDeny {
		t.Fatalf("innocent_tool was denied; env list parsing is broken (decision=%q)", res.Decision)
	}
}

// TestEnvAutoMode_Regression 验证 TIANCAN_AUTO_MODE 经 EnvBool 收敛后生效。
func TestEnvAutoMode_Regression(t *testing.T) {
	t.Setenv("TIANCAN_AUTO_MODE", "1")
	s := NewSafetySystem()
	if !s.config.AutoMode {
		t.Fatal("TIANCAN_AUTO_MODE=1 should enable AutoMode")
	}
}
