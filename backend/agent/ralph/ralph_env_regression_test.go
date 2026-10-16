package ralph

import (
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

func noopChat(systemPrompt string, messages []types.Message) (string, error) { return "ok", nil }

// TestEnvDisableRalph_Regression 行为回归（G2）：
// TIANCAN_DISABLE_RALPH=1 经 envconfig.EnvBool 收敛后语义不变。
//
// 契约溯源：G2 前 ralph.go 写作 os.Getenv("TIANCAN_DISABLE_RALPH") != "1"。
func TestEnvDisableRalph_Regression(t *testing.T) {
	t.Setenv("TIANCAN_DISABLE_RALPH", "1")
	if NewRalphLoop(noopChat, nil).IsEnabled() {
		t.Fatal("TIANCAN_DISABLE_RALPH=1 should disable the loop")
	}
}

// TestEnvDisableRalph_Counterexample 反证测试：
// "0"/"true 以外的值"/未设置 不得禁用——若收敛把语义改成"非空即禁用"，本测试必然失败。
func TestEnvDisableRalph_Counterexample(t *testing.T) {
	for _, v := range []string{"0", "false", "2", ""} {
		t.Run("value="+v, func(t *testing.T) {
			if v != "" {
				t.Setenv("TIANCAN_DISABLE_RALPH", v)
			}
			if !NewRalphLoop(noopChat, nil).IsEnabled() {
				t.Fatalf("TIANCAN_DISABLE_RALPH=%q must NOT disable the loop", v)
			}
		})
	}
}

// TestEnvRalphMaxIter_Regression 验证 TIANCAN_RALPH_MAX_ITER 覆盖语义（非法值回退默认）。
func TestEnvRalphMaxIter_Regression(t *testing.T) {
	t.Setenv("TIANCAN_RALPH_MAX_ITER", "7")
	rl := NewRalphLoop(noopChat, nil)
	if rl.maxIter != 7 {
		t.Fatalf("maxIter=%d want 7", rl.maxIter)
	}

	t.Setenv("TIANCAN_RALPH_MAX_ITER", "garbage")
	rl = NewRalphLoop(noopChat, nil)
	if rl.maxIter != MaxIterations {
		t.Fatalf("garbage value should fall back to default %d, got %d", MaxIterations, rl.maxIter)
	}
}
