package window

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// WindowInfo tracks a managed window's state.
type WindowInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Mode      string    `json:"mode"` // "editor", "terminal", "ai", "debug", "main"
	URL       string    `json:"url"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	IsActive  bool      `json:"isActive"`
	CreatedAt time.Time `json:"createdAt"`
}

// Manager manages multi-window lifecycle and inter-window communication.
type Manager struct {
	mu      sync.RWMutex
	app     *application.App
	windows map[string]*WindowInfo
}

// NewManager creates a new window manager.
func NewManager() *Manager {
	return &Manager{
		windows: make(map[string]*WindowInfo),
	}
}

// SetApp injects the Wails application instance.
func (m *Manager) SetApp(app *application.App) {
	m.app = app
}

// RegisterWindow tracks a window after creation.
func (m *Manager) RegisterWindow(id, name, mode string, width, height int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows[id] = &WindowInfo{
		ID: id, Name: name, Mode: mode,
		URL:   "/?window=" + mode,
		Width: width, Height: height,
		IsActive:  true,
		CreatedAt: time.Now(),
	}
}

// ListWindows returns all managed windows.
func (m *Manager) ListWindows() []WindowInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]WindowInfo, 0, len(m.windows))
	for _, w := range m.windows {
		out = append(out, *w)
	}
	return out
}

// OpenWindow creates a new managed window at runtime.
func (m *Manager) OpenWindow(mode string) error {
	if m.app == nil {
		return fmt.Errorf("window manager: app not initialized")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if window of this mode already exists
	for _, w := range m.windows {
		if w.Mode == mode && w.IsActive {
			return fmt.Errorf("window mode %s already open", mode)
		}
	}

	titles := map[string]string{
		"editor":   "天蚕 IDE — 编辑器",
		"terminal": "天蚕 IDE — 终端",
		"ai":       "天蚕 IDE — AI 助手",
		"debug":    "天蚕 IDE — 调试",
	}
	sizes := map[string][2]int{
		"editor":   {1000, 800},
		"terminal": {700, 500},
		"ai":       {500, 700},
		"debug":    {600, 500},
	}

	title := titles[mode]
	if title == "" {
		title = "天蚕 IDE"
	}
	dim := sizes[mode]
	if dim[0] == 0 {
		dim = [2]int{800, 600}
	}

	win := m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           title,
		Width:           dim[0],
		Height:          dim[1],
		MinWidth:        400,
		MinHeight:       300,
		DevToolsEnabled: true,
		URL:             "/?window=" + mode,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInsetUnified,
		},
	})

	id := fmt.Sprintf("win-%d", time.Now().UnixNano())
	m.windows[id] = &WindowInfo{
		ID: id, Name: title, Mode: mode,
		URL:   "/?window=" + mode,
		Width: dim[0], Height: dim[1],
		IsActive:  true,
		CreatedAt: time.Now(),
	}

	_ = win // window is managed by Wails runtime
	return nil
}

// CloseWindow closes a window by mode.
func (m *Manager) CloseWindow(mode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, w := range m.windows {
		if w.Mode == mode && w.IsActive {
			w.IsActive = false
			delete(m.windows, id)
			return nil
		}
	}
	return fmt.Errorf("no active window with mode %s", mode)
}

// BroadcastEvent sends an event to all windows via the Wails event system.
func (m *Manager) BroadcastEvent(eventName string, data interface{}) error {
	if m.app == nil {
		return fmt.Errorf("window manager: app not initialized")
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal event data: %w", err)
	}

	// Use Wails runtime event emission
	m.app.Event.Emit(eventName, string(jsonData))
	return nil
}

// GetActiveWindowMode returns the mode of the currently active window.
func (m *Manager) GetActiveWindowMode() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, w := range m.windows {
		if w.IsActive {
			return w.Mode
		}
	}
	return "main"
}

// IsMultiWindowEnabled returns whether multi-window mode is active.
func (m *Manager) IsMultiWindowEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.windows) > 1
}
