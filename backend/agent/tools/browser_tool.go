package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/tools/browser"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// BrowserTool drives a real (rod/Chrome) browser. All input is produced as
// trusted CDP events; the generic `evaluate` action is additionally guarded by
// browser.IsSyntheticInputScript so isTrusted=false synthetic input can never
// reach a page (the isTrusted hard wall).
//
// The tool is intentionally not read-only: per the fail-closed safety model
// (G4/G8) every browser action must pass the approval gate before execution.
type BrowserTool struct {
	mgr browser.Manager
}

// NewBrowserTool creates a BrowserTool backed by a rod-managed browser.
func NewBrowserTool() *BrowserTool {
	return &BrowserTool{mgr: browser.NewManager()}
}

// SetManager swaps the session manager (used by tests with a fake).
func (t *BrowserTool) SetManager(m browser.Manager) { t.mgr = m }

func (t *BrowserTool) Name() string { return "browser" }

func (t *BrowserTool) Description() string {
	return "Drive a real browser via rod (pure-Go CDP). Actions: " +
		"navigate (url), click/type/fill (selector[, text]), screenshot, " +
		"evaluate (expr — read-only JS only; synthetic input is blocked), " +
		"html, text, title, close (session_id). Optional session_id reuses a page; " +
		"otherwise a new session is created and its id returned."
}

func (t *BrowserTool) IsReadOnly() bool        { return false }
func (t *BrowserTool) IsConcurrencySafe() bool { return false }

func (t *BrowserTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "description": "navigate|click|type|fill|screenshot|evaluate|html|text|title|close"},
			"session_id": map[string]interface{}{"type": "string", "description": "Reuse an existing page session (returned by a prior call)"},
			"url":        map[string]interface{}{"type": "string", "description": "URL for navigate"},
			"selector":   map[string]interface{}{"type": "string", "description": "CSS selector for click/type/fill"},
			"text":       map[string]interface{}{"type": "string", "description": "Text for type/fill"},
			"expr":       map[string]interface{}{"type": "string", "description": "JavaScript expression for evaluate (read-only; synthetic input blocked)"},
		},
		"required": []string{"action"},
	}
}

func (t *BrowserTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: action")
	}

	// Resolve the session: reuse an explicit id, else open a fresh one.
	var (
		session Session
		sid     string
		created bool
	)
	if id, _ := args["session_id"].(string); id != "" {
		s, err := t.mgr.GetSession(id)
		if err != nil {
			return types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		session = s
		sid = id
	} else {
		newID, s, err := t.mgr.NewSession()
		if err != nil {
			return types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		session = s
		sid = newID
		created = true
	}

	content, err := t.run(ctx, session, action, args)
	if err != nil {
		return types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if created {
		content = fmt.Sprintf("%s\n[session: %s]", content, sid)
	}
	return types.ToolResult{Content: content, Success: true}, nil
}

func (t *BrowserTool) run(ctx context.Context, s Session, action string, args map[string]interface{}) (string, error) {
	switch action {
	case "navigate":
		url, _ := args["url"].(string)
		if url == "" {
			return "", fmt.Errorf("navigate requires 'url'")
		}
		if err := s.Navigate(ctx, url); err != nil {
			return "", err
		}
		return fmt.Sprintf("navigated to %s", url), nil

	case "click":
		sel := requireSelector(args)
		if sel == "" {
			return "", fmt.Errorf("click requires 'selector'")
		}
		if err := s.Click(ctx, sel); err != nil {
			return "", err
		}
		return fmt.Sprintf("clicked %s", sel), nil

	case "type":
		sel, text := requireSelector(args), argString(args, "text")
		if sel == "" {
			return "", fmt.Errorf("type requires 'selector'")
		}
		if err := s.Type(ctx, sel, text); err != nil {
			return "", err
		}
		return fmt.Sprintf("typed into %s", sel), nil

	case "fill":
		sel, text := requireSelector(args), argString(args, "text")
		if sel == "" {
			return "", fmt.Errorf("fill requires 'selector'")
		}
		if err := s.Fill(ctx, sel, text); err != nil {
			return "", err
		}
		return fmt.Sprintf("filled %s", sel), nil

	case "screenshot":
		data, err := s.Screenshot(ctx)
		if err != nil {
			return "", err
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		return fmt.Sprintf("PNG screenshot (base64, %d bytes):\n%s", len(data), b64), nil

	case "evaluate":
		expr, _ := args["expr"].(string)
		if expr == "" {
			return "", fmt.Errorf("evaluate requires 'expr'")
		}
		out, err := s.Evaluate(ctx, expr)
		if err != nil {
			return "", err
		}
		return out, nil

	case "html":
		html, err := s.HTML(ctx)
		if err != nil {
			return "", err
		}
		return html, nil

	case "text":
		text, err := s.Text(ctx)
		if err != nil {
			return "", err
		}
		return text, nil

	case "title":
		title, err := s.Title(ctx)
		if err != nil {
			return "", err
		}
		return title, nil

	case "close":
		if err := s.Close(); err != nil {
			return "", err
		}
		return "session closed", nil

	default:
		return "", fmt.Errorf("unknown browser action: %s", action)
	}
}

func requireSelector(args map[string]interface{}) string { return argString(args, "selector") }

func argString(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// ensure the alias compiles even if the concrete rod session type is unavailable
// in a given build; the Manager always returns the browser.Session interface.
var _ browser.Session = (Session)(nil)

// Session is an alias to the browser package's Session interface so the tool code
// reads cleanly without importing the browser package name everywhere.
type Session = browser.Session
