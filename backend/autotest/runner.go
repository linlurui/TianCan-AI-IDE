package autotest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Runner executes tests against a local browser.
type Runner struct{}

// RunEvent is one progress message streamed to the UI while a test runs.
type RunEvent struct {
	ScriptID string `json:"scriptId"`
	Type     string `json:"type"` // step | log | error | done
	Index    int    `json:"index,omitempty"`
	Step     string `json:"step,omitempty"`
	Line     string `json:"line,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Ms       int64  `json:"ms,omitempty"`
	Shot     string `json:"shot,omitempty"`
	Total    int    `json:"total,omitempty"`
}

// Reporter receives progress events. It must not block for long.
type Reporter func(RunEvent)

// pollInterval is how often wait-for-element steps re-check the DOM.
const pollInterval = 150 * time.Millisecond

// Run executes every step in order and stops at the first failure.
// ctx cancellation (StopRun) shuts the browser down mid-run.
func (r *Runner) Run(ctx context.Context, sc Script, shotsDir string, rep Reporter) error {
	bin, err := resolveBin()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		return fmt.Errorf("create screenshots dir: %w", err)
	}

	controlURL, cleanup, err := launchBrowser(ctx, bin)
	if err != nil {
		return err
	}
	defer cleanup()

	cdp, err := DialCDP(ctx, controlURL)
	if err != nil {
		return err
	}
	defer cdp.Close()

	// 建标签页并建立扁平会话（后续所有命令带 sessionId）。
	target, err := createTarget(ctx, cdp)
	if err != nil {
		return err
	}
	sid, err := attachSession(ctx, cdp, target)
	if err != nil {
		return err
	}
	page := &pageSession{cdp: cdp, sid: sid}
	// Page.enable：让页面域命令可用（截图需要）。
	if _, err := page.call(ctx, "Page.enable", nil); err != nil {
		return err
	}

	for i, st := range sc.Steps {
		if err := ctx.Err(); err != nil {
			rep(RunEvent{ScriptID: sc.ID, Type: "error", Index: i, Step: st.Type, Line: "已停止"})
			return err
		}
		rep(RunEvent{ScriptID: sc.ID, Type: "step", Index: i, Step: st.Type, Total: len(sc.Steps)})
		start := time.Now()
		shot, err := r.runStep(ctx, page, shotsDir, i, st)
		if err != nil {
			rep(RunEvent{
				ScriptID: sc.ID, Type: "error", Index: i, Step: st.Type,
				Line: err.Error(), Ms: time.Since(start).Milliseconds(), Total: len(sc.Steps),
			})
			return fmt.Errorf("第 %d 步（%s）失败: %w", i+1, st.Type, err)
		}
		rep(RunEvent{
			ScriptID: sc.ID, Type: "log", Index: i, Step: st.Type, OK: true,
			Ms: time.Since(start).Milliseconds(), Shot: shot, Total: len(sc.Steps),
		})
	}
	return nil
}

// pageSession is one attached tab.
type pageSession struct {
	cdp *CDP
	sid string
}

func (p *pageSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.cdp.Call(ctx, p.sid, method, params)
}

// eval runs a JS function body in the page and returns its JSON value.
func (p *pageSession) eval(ctx context.Context, js string) (json.RawMessage, error) {
	res, err := p.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    js,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		return nil, fmt.Errorf("页面脚本错误: %s", out.ExceptionDetails.Text)
	}
	return out.Result.Value, nil
}

// waitElement polls until selector exists or the step budget runs out.
func (p *pageSession) waitElement(ctx context.Context, selector string, timeout time.Duration) error {
	js := fmt.Sprintf(`!!document.querySelector(%s)`, jsString(selector))
	deadline := time.Now().Add(timeout)
	for {
		raw, err := p.eval(ctx, js)
		if err == nil && string(raw) == "true" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待元素 %s 超时（%s）", selector, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// runStep performs one step and returns the screenshot file name, if any.
func (r *Runner) runStep(ctx context.Context, page *pageSession, shotsDir string, idx int, st Step) (string, error) {
	switch st.Type {
	case StepOpen:
		if _, err := page.call(ctx, "Page.navigate", map[string]any{"url": st.URL}); err != nil {
			return "", fmt.Errorf("打开 %s: %w", st.URL, err)
		}
		// 等 document.readyState 到 complete；超时不判失败（长连接站点
		// 永远不 idle，但那不影响后续步骤）。
		waitReady(ctx, page, st.StepTimeout())
		return "", nil

	case StepClick:
		if err := page.waitElement(ctx, st.Selector, st.StepTimeout()); err != nil {
			return "", err
		}
		js := fmt.Sprintf(`(() => {
			const el = document.querySelector(%s);
			if (!el) return "missing";
			el.scrollIntoView({block: "center"});
			el.click();
			return "ok";
		})()`, jsString(st.Selector))
		raw, err := page.eval(ctx, js)
		if err != nil {
			return "", err
		}
		if string(raw) != `"ok"` {
			return "", fmt.Errorf("元素 %s 点击后消失", st.Selector)
		}
		return "", nil

	case StepFill:
		if err := page.waitElement(ctx, st.Selector, st.StepTimeout()); err != nil {
			return "", err
		}
		// 赋 value + 派发 input/change：React/Vue 受控组件需要事件才认账。
		js := fmt.Sprintf(`(() => {
			const el = document.querySelector(%s);
			if (!el) return "missing";
			el.focus();
			el.value = %s;
			el.dispatchEvent(new Event("input", {bubbles: true}));
			el.dispatchEvent(new Event("change", {bubbles: true}));
			return "ok";
		})()`, jsString(st.Selector), jsString(st.Text))
		raw, err := page.eval(ctx, js)
		if err != nil {
			return "", err
		}
		if string(raw) != `"ok"` {
			return "", fmt.Errorf("元素 %s 填写时消失", st.Selector)
		}
		return "", nil

	case StepAssertText:
		if err := page.waitElement(ctx, st.Selector, st.StepTimeout()); err != nil {
			return "", err
		}
		raw, err := page.eval(ctx, fmt.Sprintf(
			`(document.querySelector(%s) || {}).textContent || ""`, jsString(st.Selector)))
		if err != nil {
			return "", err
		}
		var got string
		_ = json.Unmarshal(raw, &got)
		if !strings.Contains(got, st.Text) {
			return "", fmt.Errorf("断言失败: %q 不在 %q 中", st.Text, got)
		}
		return "", nil

	case StepAssertTitle:
		raw, err := page.eval(ctx, `document.title || ""`)
		if err != nil {
			return "", err
		}
		var title string
		_ = json.Unmarshal(raw, &title)
		if !strings.Contains(title, st.Text) {
			return "", fmt.Errorf("断言失败: 标题 %q 不含 %q", title, st.Text)
		}
		return "", nil

	case StepScreenshot:
		res, err := page.call(ctx, "Page.captureScreenshot", map[string]any{
			"format": "png",
		})
		if err != nil {
			return "", err
		}
		var out struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(res, &out); err != nil {
			return "", err
		}
		buf, err := base64.StdEncoding.DecodeString(out.Data)
		if err != nil {
			return "", fmt.Errorf("解码截图: %w", err)
		}
		name := safeName(strings.TrimSpace(st.Name)) + ".png"
		if err := os.WriteFile(filepath.Join(shotsDir, name), buf, 0o644); err != nil {
			return "", err
		}
		return name, nil

	case StepWait:
		if st.Ms > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(st.Ms) * time.Millisecond):
			}
			return "", nil
		}
		if err := page.waitElement(ctx, st.Selector, st.StepTimeout()); err != nil {
			return "", err
		}
		return "", nil
	}
	return "", fmt.Errorf("未知步骤类型 %q", st.Type)
}

// waitReady polls document.readyState until "complete" or the budget ends.
func waitReady(ctx context.Context, page *pageSession, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := page.eval(ctx, `document.readyState`)
		if err == nil {
			var state string
			if json.Unmarshal(raw, &state) == nil && state == "complete" {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// createTarget opens a new tab and returns its target id.
func createTarget(ctx context.Context, c *CDP) (string, error) {
	res, err := c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"})
	if err != nil {
		return "", fmt.Errorf("新建标签页: %w", err)
	}
	var out struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	return out.TargetID, nil
}

// attachSession attaches to a target in flat mode and returns the session id.
func attachSession(ctx context.Context, c *CDP, targetID string) (string, error) {
	res, err := c.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	})
	if err != nil {
		return "", fmt.Errorf("附加到标签页: %w", err)
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	if out.SessionID == "" {
		return "", fmt.Errorf("附加到标签页: 未返回 sessionId")
	}
	return out.SessionID, nil
}

// jsString renders a Go string as a JS string literal (JSON escaping is a
// valid subset for JS string literals).
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// safeName keeps a screenshot name to [A-Za-z0-9_-] and caps its length.
func safeName(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		out = "shot"
	}
	return out
}
