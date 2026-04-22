package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// validatePath ensures the path is within the project root and not in a protected directory.
func validatePath(path, rootPath string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %s", path)
	}
	absRoot, _ := filepath.Abs(rootPath)
	if strings.HasPrefix(absPath, absRoot) {
		protected := []string{".git", ".tiancan", ".ssh", ".gnupg"}
		for _, p := range protected {
			sep := string(filepath.Separator)
			if strings.Contains(absPath, sep+p+sep) || strings.HasSuffix(absPath, sep+p) {
				return fmt.Errorf("access denied: path is in a protected directory (%s)", p)
			}
		}
		return nil
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(absPath, home) {
		return nil
	}
	if strings.HasPrefix(absPath, "/tmp") || strings.HasPrefix(absPath, os.TempDir()) {
		return nil
	}
	return fmt.Errorf("path %s is outside project root %s", path, rootPath)
}

// isBinaryFile checks if a file appears to be binary by extension.
// Binary extensions are loaded from TIANCAN_BINARY_EXTS env var — no hardcoded lists.
func isBinaryFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return loadBinaryExts()[ext]
}

// binaryExtsCache caches the env-loaded binary extension set.
var binaryExtsCache map[string]bool
var binaryExtsOnce sync.Once

// loadBinaryExts loads binary file extensions from TIANCAN_BINARY_EXTS env var.
// TIANCAN_BINARY_EXTS=.png,.jpg,.jpeg,.gif,.exe,.dll,...
func loadBinaryExts() map[string]bool {
	binaryExtsOnce.Do(func() {
		binaryExtsCache = make(map[string]bool)
		if v := os.Getenv("TIANCAN_BINARY_EXTS"); v != "" {
			for _, e := range strings.Split(v, ",") {
				e = strings.TrimSpace(e)
				if e != "" {
					binaryExtsCache[e] = true
				}
			}
		}
		if len(binaryExtsCache) == 0 {
			binaryExtsCache = map[string]bool{
				".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
				".ico": true, ".webp": true, ".svg": true, ".tiff": true, ".tif": true,
				".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".rar": true,
				".7z": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
				".o": true, ".a": true, ".class": true, ".pyc": true, ".pyd": true,
				".wasm": true, ".sqlite": true, ".db": true, ".woff": true, ".woff2": true,
				".eot": true, ".ttf": true, ".otf": true, ".mp3": true, ".mp4": true,
				".wav": true, ".avi": true, ".mov": true, ".mkv": true, ".flac": true,
			}
		}
	})
	return binaryExtsCache
}

// toInt extracts an integer from args with a default.
func toInt(v interface{}, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return def
	}
}

// findStringLineNumbers returns 1-indexed line numbers where s appears in content.
func findStringLineNumbers(content, s string) []int {
	var nums []int
	line := 1
	searchFrom := 0
	for {
		idx := strings.Index(content[searchFrom:], s)
		if idx < 0 {
			break
		}
		// Count newlines between searchFrom and idx
		line += strings.Count(content[searchFrom:searchFrom+idx], "\n")
		nums = append(nums, line)
		searchFrom = searchFrom + idx + 1
	}
	return nums
}

// truncateOutput limits output to maxLines, adding a truncation notice.
func truncateOutput(output string, maxLines int) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	return strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n... (%d more lines truncated, total %d)", len(lines)-maxLines, len(lines))
}
