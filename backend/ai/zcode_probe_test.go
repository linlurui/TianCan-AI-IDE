package ai

import (
	"testing"
	"time"
)

// TestZCodeCredentialProbe 验证凭证探针：能读出带 key 的凭证，并并发挑出最快可用的那个。
// 关键是并发 —— 总耗时应当接近「最慢那一个探针」而不是所有探针耗时之和，
// 这正是替代「一个一个轮」的意义。
func TestZCodeCredentialProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("需要网络探测，短模式跳过")
	}
	creds, err := loadZCodeCredentials()
	if err != nil {
		t.Skipf("读不到 zcode 凭证配置，跳过: %v", err)
	}
	if len(creds) == 0 {
		t.Skip("没有带 api-key 的自定义凭证，跳过")
	}
	for _, c := range creds {
		t.Logf("候选凭证: %s (%s) %s models=%v", c.ID, c.Name, c.BaseURL, c.Models)
	}

	start := time.Now()
	out := probeZCodeCredentials(creds)
	wall := time.Since(start)
	if out == nil || out.best == nil {
		t.Fatalf("所有凭证都不可用（%d 个候选）", len(creds))
	}
	best := out.best
	t.Logf("选中: %s (%s) %s", best.ID, best.Name, best.BaseURL)
	for id := range out.failed {
		t.Logf("判定不可用: %s", id)
	}
	t.Logf("并发探针总耗时: %v（候选 %d 个；若串行则至少是各耗时之和）", wall.Round(time.Millisecond), len(creds))

	// 单个探针上限 8s，并发后总耗时应远小于 个数×8s。
	if wall > time.Duration(len(creds))*probeTimeout {
		t.Fatalf("探针没有并发执行：总耗时 %v 超过串行上界 %v", wall, time.Duration(len(creds))*probeTimeout)
	}
}
