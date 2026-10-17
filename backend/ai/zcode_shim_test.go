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
// 使用 chatMessagesToCLIPrompt（带工具定义与工具指令），与生产完全一致：
// 天蚕是 AI IDE，本地 CLI 档需以 agent 身份用各自原生工具读改项目。
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

	// 复刻生产场景：天蚕会先塞一条含工具清单的系统提示词，再跟用户消息。
	msgs := []ChatMessage{
		{Role: "system", Content: "You are TianCan AI IDE, an expert AI coding assistant.\n\n" +
			"# Available Tools\n- read_file(path)\n- write_file(path, content)\n" +
			"- edit_file(path, old, new)\n- glob(pattern)\n- grep(pattern)\n- bash(command)\n\n" +
			"Project root: /Volumes/workspace/project/Ai/TianCan-AI-IDE\n"},
		{Role: "user", Content: "在吗"},
	}
	prompt := chatMessagesToCLIPrompt(msgs)
	// 设计如此：compactCLISystemPrompt 会剥掉天蚕自己的工具 schema（本地 CLI 用各自
	// 原生工具，天蚕 schema 无用且臃肿），但必须保留「用你的原生工具」指令与项目根目录。
	if strings.Contains(prompt, "read_file(path)") {
		t.Fatal("天蚕工具 schema 未被剥离（应由 compactCLISystemPrompt 处理）")
	}
	if !strings.Contains(prompt, "native coding tools") {
		t.Fatal("丢失「使用原生工具」指令：AI IDE 场景下 CLI 需以 agent 身份干活")
	}
	if !strings.Contains(prompt, "TianCan-AI-IDE") {
		t.Fatal("丢失 Project root：CLI 需要知道项目位置才能读改代码")
	}
	t.Logf("提示词长度: %d（已剥离天蚕工具 schema，保留原生工具指令+项目根）", len(prompt))

	svc := &Service{}
	start := time.Now()
	resp, err := svc.chatGenericLocalCLI(context.Background(), "zcode", bin, 150*time.Second,
		[]string{"-p", prompt, "--no-color", "--mode", "yolo", "--cwd", svc.cliWorkDir()})
	elapsed := time.Since(start).Seconds()
	if err != nil {
		t.Fatalf("zcode 调用失败(%.1fs): %v", elapsed, err)
	}
	if strings.TrimSpace(resp) == "" {
		t.Fatal("zcode 返回空响应")
	}
	// stdout 应只有答案，不应混入 stderr 的 provider 刷新告警。
	if strings.Contains(resp, "刷新失败") {
		t.Fatalf("stderr 噪声混入答案: %q", resp)
	}
	t.Logf("zcode 回答(%.1fs): %s", elapsed, resp)
}
