package autotest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStepValidate_Matrix(t *testing.T) {
	cases := []struct {
		name string
		step Step
		ok   bool
	}{
		{"open ok", Step{Type: StepOpen, URL: "https://x.dev"}, true},
		{"open missing url", Step{Type: StepOpen}, false},
		{"click ok", Step{Type: StepClick, Selector: "button"}, true},
		{"click missing selector", Step{Type: StepClick}, false},
		{"fill ok", Step{Type: StepFill, Selector: "input", Text: "hi"}, true},
		{"fill missing selector", Step{Type: StepFill, Text: "hi"}, false},
		{"assertText ok", Step{Type: StepAssertText, Selector: "h1", Text: "Hi"}, true},
		{"assertText missing selector", Step{Type: StepAssertText, Text: "Hi"}, false},
		{"assertTitle ok", Step{Type: StepAssertTitle, Text: "Home"}, true},
		{"assertTitle missing text", Step{Type: StepAssertTitle}, false},
		{"screenshot bare", Step{Type: StepScreenshot}, true},
		{"wait ms", Step{Type: StepWait, Ms: 50}, true},
		{"wait selector", Step{Type: StepWait, Selector: ".ready"}, true},
		{"wait neither", Step{Type: StepWait}, false},
		{"unknown type", Step{Type: "teleport"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.step.Validate()
			if c.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestScriptValidate(t *testing.T) {
	if err := (Script{Name: "t"}).Validate(); err == nil {
		t.Fatal("empty steps should fail")
	}
	if err := (Script{Name: "", Steps: []Step{{Type: StepOpen, URL: "u"}}}).Validate(); err == nil {
		t.Fatal("empty name should fail")
	}
	if err := (Script{Name: "t", Steps: []Step{{Type: StepOpen, URL: "u"}}}).Validate(); err != nil {
		t.Fatalf("valid script rejected: %v", err)
	}
}

func TestValidateID_BlocksTraversal(t *testing.T) {
	bad := []string{"", "../etc/passwd", "a/b", "a.b", "..", "x y", strings.Repeat("a", 65)}
	for _, id := range bad {
		if ValidateID(id) {
			t.Fatalf("id %q should be rejected", id)
		}
	}
	for _, id := range []string{"a", "login-flow", "test_01", strings.Repeat("a", 64)} {
		if !ValidateID(id) {
			t.Fatalf("id %q should be accepted", id)
		}
	}
}

func TestSaveLoadListDelete_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	sc := Script{
		ID:   "smoke",
		Name: "冒烟测试",
		Steps: []Step{
			{Type: StepOpen, URL: "https://example.com"},
			{Type: StepAssertTitle, Text: "Example"},
			{Type: StepScreenshot, Name: "home"},
		},
	}
	if err := SaveScript(dir, sc); err != nil {
		t.Fatal(err)
	}
	got, err := LoadScript(dir, "smoke")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != sc.Name || len(got.Steps) != 3 || got.Steps[1].Text != "Example" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	list := ListScripts(dir)
	if len(list) != 1 || list[0].ID != "smoke" {
		t.Fatalf("list mismatch: %+v", list)
	}

	// 坏文件不阻塞列表。
	if err := os.WriteFile(filepath.Join(ScriptsDir(dir), "broken.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(ListScripts(dir)) != 1 {
		t.Fatal("broken file should be skipped")
	}

	if err := DeleteScript(dir, "smoke"); err != nil {
		t.Fatal(err)
	}
	if len(ListScripts(dir)) != 0 {
		t.Fatal("delete failed")
	}
	// 删不存在的文件不算错误（幂等）。
	if err := DeleteScript(dir, "ghost"); err != nil {
		t.Fatalf("delete ghost: %v", err)
	}
}

func TestSaveScript_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := SaveScript(dir, Script{ID: "x", Name: "t"}); err == nil {
		t.Fatal("invalid script should not be saved")
	}
	if err := SaveScript(dir, Script{ID: "../evil", Name: "t", Steps: []Step{{Type: StepOpen, URL: "u"}}}); err == nil {
		t.Fatal("traversal id should not be saved")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "evil.json")); !os.IsNotExist(err) {
		t.Fatal("traversal escaped scripts dir")
	}
}

func TestJSONShape_MatchesFrontendContract(t *testing.T) {
	// 前端手写 JSON 源模式时的字段名契约。
	raw := `{"id":"t1","name":"登录","steps":[{"type":"fill","selector":"#user","text":"bob"}]}`
	var sc Script
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Steps[0].Selector != "#user" || sc.Steps[0].Text != "bob" {
		t.Fatalf("unexpected shape: %+v", sc.Steps[0])
	}
	out, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"id":"t1","name":"登录","steps":[{"type":"fill","selector":"#user","text":"bob"}]}` {
		t.Fatalf("marshal mismatch: %s", out)
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"home":             "home",
		"my shot.png":      "my_shot_png",
		"../../etc/passwd": "______etc_passwd",
		"":                 "shot",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Fatalf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStepTimeout_Defaults(t *testing.T) {
	if (Step{}).StepTimeout() != DefaultTimeout {
		t.Fatal("zero timeout should default")
	}
	if (Step{TimeoutMs: 500}).StepTimeout().Milliseconds() != 500 {
		t.Fatal("explicit timeout ignored")
	}
}
