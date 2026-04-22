package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── TestRunTool ─────────────────────────────────────────────────

type TestRunTool struct{ rootPath string }

func (t *TestRunTool) Name() string { return "test_run" }
func (t *TestRunTool) Description() string {
	return "Run tests and parse results. Supports Go, Python, Node.js, and Rust. " +
		"Args: language (optional: auto-detect), path (optional: test file/dir), verbose (optional), timeout (seconds, default 120)"
}
func (t *TestRunTool) IsReadOnly() bool        { return false }
func (t *TestRunTool) IsConcurrencySafe() bool { return false }
func (t *TestRunTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"language": map[string]interface{}{"type": "string", "description": "go, python, node, rust (auto-detect if omitted)"},
			"path":     map[string]interface{}{"type": "string", "description": "Test file or directory"},
			"verbose":  map[string]interface{}{"type": "boolean", "description": "Verbose output (default false)"},
			"timeout":  map[string]interface{}{"type": "integer", "description": "Timeout in seconds (default 120)"},
		},
	}
}
func (t *TestRunTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	lang, _ := args["language"].(string)
	path, _ := args["path"].(string)
	verbose, _ := args["verbose"].(bool)
	timeoutSec := toInt(args["timeout"], 120)

	if lang == "" {
		lang = detectTestLanguage(t.rootPath, path)
	}
	if path != "" {
		path = resolvePath(path, t.rootPath)
	} else {
		path = t.rootPath
	}

	timeout := time.Duration(timeoutSec) * time.Second
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	var resultParser func(string) TestSummary

	switch lang {
	case "go":
		goArgs := []string{"test"}
		if verbose {
			goArgs = append(goArgs, "-v")
		}
		goArgs = append(goArgs, "-json", path)
		cmd = exec.CommandContext(ctx, "go", goArgs...)
		cmd.Dir = t.rootPath
		resultParser = parseGoTestOutput
	case "python", "py":
		cmd = exec.CommandContext(ctx, "python", "-m", "pytest", "-v", path)
		cmd.Dir = t.rootPath
		resultParser = parsePytestOutput
	case "node", "javascript", "typescript", "js", "ts":
		cmd = exec.CommandContext(ctx, "npx", "jest", "--verbose", path)
		cmd.Dir = t.rootPath
		resultParser = parseJestOutput
	case "rust":
		cmd = exec.CommandContext(ctx, "cargo", "test", "--manifest-path", path+"/Cargo.toml")
		cmd.Dir = t.rootPath
		resultParser = parseRustTestOutput
	default:
		return types.ToolResult{
			Content: fmt.Sprintf("Unsupported test language: %s (supported: go, python, node, rust)", lang),
			Success: false,
			Error:   "unsupported language",
		}, nil
	}

	out, err := cmd.CombinedOutput()
	output := string(out)

	if ctx.Err() == context.DeadlineExceeded {
		return types.ToolResult{
			Content: fmt.Sprintf("Tests timed out after %d seconds\n%s", timeoutSec, truncateOutput(output, 3000)),
			Success: false,
			Error:   "timeout",
		}, nil
	}

	summary := resultParser(output)

	result := fmt.Sprintf("Test Results (%s):\n  Total: %d\n  Passed: %d\n  Failed: %d\n  Skipped: %d\n  Duration: %s",
		lang, summary.Total, summary.Passed, summary.Failed, summary.Skipped, summary.Duration)

	if summary.Failed > 0 {
		result += fmt.Sprintf("\n\nFailures:\n%s", truncateOutput(summary.FailureDetails, 2000))
	}

	return types.ToolResult{
		Content: result,
		Success: summary.Failed == 0 && err == nil,
	}, nil
}

// TestSummary holds parsed test results.
type TestSummary struct {
	Total          int
	Passed         int
	Failed         int
	Skipped        int
	Duration       string
	FailureDetails string
}

// detectTestLanguage auto-detects the test language from project files.
func detectTestLanguage(rootPath, path string) string {
	// Check by path extension
	if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".go") {
		return "go"
	}
	if strings.HasSuffix(path, "test_.py") || strings.HasSuffix(path, "_test.py") || strings.HasSuffix(path, ".py") {
		return "python"
	}
	if strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.js") || strings.HasSuffix(path, ".spec.ts") {
		return "node"
	}
	if strings.HasSuffix(path, ".rs") {
		return "rust"
	}

	// Check project root
	checks := []struct {
		file string
		lang string
	}{
		{"go.mod", "go"},
		{"Cargo.toml", "rust"},
		{"package.json", "node"},
		{"requirements.txt", "python"},
		{"pyproject.toml", "python"},
	}
	for _, c := range checks {
		if _, err := exec.LookPath("stat"); err == nil {
			checkCmd := exec.Command("stat", rootPath+"/"+c.file)
			if checkCmd.Run() == nil {
				return c.lang
			}
		}
	}
	return "go" // default
}

// parseGoTestOutput parses `go test -json` output.
func parseGoTestOutput(output string) TestSummary {
	var summary TestSummary
	var failures []string

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry map[string]interface{}
		if err := jsonUnmarshal(line, &entry); err != nil {
			continue
		}

		action, _ := entry["Action"].(string)
		test, _ := entry["Test"].(string)
		_ = test

		switch action {
		case "pass":
			summary.Passed++
			summary.Total++
		case "fail":
			summary.Failed++
			summary.Total++
			if test != "" {
				failures = append(failures, fmt.Sprintf("FAIL: %s", test))
			}
		case "skip":
			summary.Skipped++
			summary.Total++
		}
	}

	// Extract elapsed time
	elapsed, _ := findInOutput(output, "elapsed")
	if elapsed != "" {
		summary.Duration = elapsed
	} else {
		summary.Duration = "unknown"
	}

	summary.FailureDetails = strings.Join(failures, "\n")
	return summary
}

// parsePytestOutput parses pytest output.
func parsePytestOutput(output string) TestSummary {
	var summary TestSummary

	// Look for summary line like "5 passed, 1 failed, 2 skipped"
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "passed") || strings.Contains(line, "failed") {
			// Parse numbers
			if n := extractNum(line, "passed"); n > 0 {
				summary.Passed = n
			}
			if n := extractNum(line, "failed"); n > 0 {
				summary.Failed = n
			}
			if n := extractNum(line, "skipped"); n > 0 {
				summary.Skipped = n
			}
			if n := extractNum(line, "error"); n > 0 {
				summary.Failed += n
			}
		}
		if strings.Contains(line, "FAILED") {
			summary.FailureDetails += line + "\n"
		}
		if strings.Contains(line, "in ") && strings.Contains(line, "s") {
			if idx := strings.LastIndex(line, "in "); idx >= 0 {
				summary.Duration = line[idx+3:]
			}
		}
	}

	summary.Total = summary.Passed + summary.Failed + summary.Skipped
	return summary
}

// parseJestOutput parses Jest output.
func parseJestOutput(output string) TestSummary {
	var summary TestSummary

	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Tests:") {
			if n := extractNum(line, "passed"); n > 0 {
				summary.Passed = n
			}
			if n := extractNum(line, "failed"); n > 0 {
				summary.Failed = n
			}
			if n := extractNum(line, "skipped"); n > 0 {
				summary.Skipped = n
			}
			if n := extractNum(line, "total"); n > 0 {
				summary.Total = n
			}
		}
		if strings.Contains(line, "FAIL") {
			summary.FailureDetails += line + "\n"
		}
		if strings.Contains(line, "Time:") {
			if idx := strings.Index(line, "Time:"); idx >= 0 {
				summary.Duration = strings.TrimSpace(line[idx+5:])
			}
		}
	}

	if summary.Total == 0 {
		summary.Total = summary.Passed + summary.Failed + summary.Skipped
	}
	return summary
}

// parseRustTestOutput parses cargo test output.
func parseRustTestOutput(output string) TestSummary {
	var summary TestSummary

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "test result:") {
			// "test result: ok. 5 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out"
			if n := extractNum(line, "passed"); n > 0 {
				summary.Passed = n
			}
			if n := extractNum(line, "failed"); n > 0 {
				summary.Failed = n
			}
			if n := extractNum(line, "ignored"); n > 0 {
				summary.Skipped = n
			}
		}
		if strings.HasPrefix(line, "FAILED") {
			summary.FailureDetails += line + "\n"
		}
	}

	summary.Total = summary.Passed + summary.Failed + summary.Skipped
	return summary
}

// helpers

func jsonUnmarshal(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}

func extractNum(line, keyword string) int {
	idx := strings.Index(line, keyword)
	if idx < 0 {
		return 0
	}
	// Look backwards for the number
	prefix := line[:idx]
	prefix = strings.TrimSpace(prefix)
	// Find last space-separated token
	tokens := strings.Fields(prefix)
	if len(tokens) == 0 {
		return 0
	}
	last := tokens[len(tokens)-1]
	// Remove trailing comma
	last = strings.TrimRight(last, ",")
	var n int
	fmt.Sscanf(last, "%d", &n)
	return n
}

func findInOutput(output, keyword string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, keyword) {
			return line, true
		}
	}
	return "", false
}
