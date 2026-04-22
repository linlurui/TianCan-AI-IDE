package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── TodoWriteTool ───────────────────────────────────────────────

// TodoWriteTool updates the task list for the current session.
type TodoWriteTool struct {
	mu    sync.Mutex
	items []TodoItem
	file  string
}

type TodoItem struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`   // pending, in_progress, completed
	Priority string `json:"priority"` // high, medium, low
}

func (t *TodoWriteTool) Name() string { return "todo_write" }
func (t *TodoWriteTool) Description() string {
	return "Update the todo/task list. Creates, updates, or reorganizes tasks. " +
		"Args: todos (required: array of {id, content, status, priority})"
}
func (t *TodoWriteTool) IsReadOnly() bool        { return false }
func (t *TodoWriteTool) IsConcurrencySafe() bool { return true }
func (t *TodoWriteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":       map[string]interface{}{"type": "string"},
						"content":  map[string]interface{}{"type": "string"},
						"status":   map[string]interface{}{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
						"priority": map[string]interface{}{"type": "string", "enum": []string{"high", "medium", "low"}},
					},
					"required": []string{"id", "content", "status", "priority"},
				},
			},
		},
		"required": []string{"todos"},
	}
}
func (t *TodoWriteTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	todosRaw, ok := args["todos"].([]interface{})
	if !ok || len(todosRaw) == 0 {
		return types.ToolResult{}, fmt.Errorf("missing or empty required argument: todos")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	var items []TodoItem
	for i, raw := range todosRaw {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return types.ToolResult{Content: fmt.Sprintf("todos[%d] is not an object", i), Success: false, Error: "invalid todo"}, nil
		}
		item := TodoItem{
			ID:       toString(m["id"]),
			Content:  toString(m["content"]),
			Status:   toString(m["status"]),
			Priority: toString(m["priority"]),
		}
		if item.ID == "" {
			item.ID = fmt.Sprintf("%d", i+1)
		}
		if item.Status == "" {
			item.Status = "pending"
		}
		if item.Priority == "" {
			item.Priority = "medium"
		}
		items = append(items, item)
	}

	t.items = items

	// Persist to file if path is set
	if t.file != "" {
		data, _ := json.MarshalIndent(items, "", "  ")
		os.WriteFile(t.file, data, 0644)
	}

	// Build summary
	var lines []string
	for _, item := range items {
		statusIcon := "○"
		switch item.Status {
		case "in_progress":
			statusIcon = "◐"
		case "completed":
			statusIcon = "●"
		}
		priorityIcon := ""
		switch item.Priority {
		case "high":
			priorityIcon = "🔴"
		case "medium":
			priorityIcon = "🟡"
		case "low":
			priorityIcon = "🟢"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s", statusIcon, priorityIcon, item.ID, item.Content))
	}

	return types.ToolResult{
		Content: fmt.Sprintf("Todo list updated (%d items):\n%s", len(items), strings.Join(lines, "\n")),
		Success: true,
	}, nil
}

// SetFile sets the persistence file path.
func (t *TodoWriteTool) SetFile(path string) {
	t.file = path
}

// GetItems returns current todo items.
func (t *TodoWriteTool) GetItems() []TodoItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TodoItem{}, t.items...)
}

// LoadFromFile loads todos from a JSON file.
func (t *TodoWriteTool) LoadFromFile(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	t.items = items
	t.file = path
	return nil
}

// ── TodoReadTool ────────────────────────────────────────────────

// TodoReadTool reads the current todo list.
type TodoReadTool struct {
	writeTool *TodoWriteTool
}

func (t *TodoReadTool) Name() string { return "todo_read" }
func (t *TodoReadTool) Description() string {
	return "Read the current todo/task list. Returns all items with their status and priority."
}
func (t *TodoReadTool) IsReadOnly() bool        { return true }
func (t *TodoReadTool) IsConcurrencySafe() bool { return true }
func (t *TodoReadTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{},
	}
}
func (t *TodoReadTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	items := t.writeTool.GetItems()
	if len(items) == 0 {
		return types.ToolResult{Content: "No todo items. Use todo_write to create tasks.", Success: true}, nil
	}

	var lines []string
	for _, item := range items {
		statusIcon := "○"
		switch item.Status {
		case "in_progress":
			statusIcon = "◐"
		case "completed":
			statusIcon = "●"
		}
		priorityIcon := ""
		switch item.Priority {
		case "high":
			priorityIcon = "🔴"
		case "medium":
			priorityIcon = "🟡"
		case "low":
			priorityIcon = "🟢"
		}
		lines = append(lines, fmt.Sprintf("%s %s [%s] %s", statusIcon, priorityIcon, item.ID, item.Content))
	}

	return types.ToolResult{
		Content: fmt.Sprintf("Todo list (%d items):\n%s", len(items), strings.Join(lines, "\n")),
		Success: true,
	}, nil
}

// ── Helper ──────────────────────────────────────────────────────

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return fmt.Sprintf("%.0f", s)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// InitTodoTools creates a linked pair of TodoWriteTool and TodoReadTool.
// The write tool persists to <rootPath>/.tiancan/todos.json.
func InitTodoTools(rootPath string) (*TodoWriteTool, *TodoReadTool) {
	writeTool := &TodoWriteTool{}
	todoDir := filepath.Join(rootPath, ".tiancan")
	os.MkdirAll(todoDir, 0755)
	todoFile := filepath.Join(todoDir, "todos.json")
	writeTool.SetFile(todoFile)
	writeTool.LoadFromFile(todoFile)

	readTool := &TodoReadTool{writeTool: writeTool}
	return writeTool, readTool
}
