package autotest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/browser"
)

// browserAvailable reports whether a local browser binary can be resolved.
func browserAvailable() bool { return browser.Available() }

// TestRunner_EndToEnd drives a real browser through a real page. It is gated
// on TIANCAN_TEST_BROWSER=1 because it needs Chrome/Chromium on the machine
// and opens a network connection; unit CI stays hermetic without it.
func TestRunner_EndToEnd(t *testing.T) {
	if os.Getenv("TIANCAN_TEST_BROWSER") != "1" {
		t.Skip("set TIANCAN_TEST_BROWSER=1 to run the real-browser smoke test")
	}
	if !browserAvailable() {
		t.Skip("no local Chrome/Chromium")
	}

	sc := Script{
		ID:   "e2e",
		Name: "端到端冒烟",
		Steps: []Step{
			{Type: StepOpen, URL: "https://example.com", TimeoutMs: 30000},
			{Type: StepAssertTitle, Text: "Example Domain"},
			// example.com 2024 年后改版：不再有 h1，正文是单个 <p>。
			// 无头 Chrome 按 Accept-Language 可能拿到中文页，断言取双语共存的
			// 片段不可靠——用标题断言 + 截图步骤覆盖正文路径即可，这里改断言
			// 标签选择器存在性（任何语言都有 <p>）。
			{Type: StepAssertText, Selector: "p", Text: ""},
			{Type: StepScreenshot, Name: "example-home"},
		},
	}
	if err := sc.Validate(); err != nil {
		t.Fatal(err)
	}

	shots := filepath.Join(t.TempDir(), "screenshots")
	var events []RunEvent
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	err := (&Runner{}).Run(ctx, sc, shots, func(ev RunEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("run failed: %v (events: %+v)", err, events)
	}
	if len(events) == 0 {
		t.Fatal("no progress events")
	}
	// 截图必须真的落了盘。
	if _, err := os.Stat(filepath.Join(shots, "example-home.png")); err != nil {
		t.Fatalf("screenshot missing: %v", err)
	}
}

// TestRunner_AssertFailureStopsRun 验证断言失败会中止后续步骤。
func TestRunner_AssertFailureStopsRun(t *testing.T) {
	if os.Getenv("TIANCAN_TEST_BROWSER") != "1" {
		t.Skip("set TIANCAN_TEST_BROWSER=1 to run the real-browser smoke test")
	}
	if !browserAvailable() {
		t.Skip("no local Chrome/Chromium")
	}

	sc := Script{
		ID:   "e2e-fail",
		Name: "断言失败即停",
		Steps: []Step{
			{Type: StepOpen, URL: "https://example.com", TimeoutMs: 30000},
			{Type: StepAssertTitle, Text: "这个标题不存在"},
			{Type: StepScreenshot, Name: "never-reached"},
		},
	}
	shots := filepath.Join(t.TempDir(), "screenshots")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := (&Runner{}).Run(ctx, sc, shots, func(RunEvent) {})
	if err == nil {
		t.Fatal("expected the failing assert to stop the run")
	}
	if _, statErr := os.Stat(filepath.Join(shots, "never-reached.png")); !os.IsNotExist(statErr) {
		t.Fatal("steps after the failure should not have run")
	}
}
