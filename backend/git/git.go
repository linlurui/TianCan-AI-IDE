package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GetHeadFileContent returns the content of a file at HEAD (last commit).
// Returns an empty string if the file is untracked (no HEAD version).
func (s *Service) GetHeadFileContent(repoPath, filePath string) (string, error) {
	cmd := exec.Command("git", "show", "HEAD:"+filePath)
	cmd.Dir = repoPath
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		// File doesn't exist at HEAD (new/untracked) — return empty string
		return "", nil
	}
	return stdout.String(), nil
}

// GetFileDiff returns a unified diff string for the given file.
// For tracked files it shows unstaged changes (falling back to staged).
// For untracked files it shows the full content as additions.
func (s *Service) GetFileDiff(repoPath, filePath string) (string, error) {
	// run captures stdout regardless of exit code (git diff exits 1 when diffs exist).
	run := func(args ...string) (string, bool) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Run() // ignore error — exit 1 is normal for diff with changes
		out := stdout.String()
		return out, len(out) > 0
	}

	// 1. unstaged changes
	if diff, ok := run("diff", "--", filePath); ok {
		return diff, nil
	}
	// 2. staged changes
	if diff, ok := run("diff", "--cached", "--", filePath); ok {
		return diff, nil
	}
	// 3. untracked file → show as new file via git diff --no-index
	abs := filepath.Join(repoPath, filePath)
	if diff, ok := run("diff", "--no-index", "--", "/dev/null", abs); ok {
		lines := strings.Split(diff, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "+++ ") {
				lines[i] = "+++ b/" + filePath
			}
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", fmt.Errorf("no diff available for %s", filePath)
}

// Service exposes git operations to the frontend via Wails.
type Service struct{}

// RepoStatus summarises the working-tree state.
type RepoStatus struct {
	Branch  string       `json:"branch"`
	IsRepo  bool         `json:"isRepo"`
	Files   []FileStatus `json:"files"`
	IsDirty bool         `json:"isDirty"`
}

// FileStatus represents one entry from `git status`.
type FileStatus struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
}

// InitRepo initialises a new git repository at the given path.
func (s *Service) InitRepo(path string) error {
	_, err := gogit.PlainInit(path, false)
	if err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	return nil
}

// GetStatus returns the current working-tree status.
func (s *Service) GetStatus(path string) (RepoStatus, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return RepoStatus{IsRepo: false}, nil
	}

	head, err := repo.Head()
	branch := "HEAD"
	if err == nil {
		branch = head.Name().Short()
	}

	wt, err := repo.Worktree()
	if err != nil {
		return RepoStatus{IsRepo: true, Branch: branch}, fmt.Errorf("worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return RepoStatus{IsRepo: true, Branch: branch}, fmt.Errorf("status: %w", err)
	}

	var files []FileStatus
	for fp, s := range status {
		files = append(files, FileStatus{
			Path:     fp,
			Staging:  string(s.Staging),
			Worktree: string(s.Worktree),
		})
	}

	return RepoStatus{
		Branch:  branch,
		IsRepo:  true,
		Files:   files,
		IsDirty: !status.IsClean(),
	}, nil
}

// StageAll stages all changes (equivalent to `git add -A`).
func (s *Service) StageAll(path string) error {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return fmt.Errorf("stage all: %w", err)
	}
	return nil
}

// Commit creates a new commit with the provided message.
func (s *Service) Commit(path, message, authorName, authorEmail string) (string, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}

	if authorName == "" {
		authorName = "TianCan IDE"
	}
	if authorEmail == "" {
		authorEmail = "ide@tiancan.local"
	}

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return hash.String(), nil
}

// GetLog returns the last N commits.
func (s *Service) GetLog(path string, limit int) ([]CommitInfo, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	iter, err := repo.Log(&gogit.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer iter.Close()

	var commits []CommitInfo
	for i := 0; i < limit; i++ {
		c, err := iter.Next()
		if err != nil {
			break
		}
		commits = append(commits, CommitInfo{
			Hash:    c.Hash.String()[:8],
			Message: c.Message,
			Author:  c.Author.Name,
			When:    c.Author.When.Format(time.RFC3339),
		})
	}
	return commits, nil
}

// StageFile stages a single file (git add -- <file>).
func (s *Service) StageFile(repoPath, filePath string) error {
	cmd := exec.Command("git", "add", "--", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("stage file: %w: %s", err, out)
	}
	return nil
}

// UnstageFile removes a file from the staging area (git restore --staged -- <file>).
func (s *Service) UnstageFile(repoPath, filePath string) error {
	cmd := exec.Command("git", "restore", "--staged", "--", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unstage file: %w: %s", err, out)
	}
	return nil
}

// DiscardFile discards working-tree changes for a file (git checkout -- <file>).
// For untracked files it removes the file entirely.
func (s *Service) DiscardFile(repoPath, filePath string) error {
	cmd := exec.Command("git", "checkout", "--", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("discard file: %w: %s", err, out)
	}
	return nil
}

// CommitFileInfo describes one file changed in a commit.
type CommitFileInfo struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// GetCommitFiles returns the list of files changed in a given commit.
func (s *Service) GetCommitFiles(repoPath, hash string) ([]CommitFileInfo, error) {
	cmd := exec.Command("git", "show", "--name-status", "--pretty=format:", hash)
	cmd.Dir = repoPath
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("get commit files: %w", err)
	}
	var files []CommitFileInfo
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		files = append(files, CommitFileInfo{Status: parts[0], Path: parts[len(parts)-1]})
	}
	return files, nil
}

// GetCommitFileDiff returns the unified diff of a single file at the given commit.
func (s *Service) GetCommitFileDiff(repoPath, hash, filePath string) (string, error) {
	// Try diff against parent; for the initial commit fall back to show.
	run := func(args ...string) (string, bool) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Run()
		return buf.String(), buf.Len() > 0
	}
	if diff, ok := run("diff", hash+"^", hash, "--", filePath); ok {
		return diff, nil
	}
	// Initial commit or rename – fall back to git show
	diff, _ := run("show", hash, "--", filePath)
	return diff, nil
}

// RestoreFileFromCommit restores a single file to its state at the given commit.
func (s *Service) RestoreFileFromCommit(repoPath, hash, filePath string) error {
	cmd := exec.Command("git", "checkout", hash, "--", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore file: %w: %s", err, out)
	}
	return nil
}

// RevertCommit creates a new commit that undoes the changes introduced by the given commit hash.
func (s *Service) RevertCommit(repoPath, hash string) error {
	cmd := exec.Command("git", "revert", "--no-edit", hash)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("revert commit: %w: %s", err, out)
	}
	return nil
}

// CommitInfo is a trimmed representation of a commit.
type CommitInfo struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
	Author  string `json:"author"`
	When    string `json:"when"`
}

// BranchInfo represents a git branch.
type BranchInfo struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"isCurrent"`
}

// GetBranches returns all local branches.
func (s *Service) GetBranches(repoPath string) ([]BranchInfo, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	head, err := repo.Head()
	currentBranch := ""
	if err == nil {
		currentBranch = head.Name().Short()
	}

	branches, err := repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	var result []BranchInfo
	err = branches.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		result = append(result, BranchInfo{
			Name:      name,
			IsCurrent: name == currentBranch,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterate branches: %w", err)
	}

	return result, nil
}

// CheckoutBranch switches to the specified branch.
func (s *Service) CheckoutBranch(repoPath, branchName string) error {
	cmd := exec.Command("git", "checkout", branchName)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout branch: %w: %s", err, out)
	}
	return nil
}
