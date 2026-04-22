package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ExpandTilde expands ~ to the user's home directory.
// Mirrors Claude Code's expandTilde utility.
func ExpandTilde(path string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		if home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// ExpandPath expands environment variables and tilde in a path.
// Mirrors Claude Code's expandPath utility.
func ExpandPath(path string) string {
	if path == "" {
		return path
	}
	// Expand tilde first
	path = ExpandTilde(path)
	// Expand environment variables ($HOME, ${HOME}, etc.)
	path = os.ExpandEnv(path)
	return path
}

// ValidatePath checks if a path is safe to access.
// Returns an error if the path is outside the allowed root or in a protected directory.
// Mirrors Claude Code's path validation logic.
func ValidatePath(path string, rootPath string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}

	// Expand the path
	expanded := ExpandPath(path)
	absPath, err := filepath.Abs(expanded)
	if err != nil {
		return fmt.Errorf("cannot resolve absolute path: %w", err)
	}

	// Check if outside root
	if rootPath != "" {
		absRoot, err := filepath.Abs(rootPath)
		if err != nil {
			absRoot = rootPath
		}
		if !strings.HasPrefix(absPath, absRoot) {
			return fmt.Errorf("path %s is outside project root %s", absPath, absRoot)
		}
	}

	// Check for directory traversal attempts
	cleanPath := filepath.Clean(absPath)
	if strings.Contains(cleanPath, "..") {
		// After cleaning, ".." should not remain — this indicates a traversal attempt
		return fmt.Errorf("path contains directory traversal: %s", path)
	}

	return nil
}

// IsProtectedPath checks if a path points to a protected system directory.
// Protected dirs are loaded from TIANCAN_PROTECTED_PATHS env var — no hardcoded lists.
// Mirrors Claude Code's protected path checks.
func IsProtectedPath(path string) bool {
	protectedDirs := loadProtectedDirs()

	expanded := ExpandPath(path)
	absPath, err := filepath.Abs(expanded)
	if err != nil {
		absPath = path
	}

	for _, dir := range protectedDirs {
		if strings.Contains(absPath, "/"+dir+"/") || strings.HasSuffix(absPath, "/"+dir) {
			return true
		}
	}
	return false
}

// protectedDirsCache caches the env-loaded list.
var protectedDirsCache []string
var protectedDirsOnce sync.Once

// loadProtectedDirs loads protected directory names from env.
// TIANCAN_PROTECTED_PATHS=.git,.ssh,.gnupg,...
func loadProtectedDirs() []string {
	protectedDirsOnce.Do(func() {
		if v := os.Getenv("TIANCAN_PROTECTED_PATHS"); v != "" {
			for _, p := range strings.Split(v, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					protectedDirsCache = append(protectedDirsCache, p)
				}
			}
		}
		if len(protectedDirsCache) == 0 {
			protectedDirsCache = []string{".git", ".tiancan", ".ssh", ".gnupg"}
		}
	})
	return protectedDirsCache
}

// GetGlobBaseDir extracts the base directory from a glob pattern.
// Returns the longest non-glob prefix of the pattern.
// Mirrors Claude Code's glob base directory resolution.
func GetGlobBaseDir(pattern string) string {
	if pattern == "" {
		return "."
	}

	// Expand the pattern
	pattern = ExpandPath(pattern)

	// Find the first glob character
	globChars := `*?[`
	re := regexp.MustCompile("[" + regexp.QuoteMeta(globChars) + "]")
	idx := re.FindStringIndex(pattern)
	if idx == nil {
		// No glob characters — the pattern itself is the base
		return filepath.Dir(pattern)
	}

	// Get the directory portion before the first glob
	baseDir := pattern[:idx[0]]
	lastSep := strings.LastIndex(baseDir, string(filepath.Separator))
	if lastSep >= 0 {
		baseDir = baseDir[:lastSep]
	}
	if baseDir == "" {
		baseDir = "."
	}
	return baseDir
}

// ResolveProjectPath resolves a path relative to the project root.
// Handles absolute paths, relative paths, and user home directory references.
func ResolveProjectPath(path string, rootPath string) string {
	if path == "" {
		return rootPath
	}

	expanded := ExpandPath(path)

	// If already absolute, return as-is
	if filepath.IsAbs(expanded) {
		return expanded
	}

	// Resolve relative to root
	return filepath.Join(rootPath, expanded)
}

// IsSubPath checks if child is a subdirectory of parent.
func IsSubPath(parent, child string) bool {
	absParent, err := filepath.Abs(parent)
	if err != nil {
		absParent = parent
	}
	absChild, err := filepath.Abs(child)
	if err != nil {
		absChild = child
	}

	// Ensure consistent separators
	absParent = filepath.Clean(absParent)
	absChild = filepath.Clean(absChild)

	if absParent == absChild {
		return true
	}
	return strings.HasPrefix(absChild, absParent+string(filepath.Separator))
}

// SafeJoin joins path elements with protection against traversal.
func SafeJoin(base string, elem ...string) (string, error) {
	joined := filepath.Join(append([]string{base}, elem...)...)
	absBase, _ := filepath.Abs(base)
	absJoined, _ := filepath.Abs(joined)

	if absBase != "" && !strings.HasPrefix(absJoined, absBase) {
		return "", fmt.Errorf("path traversal detected: %s escapes %s", joined, base)
	}
	return joined, nil
}
