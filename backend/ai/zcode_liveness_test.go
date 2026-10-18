package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestZCodeActivityMonitorDetectsGrowth 验证监控器能感知日志文件的增长。
func TestZCodeActivityMonitorDetectsGrowth(t *testing.T) {
	dir := t.TempDir()
	logf := filepath.Join(dir, "zcode-2026-01-01.jsonl")

	prev := zcodeLogOverride
	zcodeLogOverride = dir
	t.Cleanup(func() { zcodeLogOverride = prev })

	os.WriteFile(logf, []byte("seed\n"), 0o644)
	m := newZCodeActivityMonitor()
	if m.LastActive(); !m.last.IsZero() {
		t.Fatalf("建立基线时不应算作活跃")
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(logf, []byte("seed\nmore\n"), 0o644); err != nil {
		t.Fatalf("追加日志失败: %v", err)
	}
	got := m.LastActive()
	if got.IsZero() {
		t.Fatal("日志增长后应观察到活跃")
	}
	t.Logf("检测到活跃时刻 OK")

	// 停止写入后，活跃时刻不应再前进。
	frozen := m.LastActive()
	time.Sleep(20 * time.Millisecond)
	if after := m.LastActive(); after.After(frozen) {
		t.Fatalf("日志未再增长，活跃时刻不应前进: %v > %v", after, frozen)
	}
}

// writeFakeCLI 生成一个"agent loop 式"的假 CLI：持续写日志但 stdout 一直静默，
// 直到最后才吐出答案 —— 复现 zcode 工具调用阶段的行为。
func writeFakeCLI(t *testing.T, logFile, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-cli.sh")
	body := "#!/bin/sh\nfor i in 1 2 3 4 5 6 7 8 9 10; do\n" +
		"  echo \"tick $i\" >> \"" + logFile + "\"\n" +
		"  sleep 1\n" +
		"done\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("写入假 CLI 失败: %v", err)
	}
	return path
}

// TestFirstOutputUsesLivenessSignal 验证活跃度信号能让「stdout 静默但仍在干活」
// 的任务不被误杀，而缺少该信号时会被首包超时砍掉。
func TestFirstOutputUsesLivenessSignal(t *testing.T) {
	dir := t.TempDir()
	logf := filepath.Join(dir, "zcode-2026-01-01.jsonl")
	os.WriteFile(logf, []byte("seed\n"), 0o644)

	prev := zcodeLogOverride
	zcodeLogOverride = dir
	t.Cleanup(func() { zcodeLogOverride = prev })

	// 10 秒后才输出答案，远超下面给的 3 秒首包阈值。
	bin := writeFakeCLI(t, logf, "echo 'final answer'")

	t.Run("无活跃信号则被首包超时砍掉", func(t *testing.T) {
		s := &Service{}
		start := time.Now()
		_, err := s.chatGenericLocalCLI(context.Background(), "fake", bin,
			cliLimits{FirstOutput: 3 * time.Second, Idle: 3 * time.Second}, nil)
		if err == nil {
			t.Fatal("预期超时，但竟然成功了")
		}
		if !strings.Contains(err.Error(), "首包超时") {
			t.Fatalf("期望首包超时，实得: %v", err)
		}
		t.Logf("无信号时 %v 被判超时（符合预期）", time.Since(start).Round(time.Second))
	})

	t.Run("有活跃信号则等到任务完成", func(t *testing.T) {
		os.WriteFile(logf, []byte("seed\n"), 0o644) // 重置，避免影响基线
		m2 := newZCodeActivityMonitor() // 在启动前建立基线
		start := time.Now()
		s := &Service{}
		got, err := s.chatGenericLocalCLI(context.Background(), "fake", bin,
			cliLimits{FirstOutput: 3 * time.Second, Idle: 3 * time.Second, Liveness: m2.LastActive}, nil)
		if err != nil {
			t.Fatalf("有活跃信号时不应被超时杀掉，实得错误: %v", err)
		}
		if got != "final answer" {
			t.Fatalf("答案不符: %q", got)
		}
		elapsed := time.Since(start)
		if elapsed < 9*time.Second {
			t.Fatalf("似乎没有真正等待到任务结束: %v", elapsed)
		}
		t.Logf("有活跃信号时等待 %v 后拿到答案", elapsed.Round(time.Second))
	})
}
