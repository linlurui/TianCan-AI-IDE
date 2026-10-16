package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/tools/browser"
)

// stubManager wraps a fake session to drive BrowserTool without a real browser.
type stubManager struct {
	session *browserStub
	closed  bool
}

func (m *stubManager) NewSession() (string, browser.Session, error) {
	return "s1", m.session, nil
}
func (m *stubManager) GetSession(id string) (browser.Session, error) {
	if id == "s1" {
		return m.session, nil
	}
	return nil, browser.ErrSessionNotFound
}
func (m *stubManager) CloseAll() error { m.closed = true; return nil }

// browserStub records calls and enforces the guard exactly like rodSession.
type browserStub struct {
	navigated []string
	clicks    []string
	typed     []string
	filled    []string
	screenshot bool
	html      string
	text      string
	title     string
	evalOut   string
}

func (s *browserStub) Navigate(_ context.Context, url string) error {
	s.navigated = append(s.navigated, url)
	return nil
}
func (s *browserStub) Click(_ context.Context, sel string) error {
	s.clicks = append(s.clicks, sel)
	return nil
}
func (s *browserStub) Type(_ context.Context, sel, text string) error {
	s.typed = append(s.typed, sel+"=>"+text)
	return nil
}
func (s *browserStub) Fill(_ context.Context, sel, text string) error {
	s.filled = append(s.filled, sel+"=>"+text)
	return nil
}
func (s *browserStub) Screenshot(context.Context) ([]byte, error) {
	s.screenshot = true
	return []byte("PNGDATA"), nil
}
func (s *browserStub) Evaluate(_ context.Context, expr string) (string, error) {
	if ok, _ := browser.IsSyntheticInputScript(expr); ok {
		return "", browser.ErrSyntheticInput
	}
	return s.evalOut, nil
}
func (s *browserStub) HTML(context.Context) (string, error) { return s.html, nil }
func (s *browserStub) Text(context.Context) (string, error) { return s.text, nil }
func (s *browserStub) Title(context.Context) (string, error) { return s.title, nil }
func (s *browserStub) Close() error                         { return nil }

func newStubTool() (*BrowserTool, *browserStub) {
	stub := &browserStub{}
	t := &BrowserTool{}
	t.SetManager(&stubManager{session: stub})
	return t, stub
}

// payload strips the "[session: id]" banner the tool appends to auto-created
// sessions, so assertions can compare against the action's own output.
func payload(content string) string {
	if i := strings.Index(content, "\n[session: "); i >= 0 {
		return content[:i]
	}
	return content
}

func TestBrowserTool_Navigate(t *testing.T) {
	tl, stub := newStubTool()
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "navigate", "url": "https://x.test"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Success || len(stub.navigated) != 1 || stub.navigated[0] != "https://x.test" {
		t.Fatalf("navigate not dispatched: res=%+v stub=%+v", res, stub)
	}
	// A fresh session id is reported to the caller.
	if !strings.Contains(res.Content, "[session: s1]") {
		t.Fatalf("expected session id in content, got %q", res.Content)
	}
}

func TestBrowserTool_ClickTypeFill(t *testing.T) {
	tl, stub := newStubTool()
	if _, err := tl.Execute(context.Background(), map[string]interface{}{"action": "click", "selector": "#b"}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, err := tl.Execute(context.Background(), map[string]interface{}{"action": "type", "selector": "#i", "text": "hi"}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if _, err := tl.Execute(context.Background(), map[string]interface{}{"action": "fill", "selector": "#i", "text": "yo"}); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if len(stub.clicks) != 1 || stub.clicks[0] != "#b" {
		t.Fatalf("click mismatch: %v", stub.clicks)
	}
	if len(stub.typed) != 1 || stub.typed[0] != "#i=>hi" {
		t.Fatalf("type mismatch: %v", stub.typed)
	}
	if len(stub.filled) != 1 || stub.filled[0] != "#i=>yo" {
		t.Fatalf("fill mismatch: %v", stub.filled)
	}
}

func TestBrowserTool_Screenshot(t *testing.T) {
	tl, stub := newStubTool()
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "screenshot"})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if !stub.screenshot || !strings.Contains(res.Content, "base64") {
		t.Fatalf("screenshot content wrong: %q", res.Content)
	}
}

func TestBrowserTool_EvaluateSafe(t *testing.T) {
	tl, stub := newStubTool()
	stub.evalOut = "Example Title"
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "evaluate", "expr": "document.title"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if payload(res.Content) != "Example Title" {
		t.Fatalf("evaluate content: %q", res.Content)
	}
}

func TestBrowserTool_EvaluateSyntheticBlocked(t *testing.T) {
	tl, _ := newStubTool()
	res, err := tl.Execute(context.Background(), map[string]interface{}{
		"action": "evaluate",
		"expr":   "document.getElementById('b').dispatchEvent(new Event('click'))",
	})
	if err != nil {
		t.Fatalf("execute should not return a Go error (tool surfaces via ToolResult): %v", err)
	}
	if res.Success {
		t.Fatalf("synthetic evaluate must fail: %+v", res)
	}
	if !strings.Contains(res.Error, "synthesize") {
		t.Fatalf("expected synthetic-input rejection, got success=%v err=%q", res.Success, res.Error)
	}
}

func TestBrowserTool_ReadOnlyAccessors(t *testing.T) {
	tl, stub := newStubTool()
	stub.html = "<html></html>"
	stub.text = "body text"
	stub.title = "T"
	if res, _ := tl.Execute(context.Background(), map[string]interface{}{"action": "html"}); payload(res.Content) != "<html></html>" {
		t.Fatalf("html: %q", res.Content)
	}
	if res, _ := tl.Execute(context.Background(), map[string]interface{}{"action": "text"}); payload(res.Content) != "body text" {
		t.Fatalf("text: %q", res.Content)
	}
	if res, _ := tl.Execute(context.Background(), map[string]interface{}{"action": "title"}); payload(res.Content) != "T" {
		t.Fatalf("title: %q", res.Content)
	}
}

func TestBrowserTool_MissingAction(t *testing.T) {
	tl, _ := newStubTool()
	if _, err := tl.Execute(context.Background(), map[string]interface{}{}); err == nil {
		t.Fatal("missing action should error")
	}
}

func TestBrowserTool_UnknownAction(t *testing.T) {
	tl, _ := newStubTool()
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "frobnicate"})
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if res.Success {
		t.Fatalf("unknown action must fail: %+v", res)
	}
}
