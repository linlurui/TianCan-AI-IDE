package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileEvent represents a file change event.
type FileEvent struct {
	Path      string    `json:"path"`
	Operation string    `json:"operation"` // "create", "write", "remove", "rename", "chmod"
	Timestamp time.Time `json:"timestamp"`
}

// Callback is called when a file change is detected.
type Callback func(event FileEvent)

// Watcher monitors file system changes in a project.
type Watcher struct {
	rootPath  string
	watcher   *fsnotify.Watcher
	callbacks []Callback
	mu        sync.RWMutex
	running   bool
	debounce  time.Duration
	// Track recent events to debounce
	pending   map[string]time.Time
	pendingMu sync.Mutex
	// Ignore patterns
	ignorePatterns []string
}

// NewWatcher creates a new file system watcher.
func NewWatcher(rootPath string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	w := &Watcher{
		rootPath:  rootPath,
		watcher:   fsw,
		debounce:  300 * time.Millisecond,
		pending:   make(map[string]time.Time),
		ignorePatterns: []string{
			".git", "node_modules", "vendor", "__pycache__",
			".tiancan", "dist", "build", ".next", "target",
			".DS_Store", "*.pyc", "*.swp",
		},
	}

	return w, nil
}

// OnChange registers a callback for file change events.
func (w *Watcher) OnChange(cb Callback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

// Start begins watching the project directory tree.
func (w *Watcher) Start() error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	w.mu.Unlock()

	// Add root and all subdirectories
	if err := w.addWatchTree(w.rootPath); err != nil {
		return fmt.Errorf("add watch tree: %w", err)
	}

	// Process events
	go w.processEvents()

	// Periodically scan for new directories (fsnotify doesn't auto-watch new dirs)
	go w.rescanLoop()

	return nil
}

// Stop stops the watcher.
func (w *Watcher) Stop() {
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
	w.watcher.Close()
}

// AddIgnorePattern adds a pattern to ignore.
func (w *Watcher) AddIgnorePattern(pattern string) {
	w.ignorePatterns = append(w.ignorePatterns, pattern)
}

// --- internal ---

func (w *Watcher) addWatchTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && w.shouldIgnore(path) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if err := w.watcher.Add(path); err != nil {
				// Log but don't fail for individual directories
				return nil
			}
		}
		return nil
	})
}

func (w *Watcher) shouldIgnore(path string) bool {
	name := filepath.Base(path)
	for _, pattern := range w.ignorePatterns {
		if strings.Contains(pattern, "*") {
			if matched, _ := filepath.Match(pattern, name); matched {
				return true
			}
		}
		if name == pattern || strings.Contains(path, string(filepath.Separator)+pattern+string(filepath.Separator)) {
			return true
		}
	}
	// Ignore hidden directories
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}
	return false
}

func (w *Watcher) processEvents() {
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	path := event.Name
	if w.shouldIgnore(path) {
		return
	}

	// Map fsnotify operations to our operation names
	operation := ""
	switch {
	case event.Has(fsnotify.Create):
		operation = "create"
		// Watch new directories
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			w.watcher.Add(path)
		}
	case event.Has(fsnotify.Write):
		operation = "write"
	case event.Has(fsnotify.Remove):
		operation = "remove"
	case event.Has(fsnotify.Rename):
		operation = "rename"
	case event.Has(fsnotify.Chmod):
		operation = "chmod"
	}
	if operation == "" {
		return
	}

	// Debounce: skip if we've seen this path very recently
	w.pendingMu.Lock()
	if lastTime, ok := w.pending[path]; ok && time.Since(lastTime) < w.debounce {
		w.pendingMu.Unlock()
		return
	}
	w.pending[path] = time.Now()
	w.pendingMu.Unlock()

	// Clean up old pending entries periodically
	go w.cleanPending()

	// Notify callbacks
	evt := FileEvent{
		Path:      path,
		Operation: operation,
		Timestamp: time.Now(),
	}

	w.mu.RLock()
	callbacks := make([]Callback, len(w.callbacks))
	copy(callbacks, w.callbacks)
	w.mu.RUnlock()

	for _, cb := range callbacks {
		cb(evt)
	}
}

func (w *Watcher) rescanLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		w.mu.RLock()
		running := w.running
		w.mu.RUnlock()
		if !running {
			return
		}
		w.addWatchTree(w.rootPath)
	}
}

func (w *Watcher) cleanPending() {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	now := time.Now()
	for path, t := range w.pending {
		if now.Sub(t) > 5*time.Second {
			delete(w.pending, path)
		}
	}
}
