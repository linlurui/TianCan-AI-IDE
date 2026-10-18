package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestApplyZCodeProviderOrderKeepsHealthyHead 锁住一条重要约束：
// 探针不能无条件把 API-key 档顶到 providerOrder 首位。
//
// 反例就是本次真实事故：GLM-5.3 只绑在账号型 provider
// （account:bigmodel-individual-coding-plan，OAuth 无从探测）上，
// 一旦被 API-key 档挤下去，zcode 为选用模型找不到 provider，直接
// "Model creation failed"，把本来能用的链路打断。
func TestApplyZCodeProviderOrderKeepsHealthyHead(t *testing.T) {
	const head = "account:bigmodel-individual-coding-plan"
	const keyed = "new-provider"

	writeCfg := func(order []string) string {
		t.Helper()
		return writeTempProviderCfg(t, order)
	}

	t.Run("首位未被判失败则保持原顺序", func(t *testing.T) {
		p := writeCfg([]string{head, keyed})
		if err := applyZCodeProviderOrder(keyed, map[string]bool{}); err != nil {
			t.Fatalf("apply 失败: %v", err)
		}
		got := readOrder(t, p)
		if len(got) != 2 || got[0] != head {
			t.Fatalf("首位仍可用时不应重排，实得: %v", got)
		}
		t.Logf("保持原顺序 OK: %v", got)
	})

	t.Run("首位明确探失败才把它挪走", func(t *testing.T) {
		p := writeCfg([]string{keyed, head})
		if err := applyZCodeProviderOrder(head, map[string]bool{keyed: true}); err != nil {
			t.Fatalf("apply 失败: %v", err)
		}
		got := readOrder(t, p)
		if len(got) != 2 || got[0] != head || got[1] != keyed {
			t.Fatalf("首位失败时应把它挪后，实得: %v", got)
		}
		t.Logf("失败档挪后 OK: %v", got)
	})
}

func writeTempProviderCfg(t *testing.T, order []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider_config.json")
	doc := map[string]interface{}{
		"config": map[string]interface{}{"providerOrder": order},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	prev := zcodeProviderPathOverride
	zcodeProviderPathOverride = path
	t.Cleanup(func() { zcodeProviderPathOverride = prev })
	return path
}

func readOrder(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var doc struct {
		Config struct {
			ProviderOrder []string `json:"providerOrder"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return doc.Config.ProviderOrder
}
