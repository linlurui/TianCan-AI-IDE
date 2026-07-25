package ralph

import (
	"fmt"
	"sync"
	"time"
)

// TaskFile represents a file modified during an agent task.
type TaskFile struct {
	Path         string    `json:"path"`
	OriginalHash string    `json:"originalHash"` // hash before modification
	NewHash      string    `json:"newHash"`      // hash after modification
	OldContent   string    `json:"oldContent"`   // original content (for rollback)
	NewContent   string    `json:"newContent"`   // new content
	Status       string    `json:"status"`       // pending, applied, rejected, modified
	ModifiedAt   time.Time `json:"modifiedAt"`
}

// AgentTask represents a task tracked by the agent system.
type AgentTask struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Status        string      `json:"status"` // running, completed, failed
	ParentID      string      `json:"parentId,omitempty"`
	Children      []*AgentTask `json:"children,omitempty"`
	ModifiedFiles []TaskFile  `json:"modifiedFiles"`
	Summary       string      `json:"summary"`
	Error         string      `json:"error,omitempty"`
	CreatedAt     time.Time   `json:"createdAt"`
	CompletedAt   *time.Time  `json:"completedAt,omitempty"`
}

// TaskManager manages all agent tasks and provides frontend bindings.
type TaskManager struct {
	mu         sync.RWMutex
	tasks      map[string]*AgentTask
	rootTasks  []*AgentTask // tasks with no parent
	pendingOps map[string]func() // pending apply/reject operations
}

var (
	globalTaskManager     *TaskManager
	globalTaskManagerOnce sync.Once
)

// GetTaskManager returns the global task manager instance.
func GetTaskManager() *TaskManager {
	globalTaskManagerOnce.Do(func() {
		globalTaskManager = &TaskManager{
			tasks:      make(map[string]*AgentTask),
			rootTasks:  make([]*AgentTask, 0),
			pendingOps: make(map[string]func()),
		}
	})
	return globalTaskManager
}

// CreateTask creates a new agent task.
func (tm *TaskManager) CreateTask(name string, parentID string) *AgentTask {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task := &AgentTask{
		ID:            fmt.Sprintf("task-%d", time.Now().UnixNano()),
		Name:          name,
		Status:        "running",
		ParentID:      parentID,
		ModifiedFiles: make([]TaskFile, 0),
		CreatedAt:     time.Now(),
	}

	tm.tasks[task.ID] = task

	if parentID != "" {
		if parent, ok := tm.tasks[parentID]; ok {
			parent.Children = append(parent.Children, task)
		}
	} else {
		tm.rootTasks = append(tm.rootTasks, task)
	}

	return task
}

// GetTask returns a task by ID.
func (tm *TaskManager) GetTask(taskID string) *AgentTask {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.tasks[taskID]
}

// GetAllTasks returns all root tasks with their children.
func (tm *TaskManager) GetAllTasks() []*AgentTask {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make([]*AgentTask, len(tm.rootTasks))
	copy(result, tm.rootTasks)
	return result
}

// GetTasksForFrontend returns tasks in a format suitable for frontend binding.
func (tm *TaskManager) GetTasksForFrontend() []map[string]interface{} {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make([]map[string]interface{}, 0, len(tm.rootTasks))
	for _, task := range tm.rootTasks {
		result = append(result, taskToMap(task))
	}
	return result
}

func taskToMap(task *AgentTask) map[string]interface{} {
	m := map[string]interface{}{
		"id":            task.ID,
		"name":          task.Name,
		"status":        task.Status,
		"parentId":      task.ParentID,
		"summary":       task.Summary,
		"error":         task.Error,
		"createdAt":     task.CreatedAt.Unix(),
		"modifiedFiles": task.ModifiedFiles,
	}

	if task.CompletedAt != nil {
		m["completedAt"] = task.CompletedAt.Unix()
	}

	if len(task.Children) > 0 {
		children := make([]map[string]interface{}, len(task.Children))
		for i, child := range task.Children {
			children[i] = taskToMap(child)
		}
		m["children"] = children
	}

	return m
}

// AddModifiedFile adds a file modification to a task.
func (tm *TaskManager) AddModifiedFile(taskID string, file TaskFile) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, ok := tm.tasks[taskID]
	if !ok {
		return
	}

	// Check if file already exists
	for i, f := range task.ModifiedFiles {
		if f.Path == file.Path {
			task.ModifiedFiles[i] = file
			return
		}
	}

	task.ModifiedFiles = append(task.ModifiedFiles, file)
}

// CompleteTask marks a task as completed.
func (tm *TaskManager) CompleteTask(taskID string, summary string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, ok := tm.tasks[taskID]
	if !ok {
		return
	}

	task.Status = "completed"
	task.Summary = summary
	now := time.Now()
	task.CompletedAt = &now
}

// FailTask marks a task as failed.
func (tm *TaskManager) FailTask(taskID string, errMsg string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, ok := tm.tasks[taskID]
	if !ok {
		return
	}

	task.Status = "failed"
	task.Error = errMsg
	now := time.Now()
	task.CompletedAt = &now
}

// ApplyTask applies all changes from a task (accepts all modified files).
func (tm *TaskManager) ApplyTask(taskID string) (string, error) {
	tm.mu.Lock()
	task, ok := tm.tasks[taskID]
	tm.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	// Apply each file
	for _, f := range task.ModifiedFiles {
		if f.Status == "pending" || f.Status == "rejected" {
			f.Status = "applied"
		}
	}

	task.Status = "applied"
	return fmt.Sprintf("Applied %d files from task %s", len(task.ModifiedFiles), taskID), nil
}

// RejectTask rejects all changes from a task (restores original content).
func (tm *TaskManager) RejectTask(taskID string) (string, error) {
	tm.mu.Lock()
	task, ok := tm.tasks[taskID]
	tm.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	// Mark all files as rejected
	for i := range task.ModifiedFiles {
		task.ModifiedFiles[i].Status = "rejected"
	}

	task.Status = "rejected"
	return fmt.Sprintf("Rejected %d files from task %s", len(task.ModifiedFiles), taskID), nil
}

// ApplyFile applies a single file from a task.
func (tm *TaskManager) ApplyFile(taskID string, filePath string) (string, error) {
	tm.mu.Lock()
	task, ok := tm.tasks[taskID]
	tm.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	for i, f := range task.ModifiedFiles {
		if f.Path == filePath {
			task.ModifiedFiles[i].Status = "applied"
			return fmt.Sprintf("Applied file: %s", filePath), nil
		}
	}

	return "", fmt.Errorf("file not found in task: %s", filePath)
}

// RejectFile rejects a single file from a task (restores original content).
func (tm *TaskManager) RejectFile(taskID string, filePath string) (string, error) {
	tm.mu.Lock()
	task, ok := tm.tasks[taskID]
	tm.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	for i, f := range task.ModifiedFiles {
		if f.Path == filePath {
			task.ModifiedFiles[i].Status = "rejected"
			return fmt.Sprintf("Rejected file: %s", filePath), nil
		}
	}

	return "", fmt.Errorf("file not found in task: %s", filePath)
}

// GetFileOriginalContent returns the original content of a file from a task.
func (tm *TaskManager) GetFileOriginalContent(taskID string, filePath string) (string, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[taskID]
	tm.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	for _, f := range task.ModifiedFiles {
		if f.Path == filePath {
			return f.OldContent, nil
		}
	}

	return "", fmt.Errorf("file not found in task: %s", filePath)
}

// CleanupOldTasks removes tasks older than the specified duration.
func (tm *TaskManager) CleanupOldTasks(olderThan time.Duration) int {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	removed := 0

	// Filter root tasks
	newRootTasks := make([]*AgentTask, 0, len(tm.rootTasks))
	for _, task := range tm.rootTasks {
		if task.CreatedAt.After(cutoff) {
			newRootTasks = append(newRootTasks, task)
		} else {
			removed++
			delete(tm.tasks, task.ID)
		}
	}
	tm.rootTasks = newRootTasks

	return removed
}
