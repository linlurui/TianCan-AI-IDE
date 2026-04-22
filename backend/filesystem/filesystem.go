package filesystem

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// GrepMatch represents a single search hit.
type GrepMatch struct {
	FilePath   string `json:"filePath"`
	FileName   string `json:"fileName"`
	LineNumber int    `json:"lineNumber"`
	LineText   string `json:"lineText"`
}

var binaryExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "bmp": true,
	"svg": true, "ico": true, "webp": true,
	"woff": true, "woff2": true, "ttf": true, "eot": true,
	"zip": true, "tar": true, "gz": true, "rar": true, "7z": true,
	"pdf": true, "doc": true, "docx": true, "xls": true, "xlsx": true,
	"exe": true, "bin": true, "so": true, "dylib": true, "dll": true,
	"lock": true, "sum": true,
}

// GrepFiles searches all text files under rootPath for query (case-insensitive)
// using a parallel worker pool. Returns up to maxResults matches.
func (s *Service) GrepFiles(rootPath, query string, maxResults int) ([]GrepMatch, error) {
	if query == "" {
		return nil, nil
	}
	if maxResults <= 0 {
		maxResults = 200
	}
	lower := strings.ToLower(query)

	// Collect candidate files first (fast, single-threaded walk)
	var files []string
	_ = filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || skipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
		if binaryExts[ext] {
			return nil
		}
		files = append(files, path)
		return nil
	})

	// Parallel search with worker pool
	numWorkers := runtime.NumCPU()
	if numWorkers > 8 {
		numWorkers = 8
	}

	type fileJob struct{ path string }
	jobs := make(chan fileJob, len(files))
	resultCh := make(chan GrepMatch, 256)

	var found int64 // atomic counter
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if atomic.LoadInt64(&found) >= int64(maxResults) {
					continue
				}
				searchFile(job.path, lower, maxResults, &found, resultCh)
			}
		}()
	}

	for _, f := range files {
		jobs <- fileJob{f}
	}
	close(jobs)

	// Close resultCh after all workers finish
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var results []GrepMatch
	for m := range resultCh {
		results = append(results, m)
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

func searchFile(path, lower string, maxResults int, found *int64, out chan<- GrepMatch) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	name := filepath.Base(path)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)
	lineNum := 0
	for scanner.Scan() {
		if atomic.LoadInt64(found) >= int64(maxResults) {
			return
		}
		lineNum++
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), lower) {
			atomic.AddInt64(found, 1)
			out <- GrepMatch{
				FilePath:   path,
				FileName:   name,
				LineNumber: lineNum,
				LineText:   strings.TrimSpace(line),
			}
		}
	}
}

// Service exposes file system operations to the frontend via Wails.
type Service struct {
	workingDir string
	App        *application.App
}

// SelectDirectory opens a native folder-picker dialog and returns the chosen path.
func (s *Service) SelectDirectory() (string, error) {
	if s.App == nil {
		return "", fmt.Errorf("app not initialized")
	}
	path, err := s.App.Dialog.
		OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("选择项目目录").
		PromptForSingleSelection()
	return path, err
}

// SelectFile opens a native file-picker dialog and returns the chosen file path.
// extensions is a comma-separated list like "db,sqlite,sqlite3" (without dots).
func (s *Service) SelectFile(title string, extensions string) (string, error) {
	if s.App == nil {
		return "", fmt.Errorf("app not initialized")
	}
	d := s.App.Dialog.
		OpenFile().
		CanChooseDirectories(false).
		CanChooseFiles(true).
		SetTitle(title)
	return d.PromptForSingleSelection()
}

// FileNode represents a single entry in the directory tree.
type FileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"isDir"`
	Children []FileNode `json:"children,omitempty"`
	Ext      string     `json:"ext"`
}

// ReadFile reads a file and returns its content as a string.
func (s *Service) ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(data), nil
}

// ReadFileAsBase64 reads a binary file and returns its content as a base64-encoded string.
func (s *Service) ReadFileAsBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// WriteFile writes content to the given path, creating directories as needed.
func (s *Service) WriteFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dirs: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// ListDirectory returns a flat list of entries one level deep.
func (s *Service) ListDirectory(path string) ([]FileNode, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	nodes := make([]FileNode, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(path, e.Name())
		nodes = append(nodes, FileNode{
			Name:  e.Name(),
			Path:  full,
			IsDir: e.IsDir(),
			Ext:   strings.TrimPrefix(filepath.Ext(e.Name()), "."),
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	return nodes, nil
}

// GetDirectoryTree returns the full recursive directory tree (max depth 10).
func (s *Service) GetDirectoryTree(path string) (FileNode, error) {
	return buildTree(path, 0, 10)
}

// skipDirs are directories that should be excluded from the tree view.
var skipDirs = map[string]bool{
	"node_modules": true,
	"target":       true,
	".git":         true,
	".gradle":      true,
	"build":        true,
	"dist":         true,
	"out":          true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".idea":        true,
	".vscode":      true,
	".DS_Store":    true,
}

func buildTree(path string, depth, maxDepth int) (FileNode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileNode{}, err
	}

	node := FileNode{
		Name:  filepath.Base(path),
		Path:  path,
		IsDir: info.IsDir(),
		Ext:   strings.TrimPrefix(filepath.Ext(path), "."),
	}

	if !info.IsDir() || depth >= maxDepth {
		return node, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return node, nil
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		child, err := buildTree(filepath.Join(path, name), depth+1, maxDepth)
		if err != nil {
			continue
		}
		node.Children = append(node.Children, child)
	}

	sort.Slice(node.Children, func(i, j int) bool {
		if node.Children[i].IsDir != node.Children[j].IsDir {
			return node.Children[i].IsDir
		}
		return strings.ToLower(node.Children[i].Name) < strings.ToLower(node.Children[j].Name)
	})
	return node, nil
}

// GetWorkingDirectory returns the process working directory.
func (s *Service) GetWorkingDirectory() (string, error) {
	if s.workingDir != "" {
		return s.workingDir, nil
	}
	return os.Getwd()
}

// SetWorkingDirectory changes the active project root.
func (s *Service) SetWorkingDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	s.workingDir = path
	return nil
}

// FileExists reports whether the given path exists.
func (s *Service) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CreateFile creates an empty file at the given path, creating parent directories as needed.
func (s *Service) CreateFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dirs: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	return f.Close()
}

// MkDir creates a directory (and all parents) at the given path.
func (s *Service) MkDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return nil
}

// RenameFile renames (or moves) a file or directory from oldPath to newPath.
func (s *Service) RenameFile(oldPath, newPath string) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// DeleteFile removes a file or recursively removes a directory.
func (s *Service) DeleteFile(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}
