package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// TestWrapWithResourceLimits 表驱动验证 ulimit 前奏生成。
//
// 契约溯源：G4 计划——bash 工具命令前插 RLIMIT_CPU/AS/FSIZE/NPROC。
func TestWrapWithResourceLimits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 无 ulimit 语义")
	}
	tests := []struct {
		name string
		lim  ResourceLimits
		want []string // 必须出现的前奏片段
		notW []string // 不得出现的片段
	}{
		{
			name: "全项限制",
			lim:  ResourceLimits{CPUSeconds: 60, MemoryKB: 1024, FileSizeKB: 2048, MaxProcesses: 64},
			want: []string{"ulimit -t 60", "ulimit -v 1024", "ulimit -f 2048", "ulimit -u 64"},
		},
		{
			name: "部分为零则跳过",
			lim:  ResourceLimits{CPUSeconds: 60},
			want: []string{"ulimit -t 60"},
			notW: []string{"ulimit -v", "ulimit -f", "ulimit -u"},
		},
		{
			name: "全零不加前奏",
			lim:  ResourceLimits{},
			notW: []string{"ulimit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapWithResourceLimits("echo hi", tt.lim)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Fatalf("wrapped command missing %q: %q", w, got)
				}
			}
			for _, nw := range tt.notW {
				if strings.Contains(got, nw) {
					t.Fatalf("wrapped command should not contain %q: %q", nw, got)
				}
			}
			if !strings.HasSuffix(got, "echo hi") {
				t.Fatalf("command body must be preserved verbatim: %q", got)
			}
		})
	}
}

// TestWrapWithResourceLimits_DisableEnv 反证测试：
// TIANCAN_RLIMIT_DISABLE=1 时必须原样返回——若禁用开关失效，本测试必然失败。
func TestWrapWithResourceLimits_DisableEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 无 ulimit 语义")
	}
	t.Setenv("TIANCAN_RLIMIT_DISABLE", "1")
	got := wrapWithResourceLimits("echo hi", DefaultResourceLimits())
	if got != "echo hi" {
		t.Fatalf("disable switch broken: %q", got)
	}
}

// TestBashTool_RlimitBehaviorEvidence 行为证据（仅 unix）：
// 经 BashTool 真实执行，命令内读到的 ulimit 值等于注入值。
func TestBashTool_RlimitBehaviorEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 无 ulimit 语义")
	}
	t.Setenv("TIANCAN_RLIMIT_CPU", "4242")      // 用辨识度高的值
	t.Setenv("TIANCAN_RLIMIT_AS_KB", "0")       // 关闭其余项避免环境差异
	t.Setenv("TIANCAN_RLIMIT_FSIZE_KB", "0")
	t.Setenv("TIANCAN_RLIMIT_NPROC", "0")

	tool := &BashTool{rootPath: t.TempDir()}
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "ulimit -t",
		"timeout": 10,
	})
	if err != nil || !res.Success {
		t.Fatalf("bash execute failed: res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Content, "4242") {
		t.Fatalf("ulimit -t inside sandboxed bash should be 4242, got: %q", res.Content)
	}
}

// TestBashTool_RlimitDisabled_Counterexample 反证测试：
// 禁用后 ulimit -t 不得出现注入值（证明上面的命中不是巧合）。
func TestBashTool_RlimitDisabled_Counterexample(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 无 ulimit 语义")
	}
	t.Setenv("TIANCAN_RLIMIT_DISABLE", "1")
	t.Setenv("TIANCAN_RLIMIT_CPU", "4242")

	tool := &BashTool{rootPath: t.TempDir()}
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "ulimit -t",
		"timeout": 10,
	})
	if err != nil || !res.Success {
		t.Fatalf("bash execute failed: res=%+v err=%v", res, err)
	}
	if strings.Contains(res.Content, "4242") {
		t.Fatalf("limits should NOT be applied when disabled, got: %q", res.Content)
	}
}
