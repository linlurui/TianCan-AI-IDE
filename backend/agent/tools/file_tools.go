package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── ReadFileTool ────────────────────────────────────────────────

type ReadFileTool struct{ rootPath string }

func (t *ReadFileTool) Name() string            { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read file contents with line numbers. Supports offset/limit for partial reads. " +
		"Automatically skips binary files. Args: path (required), offset (1-indexed start line), limit (max lines, default 2000)"
}
func (t *ReadFileTool) IsReadOnly() bool        { return true }
func (t *ReadFileTool) IsConcurrencySafe() bool { return true }
func (t *ReadFileTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":   map[string]interface{}{"type": "string", "description": "File path to read"},
			"offset": map[string]interface{}{"type": "integer", "description": "1-indexed start line (optional)"},
			"limit":  map[string]interface{}{"type": "integer", "description": "Max lines to read (default 2000)"},
		},
		"required": []string{"path"},
	}
}
func (t *ReadFileTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: path")
	}
	path = resolvePath(path, t.rootPath)
	if err := validatePath(path, t.rootPath); err != nil {
		return types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false, Error: err.Error()}, nil
	}
	if info.IsDir() {
		return t.readDir(path)
	}
	if isBinaryFile(path) {
		return types.ToolResult{
			Content: fmt.Sprintf("Binary file: %s (%d bytes)", path, info.Size()),
			Success: true,
		}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error opening file: %v", err), Success: false, Error: err.Error()}, nil
	}
	defer f.Close()

	offset := toInt(args["offset"], 1)
	limit := toInt(args["limit"], 2000)
	if offset < 1 {
		offset = 1
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	// Increase buffer for long lines
	scanner.Buffer(make([]byte, 0), 1024*1024)
	lineNum := 1
	linesRead := 0
	maxLineLen := 2000

	for lineNum < offset && scanner.Scan() {
		lineNum++
	}
	for scanner.Scan() && linesRead < limit {
		line := scanner.Text()
		if len(line) > maxLineLen {
			line = line[:maxLineLen] + fmt.Sprintf("... (%d chars truncated)", len(line)-maxLineLen)
		}
		sb.WriteString(fmt.Sprintf("%6d\t%s\n", lineNum, line))
		lineNum++
		linesRead++
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return types.ToolResult{Content: fmt.Sprintf("Error reading: %v", err), Success: false, Error: err.Error()}, nil
	}

	header := fmt.Sprintf("File: %s (%d bytes)\n", path, info.Size())
	if offset > 1 || linesRead >= limit {
		header += fmt.Sprintf("Lines %d-%d\n", offset, offset+linesRead-1)
	}
	return types.ToolResult{Content: header + sb.String(), Success: true}, nil
}

func (t *ReadFileTool) readDir(path string) (types.ToolResult, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return types.ToolResult{}, err
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Directory: %s (%d entries)", path, len(entries)))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			lines = append(lines, fmt.Sprintf("  %-40s  [error]", e.Name()))
			continue
		}
		prefix := "📄"
		if e.IsDir() {
			prefix = "📁"
		}
		lines = append(lines, fmt.Sprintf("%s %-40s %8d  %s", prefix, e.Name(), info.Size(), info.ModTime().Format("2006-01-02 15:04")))
	}
	return types.ToolResult{Content: strings.Join(lines, "\n"), Success: true}, nil
}

// ── WriteFileTool ───────────────────────────────────────────────

type WriteFileTool struct{ rootPath string }

func (t *WriteFileTool) Name() string            { return "file_write" }
func (t *WriteFileTool) Description() string {
	return "Write content to a file, creating parent directories if needed. " +
		"Args: path (required), content (required), append (optional, default false)"
}
func (t *WriteFileTool) IsReadOnly() bool        { return false }
func (t *WriteFileTool) IsConcurrencySafe() bool { return false }
func (t *WriteFileTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]interface{}{"type": "string", "description": "File path to write"},
			"content": map[string]interface{}{"type": "string", "description": "Content to write"},
			"append":  map[string]interface{}{"type": "boolean", "description": "Append instead of overwrite (default false)"},
		},
		"required": []string{"path", "content"},
	}
}
func (t *WriteFileTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: path")
	}
	content, ok := args["content"].(string)
	if !ok {
		return types.ToolResult{}, fmt.Errorf("missing required argument: content")
	}
	appendMode, _ := args["append"].(bool)

	path = resolvePath(path, t.rootPath)
	if err := validatePath(path, t.rootPath); err != nil {
		return types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error creating directory: %v", err), Success: false, Error: err.Error()}, nil
	}

	existed := false
	if _, err := os.Stat(path); err == nil {
		existed = true
	}

	var flag int
	if appendMode {
		flag = os.O_APPEND | os.O_CREATE | os.O_WRONLY
	} else {
		flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error opening file: %v", err), Success: false, Error: err.Error()}, nil
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error writing file: %v", err), Success: false, Error: err.Error()}, nil
	}

	action := "created"
	if existed && !appendMode {
		action = "overwritten"
	} else if appendMode {
		action = "appended to"
	}
	lineCount := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") && len(content) > 0 {
		lineCount++
	}
	return types.ToolResult{
		Content: fmt.Sprintf("File %s: %s (%d lines, %d bytes)", action, path, lineCount, len(content)),
		Success: true,
	}, nil
}

// ── EditFileTool ────────────────────────────────────────────────

type EditFileTool struct{ rootPath string }

func (t *EditFileTool) Name() string            { return "file_edit" }
func (t *EditFileTool) Description() string {
	return "Perform exact string replacement in a file. old_string must be unique unless replace_all=true. " +
		"Args: path (required), old_string (required), new_string (required), replace_all (optional)"
}
func (t *EditFileTool) IsReadOnly() bool        { return false }
func (t *EditFileTool) IsConcurrencySafe() bool { return false }
func (t *EditFileTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":        map[string]interface{}{"type": "string", "description": "File path to edit"},
			"old_string":  map[string]interface{}{"type": "string", "description": "Exact text to find"},
			"new_string":  map[string]interface{}{"type": "string", "description": "Replacement text"},
			"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace all occurrences (default false)"},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}
func (t *EditFileTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	path, _ := args["path"].(string)
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)

	if path == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: path")
	}
	if oldStr == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: old_string")
	}

	path = resolvePath(path, t.rootPath)
	if err := validatePath(path, t.rootPath); err != nil {
		return types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error reading file: %v", err), Success: false, Error: err.Error()}, nil
	}
	content := string(data)

	if !strings.Contains(content, oldStr) {
		lines := strings.Split(content, "\n")
		return types.ToolResult{
			Content: fmt.Sprintf("old_string not found in %s (%d lines). Ensure exact match including whitespace.", path, len(lines)),
			Success: false, Error: "old_string not found",
		}, nil
	}

	count := strings.Count(content, oldStr)
	if count > 1 && !replaceAll {
		lineNums := findStringLineNumbers(content, oldStr)
		return types.ToolResult{
			Content: fmt.Sprintf("old_string appears %d times (lines %v). Use replace_all=true or provide more specific context.", count, lineNums),
			Success: false, Error: "old_string not unique",
		}, nil
	}
	if oldStr == newStr {
		return types.ToolResult{Content: "old_string and new_string are identical — no change.", Success: true}, nil
	}

	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		newContent = strings.Replace(content, oldStr, newStr, 1)
	}
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error writing file: %v", err), Success: false, Error: err.Error()}, nil
	}

	replaced := count
	if !replaceAll {
		replaced = 1
	}
	return types.ToolResult{
		Content: fmt.Sprintf("Edited %s: %d replacement(s) applied", path, replaced),
		Success: true,
	}, nil
}

// ── MultiEditFileTool ───────────────────────────────────────────

type MultiEditFileTool struct{ rootPath string }

func (t *MultiEditFileTool) Name() string            { return "file_multi_edit" }
func (t *MultiEditFileTool) Description() string {
	return "Perform multiple exact string replacements atomically. All edits apply sequentially. " +
		"Args: path (required), edits (required: [{old_string, new_string, replace_all?}])"
}
func (t *MultiEditFileTool) IsReadOnly() bool        { return false }
func (t *MultiEditFileTool) IsConcurrencySafe() bool { return false }
func (t *MultiEditFileTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string", "description": "File path"},
			"edits": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"old_string":  map[string]interface{}{"type": "string"},
						"new_string":  map[string]interface{}{"type": "string"},
						"replace_all": map[string]interface{}{"type": "boolean"},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		"required": []string{"path", "edits"},
	}
}
func (t *MultiEditFileTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	path, _ := args["path"].(string)
	editsRaw, ok := args["edits"].([]interface{})
	if !ok || len(editsRaw) == 0 {
		return types.ToolResult{}, fmt.Errorf("missing or empty required argument: edits")
	}

	path = resolvePath(path, t.rootPath)
	if err := validatePath(path, t.rootPath); err != nil {
		return types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error reading file: %v", err), Success: false, Error: err.Error()}, nil
	}
	content := string(data)

	totalReplacements := 0
	for i, editRaw := range editsRaw {
		edit, ok := editRaw.(map[string]interface{})
		if !ok {
			return types.ToolResult{Content: fmt.Sprintf("edit[%d] is not an object", i), Success: false, Error: "invalid edit"}, nil
		}
		oldStr, _ := edit["old_string"].(string)
		newStr, _ := edit["new_string"].(string)
		replaceAll, _ := edit["replace_all"].(bool)

		if oldStr == "" {
			return types.ToolResult{Content: fmt.Sprintf("edit[%d]: old_string is empty", i), Success: false, Error: "empty old_string"}, nil
		}
		if !strings.Contains(content, oldStr) {
			return types.ToolResult{
				Content: fmt.Sprintf("edit[%d]: old_string not found (no changes applied)", i),
				Success: false, Error: "old_string not found",
			}, nil
		}
		count := strings.Count(content, oldStr)
		if count > 1 && !replaceAll {
			return types.ToolResult{
				Content: fmt.Sprintf("edit[%d]: old_string appears %d times — use replace_all or be more specific", i, count),
				Success: false, Error: "old_string not unique",
			}, nil
		}
		if replaceAll {
			content = strings.ReplaceAll(content, oldStr, newStr)
		} else {
			content = strings.Replace(content, oldStr, newStr, 1)
		}
		totalReplacements += count
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error writing file: %v", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{
		Content: fmt.Sprintf("Multi-edited %s: %d replacement(s) across %d edits", path, totalReplacements, len(editsRaw)),
		Success: true,
	}, nil
}

// ── ListDirTool ─────────────────────────────────────────────────

type ListDirTool struct{ rootPath string }

func (t *ListDirTool) Name() string            { return "list_directory" }
func (t *ListDirTool) Description() string {
	return "List directory contents with file sizes and timestamps. " +
		"Args: path (optional, defaults to root), recursive (optional), max_depth (default 3)"
}
func (t *ListDirTool) IsReadOnly() bool        { return true }
func (t *ListDirTool) IsConcurrencySafe() bool { return true }
func (t *ListDirTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":      map[string]interface{}{"type": "string", "description": "Directory path"},
			"recursive": map[string]interface{}{"type": "boolean", "description": "List recursively (default false)"},
			"max_depth": map[string]interface{}{"type": "integer", "description": "Max depth (default 3)"},
		},
	}
}
func (t *ListDirTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	path, _ := args["path"].(string)
	recursive, _ := args["recursive"].(bool)
	maxDepth := toInt(args["max_depth"], 3)

	if path == "" {
		path = t.rootPath
	} else {
		path = resolvePath(path, t.rootPath)
	}
	if err := validatePath(path, t.rootPath); err != nil {
		return types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}, nil
	}

	var lines []string
	if recursive {
		err := filepath.WalkDir(path, func(walkPath string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(path, walkPath)
			depth := len(strings.Split(rel, string(filepath.Separator)))
			if depth > maxDepth {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if d.IsDir() && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__") {
				return filepath.SkipDir
			}
			info, _ := d.Info()
			indent := strings.Repeat("  ", depth-1)
			if d.IsDir() {
				lines = append(lines, fmt.Sprintf("%s📁 %s/", indent, name))
			} else if info != nil {
				lines = append(lines, fmt.Sprintf("%s📄 %-40s %8d", indent, name, info.Size()))
			}
			return nil
		})
		if err != nil {
			return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false, Error: err.Error()}, nil
		}
	} else {
		entries, err := os.ReadDir(path)
		if err != nil {
			return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false, Error: err.Error()}, nil
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			prefix := "📄"
			if e.IsDir() {
				prefix = "📁"
			}
			lines = append(lines, fmt.Sprintf("%s %-40s %8d  %s", prefix, e.Name(), info.Size(), info.ModTime().Format("2006-01-02 15:04")))
		}
	}
	return types.ToolResult{Content: strings.Join(lines, "\n"), Success: true}, nil
}
