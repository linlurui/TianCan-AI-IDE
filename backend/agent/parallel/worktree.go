package parallel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// WorktreeIsolation provides git worktree-based isolation for parallel tasks.
// Mirrors Claude Code's worktree isolation for AgentTool parallel spawns.
type WorktreeIsolation struct {
	gitRoot   string
	worktrees map[string]*WorktreeInfo
	mu        sync.Mutex
}

// WorktreeInfo holds metadata about a created worktree.
type WorktreeInfo struct {
	WorktreePath string `json:"worktreePath"`
	Branch       string `json:"branch,omitempty"`
	HeadCommit   string `json:"headCommit,omitempty"`
	Slug         string `json:"slug"`
}

// NewWorktreeIsolation creates a new worktree isolation manager.
func NewWorktreeIsolation(gitRoot string) *WorktreeIsolation {
	return &WorktreeIsolation{
		gitRoot:   gitRoot,
		worktrees: make(map[string]*WorktreeInfo),
	}
}

// CreateWorktree creates a new git worktree for isolated execution.
// Mirrors Claude Code's createAgentWorktree().
func (w *WorktreeIsolation) CreateWorktree(slug string) (*WorktreeInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.gitRoot == "" {
		return nil, fmt.Errorf("git root not set — worktree isolation requires a git repository")
	}

	// Verify git repo exists
	gitDir := filepath.Join(w.gitRoot, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("no .git directory found at %s", w.gitRoot)
	}

	branchName := fmt.Sprintf("tiancan-agent-%s", slug)
	worktreePath := filepath.Join(w.gitRoot, ".tiancan-worktrees", slug)

	// Create worktrees directory
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	// Create the worktree via git command
	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName, "HEAD")
	cmd.Dir = w.gitRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git worktree add failed: %s: %w", string(output), err)
	}

	// Get head commit
	headCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	headCmd.Dir = worktreePath
	headCommit := ""
	if output, err := headCmd.Output(); err == nil {
		headCommit = strings.TrimSpace(string(output))
	}

	info := &WorktreeInfo{
		WorktreePath: worktreePath,
		Branch:       branchName,
		HeadCommit:   headCommit,
		Slug:         slug,
	}

	w.worktrees[slug] = info
	return info, nil
}

// RemoveWorktree removes a worktree after task completion.
// Mirrors Claude Code's cleanup worktree logic.
func (w *WorktreeIsolation) RemoveWorktree(slug string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	info, ok := w.worktrees[slug]
	if !ok {
		return fmt.Errorf("worktree %s not found", slug)
	}

	// Remove the worktree
	cmd := exec.Command("git", "worktree", "remove", info.WorktreePath, "--force")
	cmd.Dir = w.gitRoot
	if _, err := cmd.CombinedOutput(); err != nil {
		// Try force-remove the directory as fallback
		os.RemoveAll(info.WorktreePath)
	}

	// Delete the branch
	branchCmd := exec.Command("git", "branch", "-D", info.Branch)
	branchCmd.Dir = w.gitRoot
	_, _ = branchCmd.CombinedOutput() // ignore error — branch may already be gone

	delete(w.worktrees, slug)
	return nil
}

// GetWorktree returns info about a worktree.
func (w *WorktreeIsolation) GetWorktree(slug string) (*WorktreeInfo, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	info, ok := w.worktrees[slug]
	return info, ok
}

// ListWorktrees returns all active worktrees.
func (w *WorktreeIsolation) ListWorktrees() []*WorktreeInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	var list []*WorktreeInfo
	for _, info := range w.worktrees {
		list = append(list, info)
	}
	return list
}

// CleanupAll removes all worktrees.
func (w *WorktreeIsolation) CleanupAll() error {
	w.mu.Lock()
	slugs := make([]string, 0, len(w.worktrees))
	for slug := range w.worktrees {
		slugs = append(slugs, slug)
	}
	w.mu.Unlock()

	var errs []string
	for _, slug := range slugs {
		if err := w.RemoveWorktree(slug); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// BuildWorktreeNotice creates a notice for the child agent about path translation.
// Mirrors Claude Code's buildWorktreeNotice().
func BuildWorktreeNotice(parentCwd, worktreePath string) string {
	return fmt.Sprintf(`You are running in an isolated git worktree.
- Parent working directory: %s
- Your working directory: %s
- File paths in your instructions may reference the parent directory — translate them to your working directory.
- Re-read any files you need since the worktree may have a different HEAD than the parent.`, parentCwd, worktreePath)
}
