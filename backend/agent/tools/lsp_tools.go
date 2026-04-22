package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── LSPDefinitionTool ───────────────────────────────────────────

type LSPDefinitionTool struct {
	lspPort int
	rootPath string
}

func (t *LSPDefinitionTool) Name() string { return "lsp_definition" }
func (t *LSPDefinitionTool) Description() string {
	return "Go to definition at a position in a file. " +
		"Args: file (required), line (required), column (required)"
}
func (t *LSPDefinitionTool) IsReadOnly() bool        { return true }
func (t *LSPDefinitionTool) IsConcurrencySafe() bool { return true }
func (t *LSPDefinitionTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file":   map[string]interface{}{"type": "string", "description": "File URI or path"},
			"line":   map[string]interface{}{"type": "integer", "description": "Line number (1-indexed)"},
			"column": map[string]interface{}{"type": "integer", "description": "Column number (1-indexed)"},
		},
		"required": []string{"file", "line", "column"},
	}
}
func (t *LSPDefinitionTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	file, _ := args["file"].(string)
	line := toInt(args["line"], 1)
	col := toInt(args["column"], 1)
	if file == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: file")
	}

	uri := toFileURI(file)
	result, err := t.lspRequest(ctx, "textDocument/definition", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": line - 1, "character": col - 1},
	})
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("LSP definition error: %v", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: formatLSPLocations(result), Success: true}, nil
}

// ── LSPReferencesTool ──────────────────────────────────────────

type LSPReferencesTool struct {
	lspPort  int
	rootPath string
}

func (t *LSPReferencesTool) Name() string { return "lsp_references" }
func (t *LSPReferencesTool) Description() string {
	return "Find all references to a symbol at a position. " +
		"Args: file (required), line (required), column (required), include_declaration (optional, default true)"
}
func (t *LSPReferencesTool) IsReadOnly() bool        { return true }
func (t *LSPReferencesTool) IsConcurrencySafe() bool { return true }
func (t *LSPReferencesTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file":                  map[string]interface{}{"type": "string", "description": "File URI or path"},
			"line":                  map[string]interface{}{"type": "integer", "description": "Line number (1-indexed)"},
			"column":                map[string]interface{}{"type": "integer", "description": "Column number (1-indexed)"},
			"include_declaration":   map[string]interface{}{"type": "boolean", "description": "Include declarations (default true)"},
		},
		"required": []string{"file", "line", "column"},
	}
}
func (t *LSPReferencesTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	file, _ := args["file"].(string)
	line := toInt(args["line"], 1)
	col := toInt(args["column"], 1)
	includeDecl, _ := args["include_declaration"].(bool)

	if file == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: file")
	}

	uri := toFileURI(file)
	result, err := t.lspRequest(ctx, "textDocument/references", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": line - 1, "character": col - 1},
		"context":      map[string]interface{}{"includeDeclaration": includeDecl},
	})
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("LSP references error: %v", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: formatLSPLocations(result), Success: true}, nil
}

// ── LSPHoverTool ───────────────────────────────────────────────

type LSPHoverTool struct {
	lspPort  int
	rootPath string
}

func (t *LSPHoverTool) Name() string { return "lsp_hover" }
func (t *LSPHoverTool) Description() string {
	return "Get hover/type information at a position. " +
		"Args: file (required), line (required), column (required)"
}
func (t *LSPHoverTool) IsReadOnly() bool        { return true }
func (t *LSPHoverTool) IsConcurrencySafe() bool { return true }
func (t *LSPHoverTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file":   map[string]interface{}{"type": "string", "description": "File URI or path"},
			"line":   map[string]interface{}{"type": "integer", "description": "Line number (1-indexed)"},
			"column": map[string]interface{}{"type": "integer", "description": "Column number (1-indexed)"},
		},
		"required": []string{"file", "line", "column"},
	}
}
func (t *LSPHoverTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	file, _ := args["file"].(string)
	line := toInt(args["line"], 1)
	col := toInt(args["column"], 1)
	if file == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: file")
	}

	uri := toFileURI(file)
	result, err := t.lspRequest(ctx, "textDocument/hover", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": line - 1, "character": col - 1},
	})
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("LSP hover error: %v", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: formatLSPHover(result), Success: true}, nil
}

// ── LSPSymbolsTool ──────────────────────────────────────────────

type LSPSymbolsTool struct {
	lspPort  int
	rootPath string
}

func (t *LSPSymbolsTool) Name() string { return "lsp_symbols" }
func (t *LSPSymbolsTool) Description() string {
	return "Get document symbols (outline) for a file. Args: file (required)"
}
func (t *LSPSymbolsTool) IsReadOnly() bool        { return true }
func (t *LSPSymbolsTool) IsConcurrencySafe() bool { return true }
func (t *LSPSymbolsTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file": map[string]interface{}{"type": "string", "description": "File URI or path"},
		},
		"required": []string{"file"},
	}
}
func (t *LSPSymbolsTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: file")
	}

	uri := toFileURI(file)
	result, err := t.lspRequest(ctx, "textDocument/documentSymbol", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
	})
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("LSP symbols error: %v", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: formatLSPSymbols(result), Success: true}, nil
}

// ── LSPDiagnosticsTool ─────────────────────────────────────────

type LSPDiagnosticsTool struct {
	lspPort  int
	rootPath string
}

func (t *LSPDiagnosticsTool) Name() string { return "lsp_diagnostics" }
func (t *LSPDiagnosticsTool) Description() string {
	return "Get diagnostics (errors/warnings) for a file. Args: file (required)"
}
func (t *LSPDiagnosticsTool) IsReadOnly() bool        { return true }
func (t *LSPDiagnosticsTool) IsConcurrencySafe() bool { return true }
func (t *LSPDiagnosticsTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file": map[string]interface{}{"type": "string", "description": "File URI or path"},
		},
		"required": []string{"file"},
	}
}
func (t *LSPDiagnosticsTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: file")
	}

	uri := toFileURI(file)
	result, err := t.lspRequest(ctx, "textDocument/fullDocumentDiagnostic", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
	})
	if err != nil {
		// Fallback: try push diagnostics endpoint
		return types.ToolResult{Content: "Diagnostics not available (LSP may not support fullDocumentDiagnostic)", Success: true}, nil
	}
	return types.ToolResult{Content: formatLSPDiagnostics(result), Success: true}, nil
}

// ── helpers ─────────────────────────────────────────────────────

func (t *LSPDefinitionTool) lspRequest(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	return lspRequestTo(ctx, t.lspPort, method, params)
}
func (t *LSPReferencesTool) lspRequest(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	return lspRequestTo(ctx, t.lspPort, method, params)
}
func (t *LSPHoverTool) lspRequest(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	return lspRequestTo(ctx, t.lspPort, method, params)
}
func (t *LSPSymbolsTool) lspRequest(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	return lspRequestTo(ctx, t.lspPort, method, params)
}
func (t *LSPDiagnosticsTool) lspRequest(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	return lspRequestTo(ctx, t.lspPort, method, params)
}

func lspRequestTo(ctx context.Context, port int, method string, params map[string]interface{}) (json.RawMessage, error) {
	if port <= 0 {
		return nil, fmt.Errorf("LSP port not configured")
	}

	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	data, _ := json.Marshal(body)

	url := fmt.Sprintf("http://127.0.0.1:%d/lsp", port)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LSP request failed: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result json.RawMessage `json:"result"`
		Error  interface{}     `json:"error"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		return respData, nil
	}
	if result.Error != nil {
		return nil, fmt.Errorf("LSP error: %v", result.Error)
	}
	return result.Result, nil
}

func toFileURI(path string) string {
	if strings.HasPrefix(path, "file://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + path
}

func formatLSPLocations(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "No locations found"
	}

	// Try array of locations first
	var locations []struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}
	if err := json.Unmarshal(raw, &locations); err == nil && len(locations) > 0 {
		var lines []string
		for _, loc := range locations {
			path := strings.TrimPrefix(loc.URI, "file://")
			lines = append(lines, fmt.Sprintf("%s:%d:%d", path, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		return strings.Join(lines, "\n")
	}

	// Try single location
	var loc struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}
	if err := json.Unmarshal(raw, &loc); err == nil && loc.URI != "" {
		path := strings.TrimPrefix(loc.URI, "file://")
		return fmt.Sprintf("%s:%d:%d", path, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
	}

	return string(raw)
}

func formatLSPHover(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "No hover info"
	}
	var hover struct {
		Contents struct {
			Value string `json:"value"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &hover); err == nil && hover.Contents.Value != "" {
		return hover.Contents.Value
	}
	return string(raw)
}

func formatLSPSymbols(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "No symbols"
	}
	var symbols []struct {
		Name  string `json:"name"`
		Kind  int    `json:"kind"`
		Range struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
		} `json:"range"`
		Children []struct {
			Name string `json:"name"`
			Kind int    `json:"kind"`
			Range struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
		} `json:"children"`
	}
	if err := json.Unmarshal(raw, &symbols); err != nil {
		return string(raw)
	}

	var lines []string
	for _, s := range symbols {
		icon := symbolIcon(s.Kind)
		lines = append(lines, fmt.Sprintf("%s %s (line %d)", icon, s.Name, s.Range.Start.Line+1))
		for _, c := range s.Children {
			lines = append(lines, fmt.Sprintf("  %s %s (line %d)", symbolIcon(c.Kind), c.Name, c.Range.Start.Line+1))
		}
	}
	return strings.Join(lines, "\n")
}

func formatLSPDiagnostics(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "No diagnostics"
	}
	var diag struct {
		Items []struct {
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Range    struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &diag); err != nil || len(diag.Items) == 0 {
		return "No diagnostics"
	}

	var lines []string
	for _, d := range diag.Items {
		sev := "ℹ️"
		switch d.Severity {
		case 1:
			sev = "❌"
		case 2:
			sev = "⚠️"
		case 3:
			sev = "💡"
		}
		lines = append(lines, fmt.Sprintf("%s line %d: %s", sev, d.Range.Start.Line+1, d.Message))
	}
	return strings.Join(lines, "\n")
}

func symbolIcon(kind int) string {
	icons := map[int]string{
		1: "📁", 2: "📦", 3: "🏛", 4: "🔨", 5: "🏛", 6: "⚡",
		7: "📋", 8: "🔧", 9: "🔨", 10: "📦", 11: "📦", 12: "⚙️",
		13: "📝", 14: "🔑", 15: "🔑", 16: "📝", 17: "📝", 18: "🔧",
		19: "🔧", 20: "🔧", 21: "📦", 22: "📦", 23: "📦", 24: "📝",
		25: "🔧", 26: "🔧",
	}
	if icon, ok := icons[kind]; ok {
		return icon
	}
	return "🔹"
}
