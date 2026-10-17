package ai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestZCodeCLI_Live 走真实调用路径验证 zcode 可用：
// ensureZCodeCLI 准备可执行镜像 → chatGenericLocalCLI 非交互调用 → 断言拿到非空回答。
// 依赖本机安装 ZCode.app 与网络，故默认 opt-in：TIANCAN_LIVE_ZCODE=1 才真跑。
func TestZCodeCLI_Live(t *testing.T) {
	if os.Getenv("TIANCAN_LIVE_ZCODE") != "1" {
		t.Skip("set TIANCAN_LIVE_ZCODE=1 to run the live zcode check")
	}

	bin := ensureZCodeCLI()
	if bin == "" {
		t.Fatal("ensureZCodeCLI 返回空：未发现 ZCode.app 或 PATH 上的 zcode")
	}
	if !fileExists(bin) {
		t.Fatalf("ensureZCodeCLI 返回的路径不存在: %s", bin)
	}
	t.Logf("zcode 入口: %s", bin)

	svc := &Service{}
	resp, err := svc.chatGenericLocalCLI(context.Background(), "zcode", bin, 90*time.Second,
		[]string{"-p", "Reply with exactly: ZCODE_LIVE_OK", "--no-color", "--mode", "yolo", "--cwd", svc.cliWorkDir()})
	if err != nil {
		t.Fatalf("zcode 调用失败: %v", err)
	}
	if strings.TrimSpace(resp) == "" {
		t.Fatal("zcode 返回空响应")
	}
	// stdout 应只有答案，不应混入 stderr 的 provider 刷新告警。
	if strings.Contains(resp, "刷新失败") {
		t.Fatalf("stderr 噪声混入答案: %q", resp)
	}
	t.Logf("zcode 回答: %s", resp)
	if !strings.Contains(resp, "ZCODE_LIVE_OK") {
		t.Fatalf("回答内容不符预期: %q", resp)
	}
}
