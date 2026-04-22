package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── TestDiscoverTool ───────────────────────────────────────────

type TestDiscoverTool struct{ rootPath string }

func (t *TestDiscoverTool) Name() string            { return "test_discover" }
func (t *TestDiscoverTool) IsReadOnly() bool        { return true }
func (t *TestDiscoverTool) IsConcurrencySafe() bool { return true }
func (t *TestDiscoverTool) Description() string {
	return "Discover test files and test cases in the project. Args: language (auto-detect), path, pattern"
}
func (t *TestDiscoverTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"language": map[string]interface{}{"type": "string", "description": "go/python/node/rust/java (auto-detect if omitted)"},
			"path":     map[string]interface{}{"type": "string", "description": "Directory to search"},
			"pattern":  map[string]interface{}{"type": "string", "description": "Glob pattern filter"},
		},
	}
}
func (t *TestDiscoverTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	lang, _ := args["language"].(string)
	path, _ := args["path"].(string)
	pattern, _ := args["pattern"].(string)

	if path == "" {
		path = t.rootPath
	} else {
		path = resolvePath(path, t.rootPath)
	}
	if lang == "" {
		lang = detectTestLanguage(t.rootPath, path)
	}

	type TestFile struct {
		Path      string `json:"path"`
		Language  string `json:"language"`
		Framework string `json:"framework"`
		TestCount int    `json:"testCount"`
	}
	var files []TestFile

	switch lang {
	case "go":
		p := filepath.Join(path, "**", "*_test.go")
		if pattern != "" {
			p = pattern
		}
		matches, _ := filepath.Glob(p)
		if len(matches) == 0 {
			matches, _ = filepath.Glob(filepath.Join(path, "*_test.go"))
		}
		for _, m := range matches {
			files = append(files, TestFile{Path: m, Language: "go", Framework: "go test", TestCount: -1})
		}
	case "python":
		patterns := []string{"test_*.py", "*_test.py"}
		if pattern != "" {
			patterns = []string{pattern}
		}
		for _, pt := range patterns {
			matches, _ := filepath.Glob(filepath.Join(path, "**", pt))
			for _, m := range matches {
				fw := "pytest"
				if strings.Contains(m, "unittest") {
					fw = "unittest"
				}
				files = append(files, TestFile{Path: m, Language: "python", Framework: fw, TestCount: -1})
			}
		}
	case "node", "javascript", "typescript":
		patterns := []string{"*.test.js", "*.test.ts", "*.spec.js", "*.spec.ts"}
		if pattern != "" {
			patterns = []string{pattern}
		}
		for _, pt := range patterns {
			matches, _ := filepath.Glob(filepath.Join(path, "**", pt))
			for _, m := range matches {
				fw := "jest"
				if strings.Contains(m, ".spec.") {
					fw = "mocha/jasmine"
				}
				files = append(files, TestFile{Path: m, Language: "node", Framework: fw, TestCount: -1})
			}
		}
	case "rust":
		matches, _ := filepath.Glob(filepath.Join(path, "**", "tests", "*.rs"))
		for _, m := range matches {
			files = append(files, TestFile{Path: m, Language: "rust", Framework: "cargo test", TestCount: -1})
		}
	case "java":
		patterns := []string{"*Test.java", "*IT.java"}
		if pattern != "" {
			patterns = []string{pattern}
		}
		for _, pt := range patterns {
			matches, _ := filepath.Glob(filepath.Join(path, "**", pt))
			for _, m := range matches {
				files = append(files, TestFile{Path: m, Language: "java", Framework: "junit", TestCount: -1})
			}
		}
	default:
		return types.ToolResult{Content: fmt.Sprintf("Unsupported language: %s (go/python/node/rust/java)", lang), Success: false}, nil
	}

	if len(files) == 0 {
		return types.ToolResult{Content: "No test files found", Success: true}, nil
	}
	d, _ := json.MarshalIndent(files, "", "  ")
	return types.ToolResult{Content: string(d), Success: true}, nil
}

// ── TestCoverageTool ───────────────────────────────────────────

type TestCoverageTool struct{ rootPath string }

func (t *TestCoverageTool) Name() string            { return "test_coverage" }
func (t *TestCoverageTool) IsReadOnly() bool        { return false }
func (t *TestCoverageTool) IsConcurrencySafe() bool { return false }
func (t *TestCoverageTool) Description() string {
	return "Run tests with coverage analysis. Args: language, path, timeout"
}
func (t *TestCoverageTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"language": map[string]interface{}{"type": "string"},
			"path":     map[string]interface{}{"type": "string"},
			"timeout":  map[string]interface{}{"type": "integer", "description": "Seconds (default 120)"},
		},
	}
}
func (t *TestCoverageTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	lang, _ := args["language"].(string)
	path, _ := args["path"].(string)
	timeoutSec := toInt(args["timeout"], 120)

	if path == "" {
		path = t.rootPath
	} else {
		path = resolvePath(path, t.rootPath)
	}
	if lang == "" {
		lang = detectTestLanguage(t.rootPath, path)
	}

	var cmdStr string
	switch lang {
	case "go":
		cmdStr = fmt.Sprintf("cd %s && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out", path)
	case "python":
		cmdStr = fmt.Sprintf("cd %s && python -m pytest --cov=. --cov-report=term-missing", path)
	case "node", "javascript", "typescript":
		cmdStr = fmt.Sprintf("cd %s && npx jest --coverage", path)
	case "rust":
		cmdStr = fmt.Sprintf("cd %s && cargo tarpaulin --out Stdout || cargo test -- --nocapture", path)
	default:
		return types.ToolResult{Content: fmt.Sprintf("Coverage not supported for: %s", lang), Success: false}, nil
	}

	out, err := runShellCmd(cmdStr, timeoutSec)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Coverage error: %v\n%s", err, out), Success: false}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// ── TestReportTool ─────────────────────────────────────────────

type TestReportTool struct{ rootPath string }

func (t *TestReportTool) Name() string            { return "test_report" }
func (t *TestReportTool) IsReadOnly() bool        { return true }
func (t *TestReportTool) IsConcurrencySafe() bool { return true }
func (t *TestReportTool) Description() string {
	return "Generate a test report from recent test results. Args: format (text/json), path"
}
func (t *TestReportTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"format": map[string]interface{}{"type": "string", "description": "text or json (default text)"},
			"path":   map[string]interface{}{"type": "string"},
		},
	}
}
func (t *TestReportTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	format, _ := args["format"].(string)
	if format == "" {
		format = "text"
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = t.rootPath
	} else {
		path = resolvePath(path, t.rootPath)
	}

	lang := detectTestLanguage(t.rootPath, path)
	var cmdStr string
	switch lang {
	case "go":
		cmdStr = fmt.Sprintf("cd %s && go test -v -json ./... 2>&1 | tail -200", path)
	case "python":
		cmdStr = fmt.Sprintf("cd %s && python -m pytest -v --tb=short 2>&1 | tail -200", path)
	case "node", "javascript", "typescript":
		cmdStr = fmt.Sprintf("cd %s && npx jest --verbose 2>&1 | tail -200", path)
	case "rust":
		cmdStr = fmt.Sprintf("cd %s && cargo test -- --nocapture 2>&1 | tail -200", path)
	default:
		return types.ToolResult{Content: fmt.Sprintf("Unsupported: %s", lang), Success: false}, nil
	}

	out, err := runShellCmd(cmdStr, 120)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Report error: %v\n%s", err, out), Success: false}, nil
	}

	if format == "json" {
		// Try to parse as structured data
		type Report struct {
			Language string `json:"language"`
			Output   string `json:"output"`
			Success  bool   `json:"success"`
		}
		r := Report{Language: lang, Output: out, Success: err == nil}
		d, _ := json.MarshalIndent(r, "", "  ")
		return types.ToolResult{Content: string(d), Success: true}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// runShellCmd executes a shell command with timeout.
func runShellCmd(cmdStr string, timeoutSec int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), durationFromSec(timeoutSec))
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func durationFromSec(s int) time.Duration {
	if s <= 0 {
		s = 120
	}
	return time.Duration(s) * time.Second
}
