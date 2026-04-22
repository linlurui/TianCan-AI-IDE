package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── BashTool ────────────────────────────────────────────────────

type BashTool struct{ rootPath string }

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return "Execute a shell command and return its output. Commands run in the project root directory. " +
		"Args: command (required), timeout (seconds, optional, default 120), cwd (optional, override working directory)"
}
func (t *BashTool) IsReadOnly() bool        { return false }
func (t *BashTool) IsConcurrencySafe() bool { return false }
func (t *BashTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Shell command to execute"},
			"timeout": map[string]interface{}{"type": "integer", "description": "Timeout in seconds (default 120)"},
			"cwd":     map[string]interface{}{"type": "string", "description": "Working directory (optional)"},
		},
		"required": []string{"command"},
	}
}
func (t *BashTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: command")
	}
	timeoutSec := toInt(args["timeout"], 120)
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	if timeoutSec > 600 {
		timeoutSec = 600
	}

	cwd := t.rootPath
	if customCwd, ok := args["cwd"].(string); ok && customCwd != "" {
		cwd = resolvePath(customCwd, t.rootPath)
	}
	// Fallback to home directory if cwd is empty
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		}
	}

	// Use platform-appropriate shell
	shell := "bash"
	shellFlag := "-c"

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, shellFlag, command)
	cmd.Dir = cwd

	// Capture both stdout and stderr separately for better output
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	outStr := stdout.String()
	errStr := stderr.String()

	// Combine output
	var output string
	if outStr != "" {
		output = outStr
	}
	if errStr != "" {
		if output != "" {
			output += "\n"
		}
		output += "[stderr]\n" + errStr
	}

	// Truncate if too long
	output = truncateOutput(output, 5000)

	if err != nil {
		errMsg := err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("command timed out after %d seconds", timeoutSec)
		}
		return types.ToolResult{
			Content: fmt.Sprintf("Command failed: %s\n[cwd: %s]\n%s", errMsg, cwd, output),
			Success: false,
			Error:   errMsg,
		}, nil
	}

	return types.ToolResult{Content: fmt.Sprintf("[cwd: %s]\n%s", cwd, output), Success: true}, nil
}

// ── GrepTool ────────────────────────────────────────────────────

type GrepTool struct{ rootPath string }

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return "Search for a pattern in files using ripgrep-style search. " +
		"Args: pattern (required), path (optional, defaults to root), include (glob filter, optional), " +
		"exclude (glob filter, optional), case_sensitive (optional, default false), max_results (optional, default 100)"
}
func (t *GrepTool) IsReadOnly() bool        { return true }
func (t *GrepTool) IsConcurrencySafe() bool { return true }
func (t *GrepTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern":        map[string]interface{}{"type": "string", "description": "Search pattern (regex or literal)"},
			"path":           map[string]interface{}{"type": "string", "description": "Directory or file to search in"},
			"include":        map[string]interface{}{"type": "string", "description": "Glob pattern to include (e.g. '*.go')"},
			"exclude":        map[string]interface{}{"type": "string", "description": "Glob pattern to exclude (e.g. 'vendor/*')"},
			"case_sensitive": map[string]interface{}{"type": "boolean", "description": "Case sensitive search (default false)"},
			"max_results":    map[string]interface{}{"type": "integer", "description": "Max results (default 100)"},
		},
		"required": []string{"pattern"},
	}
}
func (t *GrepTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: pattern")
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = t.rootPath
	} else {
		path = resolvePath(path, t.rootPath)
	}
	include, _ := args["include"].(string)
	exclude, _ := args["exclude"].(string)
	caseSensitive, _ := args["case_sensitive"].(bool)
	maxResults := toInt(args["max_results"], 100)

	// Build grep command arguments
	grepArgs := []string{"-rn", "--color=never"}

	if !caseSensitive {
		grepArgs = append(grepArgs, "-i")
	}

	if include != "" {
		grepArgs = append(grepArgs, "--include="+include)
	} else {
		// Default: search common source files
		for _, ext := range []string{
			"*.go", "*.ts", "*.tsx", "*.js", "*.jsx", "*.py", "*.rs",
			"*.java", "*.md", "*.yaml", "*.yml", "*.json", "*.toml",
			"*.css", "*.scss", "*.html", "*.vue", "*.svelte",
			"*.c", "*.cpp", "*.h", "*.hpp", "*.cs", "*.rb", "*.php",
			"*.sh", "*.bash", "*.zsh", "*.swift", "*.kt", "*.scala",
		} {
			grepArgs = append(grepArgs, "--include="+ext)
		}
	}

	if exclude != "" {
		grepArgs = append(grepArgs, "--exclude="+exclude)
	}
	// Always exclude common noise directories
	for _, dir := range []string{".git", "node_modules", "vendor", "__pycache__", "dist", "build", ".next"} {
		grepArgs = append(grepArgs, "--exclude-dir="+dir)
	}

	grepArgs = append(grepArgs, pattern, path)

	cmd := exec.CommandContext(ctx, "grep", grepArgs...)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil && len(out) == 0 {
		return types.ToolResult{Content: "No matches found", Success: true}, nil
	}

	// Truncate to max results
	lines := strings.Split(output, "\n")
	if len(lines) > maxResults {
		output = strings.Join(lines[:maxResults], "\n") +
			fmt.Sprintf("\n... (%d more results, use max_results to see more)", len(lines)-maxResults)
	}

	return types.ToolResult{Content: output, Success: true}, nil
}

// ── GlobTool ────────────────────────────────────────────────────

type GlobTool struct{ rootPath string }

func (t *GlobTool) Name() string { return "glob" }
func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern. Supports ** for recursive matching. " +
		"Args: pattern (required), path (optional, defaults to root), exclude (optional glob to exclude)"
}
func (t *GlobTool) IsReadOnly() bool        { return true }
func (t *GlobTool) IsConcurrencySafe() bool { return true }
func (t *GlobTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{"type": "string", "description": "Glob pattern (e.g. '**/*.go', 'src/**/*.ts')"},
			"path":    map[string]interface{}{"type": "string", "description": "Base directory to search in"},
			"exclude": map[string]interface{}{"type": "string", "description": "Glob pattern to exclude"},
		},
		"required": []string{"pattern"},
	}
}
func (t *GlobTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: pattern")
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = t.rootPath
	} else {
		path = resolvePath(path, t.rootPath)
	}
	exclude, _ := args["exclude"].(string)

	// Use find for ** support since filepath.Glob doesn't support **
	var matches []string

	if strings.Contains(pattern, "**") {
		// Use find command for recursive glob
		findPattern := strings.ReplaceAll(pattern, "**/", "")
		findArgs := []string{path, "-type", "f", "-name", findPattern}

		// Exclude directories
		excludeDirs := []string{".git", "node_modules", "vendor", "__pycache__", "dist", "build"}
		for _, d := range excludeDirs {
			findArgs = append(findArgs, "-not", "-path", "*/"+d+"/*")
		}
		if exclude != "" {
			findArgs = append(findArgs, "-not", "-name", exclude)
		}

		cmd := exec.CommandContext(ctx, "find", findArgs...)
		out, err := cmd.Output()
		if err != nil && len(out) == 0 {
			return types.ToolResult{Content: "No files found", Success: true}, nil
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				rel, err := filepath.Rel(path, line)
				if err == nil {
					matches = append(matches, rel)
				} else {
					matches = append(matches, line)
				}
			}
		}
	} else {
		// Simple glob
		absPattern := filepath.Join(path, pattern)
		found, err := filepath.Glob(absPattern)
		if err != nil {
			return types.ToolResult{}, fmt.Errorf("invalid glob pattern: %s", pattern)
		}
		for _, f := range found {
			rel, err := filepath.Rel(path, f)
			if err == nil {
				matches = append(matches, rel)
			} else {
				matches = append(matches, f)
			}
		}
	}

	if len(matches) == 0 {
		return types.ToolResult{Content: "No files found matching pattern", Success: true}, nil
	}

	// Truncate if too many
	if len(matches) > 500 {
		matches = matches[:500]
		matches = append(matches, fmt.Sprintf("... (%d more files)", len(matches)-500))
	}

	return types.ToolResult{
		Content: fmt.Sprintf("Found %d files:\n%s", len(matches), strings.Join(matches, "\n")),
		Success: true,
	}, nil
}

// ── WebFetchTool ────────────────────────────────────────────────

type WebFetchTool struct{}

func (t *WebFetchTool) Name() string { return "web_fetch" }
func (t *WebFetchTool) Description() string {
	return "Fetch content from a URL. Returns the response body as text. " +
		"Args: url (required), method (optional, default GET), max_length (optional, default 50000)"
}
func (t *WebFetchTool) IsReadOnly() bool        { return true }
func (t *WebFetchTool) IsConcurrencySafe() bool { return true }
func (t *WebFetchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url":        map[string]interface{}{"type": "string", "description": "URL to fetch"},
			"method":     map[string]interface{}{"type": "string", "description": "HTTP method (default GET)"},
			"max_length": map[string]interface{}{"type": "integer", "description": "Max response length (default 50000)"},
		},
		"required": []string{"url"},
	}
}
func (t *WebFetchTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: url")
	}
	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}
	maxLength := toInt(args["max_length"], 50000)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error creating request: %v", err), Success: false, Error: err.Error()}, nil
	}
	req.Header.Set("User-Agent", "TianCan-AI-IDE/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error fetching URL: %v", err), Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return types.ToolResult{
			Content: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status),
			Success: false,
			Error:   fmt.Sprintf("HTTP %d", resp.StatusCode),
		}, nil
	}

	// Read with limit
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxLength)))
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error reading response: %v", err), Success: false, Error: err.Error()}, nil
	}

	content := string(body)
	// Strip HTML tags for basic readability if content appears to be HTML
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		content = stripHTMLTags(content)
	}

	return types.ToolResult{
		Content: fmt.Sprintf("URL: %s\nStatus: %s\nContent-Length: %d\n\n%s", url, resp.Status, len(body), content),
		Success: true,
	}, nil
}

// stripHTMLTags removes basic HTML tags for readability.
func stripHTMLTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	s = re.ReplaceAllString(s, "")
	// Clean up excessive whitespace
	re2 := regexp.MustCompile(`\n{3,}`)
	s = re2.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
