package envconfig

import (
	"os"
	"reflect"
	"testing"
)

// TestEnvBool 表驱动验证布尔语义：兼容历史 "1"，接受 true/yes/on，其余一律 false。
//
// 契约溯源：G2 前各调用点写作 os.Getenv(KEY)=="1"（或 !="1"）；
// EnvBool 是该语义的兼容超集。
func TestEnvBool(t *testing.T) {
	tests := []struct {
		value string
		set   bool
		want  bool
	}{
		{"1", true, true},
		{"true", true, true},
		{"TRUE", true, true},
		{"yes", true, true},
		{"on", true, true},
		{" 1 ", true, true}, // 带空白
		{"0", true, false},
		{"false", true, false},
		{"2", true, false}, // 反证：非约定真值不得误判为 true
		{"", true, false},
		{"", false, false}, // 未设置
	}
	for _, tt := range tests {
		name := tt.value
		if !tt.set {
			name = "<unset>"
		}
		t.Run("value="+name, func(t *testing.T) {
			restore := SetEnvLookupForTest(func(key string) string {
				if !tt.set {
					return ""
				}
				return tt.value
			})
			defer restore()
			if got := EnvBool("TEST_BOOL"); got != tt.want {
				t.Fatalf("EnvBool(%q)=%v want %v", tt.value, got, tt.want)
			}
		})
	}
}

// TestEnvInt 验证整型语义：未设置/解析失败回退默认值。
func TestEnvInt(t *testing.T) {
	tests := []struct {
		value string
		def   int
		want  int
	}{
		{"5", 3, 5},
		{"0", 3, 0},
		{"-2", 3, -2},
		{"abc", 3, 3},   // 反证：非法值必须回退默认，不得为 0 或 panic
		{"", 3, 3},      // 未设置
		{" 7 ", 3, 7},   // 带空白可解析
		{"3.5", 3, 3},   // 浮点串按非法处理
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			restore := SetEnvLookupForTest(func(string) string { return tt.value })
			defer restore()
			if got := EnvInt("TEST_INT", tt.def); got != tt.want {
				t.Fatalf("EnvInt(%q, %d)=%d want %d", tt.value, tt.def, got, tt.want)
			}
		})
	}
}

// TestEnvFloat 验证浮点语义。
func TestEnvFloat(t *testing.T) {
	restore := SetEnvLookupForTest(func(key string) string {
		if key == "GOOD" {
			return "8.5"
		}
		return "garbage"
	})
	defer restore()
	if got := EnvFloat("GOOD", 0); got != 8.5 {
		t.Fatalf("EnvFloat(GOOD)=%v want 8.5", got)
	}
	// 反证：非法值回退默认，不得为 0 伪装成"未配置"
	if got := EnvFloat("BAD", 4.0); got != 4.0 {
		t.Fatalf("EnvFloat(BAD)=%v want fallback 4.0", got)
	}
}

// TestEnvList 验证逗号列表语义：去空白、丢空元素、未设置返回 nil。
func TestEnvList(t *testing.T) {
	tests := []struct {
		value string
		set   bool
		want  []string
	}{
		{"a,b,c", true, []string{"a", "b", "c"}},
		{" a , , b ", true, []string{"a", "b"}},
		{"", true, nil},   // 反证：空串必须等同未设置（调用点以 nil 判断"无覆盖"）
		{"", false, nil},  // 未设置
		{",,,", true, nil}, // 反证：全是空元素不得产出 [""] 这类脏数据
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			restore := SetEnvLookupForTest(func(string) string {
				if !tt.set {
					return ""
				}
				return tt.value
			})
			defer restore()
			got := EnvList("TEST_LIST")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("EnvList(%q)=%v want %v", tt.value, got, tt.want)
			}
		})
	}
}

// TestEnvLookupRestore 反证测试：测试钩子必须可恢复，
// 否则一个测试的伪造会泄漏到其它测试（测试基座自身的防泄漏闸门）。
func TestEnvLookupRestore(t *testing.T) {
	key := "TIANCAN_G2_RESTORE_PROBE"
	os.Setenv(key, "real-value")
	defer os.Unsetenv(key)

	restore := SetEnvLookupForTest(func(string) string { return "fake" })
	if got := EnvString(key); got != "fake" {
		t.Fatalf("fake lookup not in effect, got %q", got)
	}
	restore()
	if got := EnvString(key); got != "real-value" {
		t.Fatalf("restore broken: got %q want real-value", got)
	}
}

// TestEnvDefaultIsOsGetenv 证明生产路径默认走真实环境变量。
func TestEnvDefaultIsOsGetenv(t *testing.T) {
	key := "TIANCAN_G2_OSGETENV_PROBE"
	os.Setenv(key, "from-os")
	defer os.Unsetenv(key)
	if got := EnvString(key); got != "from-os" {
		t.Fatalf("EnvString should read real env by default, got %q", got)
	}
}
