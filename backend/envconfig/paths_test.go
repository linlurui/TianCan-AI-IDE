package envconfig

import (
	"path/filepath"
	"testing"
)

// TestPaths_Defaults 验证默认路径与历史硬编码完全一致（语义不变的回归闸门）。
//
// 契约溯源：G2 前各调用点直接写 ~/.tiancan、~/.tiancan-ide、~/.tiancan-ai-ide；
// 本测试把这三个默认值钉死，防止收敛时顺手"改名优化"弄丢用户存量数据。
func TestPaths_Defaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got, want := ConfigHome(), filepath.Join(home, ".tiancan"); got != want {
		t.Fatalf("ConfigHome()=%q want %q", got, want)
	}
	if got, want := DataHome(), filepath.Join(home, ".tiancan-ide"); got != want {
		t.Fatalf("DataHome()=%q want %q", got, want)
	}
	if got, want := MemoryHome(), filepath.Join(home, ".tiancan-ai-ide"); got != want {
		t.Fatalf("MemoryHome()=%q want %q", got, want)
	}
	if got, want := ProjectDataDir("/some/root"), filepath.Join("/some/root", ".tiancan"); got != want {
		t.Fatalf("ProjectDataDir()=%q want %q", got, want)
	}
}

// TestPaths_Overrides 验证覆盖口生效。
func TestPaths_Overrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TIANCAN_CONFIG_HOME", "/tmp/x-config")
	t.Setenv("TIANCAN_DATA_HOME", "/tmp/x-data")
	t.Setenv("TIANCAN_MEMORY_DIR", "/tmp/x-memory")

	if got := ConfigHome(); got != "/tmp/x-config" {
		t.Fatalf("ConfigHome override failed: %q", got)
	}
	if got := DataHome(); got != "/tmp/x-data" {
		t.Fatalf("DataHome override failed: %q", got)
	}
	if got := MemoryHome(); got != "/tmp/x-memory" {
		t.Fatalf("MemoryHome override failed: %q", got)
	}
}

// TestPaths_Counterexample_OverrideDoesNotLeak 反证测试：
// 覆盖变量只管自己的目录——设了 TIANCAN_CONFIG_HOME 不得顺手把 DataHome 也改掉。
// 若有人把三个 Home 写成"同一变量管全部"，本测试必然失败。
func TestPaths_Counterexample_OverrideDoesNotLeak(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TIANCAN_CONFIG_HOME", "/tmp/x-config")

	if got, want := DataHome(), filepath.Join(home, ".tiancan-ide"); got != want {
		t.Fatalf("DataHome leaked from CONFIG_HOME override: %q want %q", got, want)
	}
	if got, want := MemoryHome(), filepath.Join(home, ".tiancan-ai-ide"); got != want {
		t.Fatalf("MemoryHome leaked from CONFIG_HOME override: %q want %q", got, want)
	}
}
