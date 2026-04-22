package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── GitDiffTool ─────────────────────────────────────────────────

type GitDiffTool struct{ rootPath string }

func (t *GitDiffTool) Name() string { return "git_diff" }
func (t *GitDiffTool) Description() string {
	return "Show git diff of staged, unstaged, or specific commits. " +
		"Args: ref (optional: HEAD, staged, or commit hash), path (optional: file/dir filter), stat (optional: show stat only)"
}
func (t *GitDiffTool) IsReadOnly() bool        { return true }
func (t *GitDiffTool) IsConcurrencySafe() bool { return true }
func (t *GitDiffTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ref":  map[string]interface{}{"type": "string", "description": "Git ref (HEAD, staged, commit hash, branch)"},
			"path": map[string]interface{}{"type": "string", "description": "File or directory to diff"},
			"stat": map[string]interface{}{"type": "boolean", "description": "Show stat summary only (default false)"},
		},
	}
}
func (t *GitDiffTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	ref, _ := args["ref"].(string)
	path, _ := args["path"].(string)
	statOnly, _ := args["stat"].(bool)

	gitArgs := []string{"diff"}
	if statOnly {
		gitArgs = append(gitArgs, "--stat")
	}

	switch ref {
	case "staged", "--staged", "--cached":
		gitArgs = append(gitArgs, "--cached")
	case "HEAD", "head":
		gitArgs = append(gitArgs, "HEAD")
	case "":
		// working tree diff
	default:
		// commit hash or branch
		gitArgs = append(gitArgs, ref)
	}

	if path != "" {
		gitArgs = append(gitArgs, "--", resolvePath(path, t.rootPath))
	}

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git diff error: %s", err), Success: false, Error: err.Error()}, nil
	}
	if out == "" {
		return types.ToolResult{Content: "No changes", Success: true}, nil
	}
	return types.ToolResult{Content: truncateOutput(out, 3000), Success: true}, nil
}

// ── GitLogTool ──────────────────────────────────────────────────

type GitLogTool struct{ rootPath string }

func (t *GitLogTool) Name() string { return "git_log" }
func (t *GitLogTool) Description() string {
	return "Show git commit log. Args: count (optional, default 20), path (optional), oneline (optional, default true)"
}
func (t *GitLogTool) IsReadOnly() bool        { return true }
func (t *GitLogTool) IsConcurrencySafe() bool { return true }
func (t *GitLogTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"count":   map[string]interface{}{"type": "integer", "description": "Number of commits (default 20)"},
			"path":    map[string]interface{}{"type": "string", "description": "File or directory filter"},
			"oneline": map[string]interface{}{"type": "boolean", "description": "One line per commit (default true)"},
		},
	}
}
func (t *GitLogTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	count := toInt(args["count"], 20)
	path, _ := args["path"].(string)
	oneline, _ := args["oneline"].(bool)

	gitArgs := []string{"log", fmt.Sprintf("-%d", count)}
	if oneline {
		gitArgs = append(gitArgs, "--oneline")
	}
	gitArgs = append(gitArgs, "--decorate")

	if path != "" {
		gitArgs = append(gitArgs, "--", resolvePath(path, t.rootPath))
	}

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git log error: %s", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// ── GitShowTool ─────────────────────────────────────────────────

type GitShowTool struct{ rootPath string }

func (t *GitShowTool) Name() string { return "git_show" }
func (t *GitShowTool) Description() string {
	return "Show commit details, file at revision, or blame info. " +
		"Args: ref (required: commit hash or file path), path (optional: file path for blame)"
}
func (t *GitShowTool) IsReadOnly() bool        { return true }
func (t *GitShowTool) IsConcurrencySafe() bool { return true }
func (t *GitShowTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ref":  map[string]interface{}{"type": "string", "description": "Commit hash, tag, or branch"},
			"path": map[string]interface{}{"type": "string", "description": "File path (for show file at revision)"},
		},
		"required": []string{"ref"},
	}
}
func (t *GitShowTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	ref, _ := args["ref"].(string)
	path, _ := args["path"].(string)
	if ref == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: ref")
	}

	var gitArgs []string
	if path != "" {
		gitArgs = []string{"show", fmt.Sprintf("%s:%s", ref, path)}
	} else {
		gitArgs = []string{"show", "--stat", ref}
	}

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git show error: %s", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: truncateOutput(out, 3000), Success: true}, nil
}

// ── GitBlameTool ────────────────────────────────────────────────

type GitBlameTool struct{ rootPath string }

func (t *GitBlameTool) Name() string { return "git_blame" }
func (t *GitBlameTool) Description() string {
	return "Show git blame for a file. Args: path (required), start_line (optional), end_line (optional)"
}
func (t *GitBlameTool) IsReadOnly() bool        { return true }
func (t *GitBlameTool) IsConcurrencySafe() bool { return true }
func (t *GitBlameTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":       map[string]interface{}{"type": "string", "description": "File path to blame"},
			"start_line": map[string]interface{}{"type": "integer", "description": "Start line (1-indexed)"},
			"end_line":   map[string]interface{}{"type": "integer", "description": "End line"},
		},
		"required": []string{"path"},
	}
}
func (t *GitBlameTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: path")
	}
	startLine := toInt(args["start_line"], 0)
	endLine := toInt(args["end_line"], 0)

	gitArgs := []string{"blame", resolvePath(path, t.rootPath)}
	if startLine > 0 {
		gitArgs = append(gitArgs, fmt.Sprintf("-L %d,%d", startLine, endLine))
	}

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git blame error: %s", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: truncateOutput(out, 2000), Success: true}, nil
}

// ── GitCommitTool ───────────────────────────────────────────────

type GitCommitTool struct{ rootPath string }

func (t *GitCommitTool) Name() string { return "git_commit" }
func (t *GitCommitTool) Description() string {
	return "Stage and commit changes. Args: message (required), paths (optional: files to stage, default all), all (optional: stage all tracked)"
}
func (t *GitCommitTool) IsReadOnly() bool        { return false }
func (t *GitCommitTool) IsConcurrencySafe() bool { return false }
func (t *GitCommitTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{"type": "string", "description": "Commit message"},
			"paths": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{"type": "string"},
				"description": "Files to stage",
			},
			"all": map[string]interface{}{"type": "boolean", "description": "Stage all tracked changes (git commit -a)"},
		},
		"required": []string{"message"},
	}
}
func (t *GitCommitTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	message, _ := args["message"].(string)
	if message == "" {
		return types.ToolResult{}, fmt.Errorf("missing required argument: message")
	}
	stageAll, _ := args["all"].(bool)
	pathsRaw, _ := args["paths"].([]interface{})

	// Stage specific files if provided
	if len(pathsRaw) > 0 {
		stageArgs := []string{"add"}
		for _, p := range pathsRaw {
			if s, ok := p.(string); ok && s != "" {
				stageArgs = append(stageArgs, resolvePath(s, t.rootPath))
			}
		}
		if _, err := runGit(ctx, t.rootPath, stageArgs...); err != nil {
			return types.ToolResult{Content: fmt.Sprintf("git add error: %s", err), Success: false, Error: err.Error()}, nil
		}
	}

	// Commit
	commitArgs := []string{"commit", "-m", message}
	if stageAll && len(pathsRaw) == 0 {
		commitArgs = []string{"commit", "-a", "-m", message}
	}

	out, err := runGit(ctx, t.rootPath, commitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git commit error: %s\n%s", err, out), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: fmt.Sprintf("Committed: %s", strings.TrimSpace(out)), Success: true}, nil
}

// ── GitStatusTool ───────────────────────────────────────────────

type GitStatusTool struct{ rootPath string }

func (t *GitStatusTool) Name() string { return "git_status" }
func (t *GitStatusTool) Description() string {
	return "Show git working tree status. Args: short (optional: short format, default true)"
}
func (t *GitStatusTool) IsReadOnly() bool        { return true }
func (t *GitStatusTool) IsConcurrencySafe() bool { return true }
func (t *GitStatusTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"short": map[string]interface{}{"type": "boolean", "description": "Short format (default true)"},
		},
	}
}
func (t *GitStatusTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	short, _ := args["short"].(bool)

	gitArgs := []string{"status"}
	if short {
		gitArgs = append(gitArgs, "--short")
	}
	gitArgs = append(gitArgs, "--branch")

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git status error: %s", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// ── GitBranchTool ───────────────────────────────────────────────

type GitBranchTool struct{ rootPath string }

func (t *GitBranchTool) Name() string { return "git_branch" }
func (t *GitBranchTool) Description() string {
	return "List, create, or switch branches. Args: action (list/create/switch, default list), name (branch name for create/switch)"
}
func (t *GitBranchTool) IsReadOnly() bool        { return false }
func (t *GitBranchTool) IsConcurrencySafe() bool { return false }
func (t *GitBranchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{"type": "string", "description": "list, create, or switch (default list)"},
			"name":   map[string]interface{}{"type": "string", "description": "Branch name (for create/switch)"},
		},
	}
}
func (t *GitBranchTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	action, _ := args["action"].(string)
	name, _ := args["name"].(string)
	if action == "" {
		action = "list"
	}

	var gitArgs []string
	switch action {
	case "list":
		gitArgs = []string{"branch", "-a", "-v"}
	case "create":
		if name == "" {
			return types.ToolResult{}, fmt.Errorf("missing required argument: name for create")
		}
		gitArgs = []string{"checkout", "-b", name}
	case "switch":
		if name == "" {
			return types.ToolResult{}, fmt.Errorf("missing required argument: name for switch")
		}
		gitArgs = []string{"checkout", name}
	default:
		return types.ToolResult{}, fmt.Errorf("invalid action: %s (use list, create, or switch)", action)
	}

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git branch error: %s", err), Success: false, Error: err.Error()}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// ── GitStashTool ────────────────────────────────────────────────

type GitStashTool struct{ rootPath string }

func (t *GitStashTool) Name() string { return "git_stash" }
func (t *GitStashTool) Description() string {
	return "Stash or pop changes. Args: action (push/pop/list, default list), message (for push)"
}
func (t *GitStashTool) IsReadOnly() bool        { return false }
func (t *GitStashTool) IsConcurrencySafe() bool { return false }
func (t *GitStashTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":  map[string]interface{}{"type": "string", "description": "push, pop, or list (default list)"},
			"message": map[string]interface{}{"type": "string", "description": "Stash message (for push)"},
		},
	}
}
func (t *GitStashTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	action, _ := args["action"].(string)
	message, _ := args["message"].(string)
	if action == "" {
		action = "list"
	}

	var gitArgs []string
	switch action {
	case "list":
		gitArgs = []string{"stash", "list"}
	case "push":
		gitArgs = []string{"stash", "push"}
		if message != "" {
			gitArgs = append(gitArgs, "-m", message)
		}
	case "pop":
		gitArgs = []string{"stash", "pop"}
	default:
		return types.ToolResult{}, fmt.Errorf("invalid action: %s (use push, pop, or list)", action)
	}

	out, err := runGit(ctx, t.rootPath, gitArgs...)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("git stash error: %s", err), Success: false, Error: err.Error()}, nil
	}
	if out == "" && action == "list" {
		return types.ToolResult{Content: "No stashes", Success: true}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// ── helper ──────────────────────────────────────────────────────

func runGit(ctx context.Context, rootPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = rootPath
	out, err := cmd.CombinedOutput()
	return string(out), err
}
