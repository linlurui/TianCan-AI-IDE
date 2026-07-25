package agent

import (
	"github.com/rocky233/tiancan-ai-ide/backend/agent/ralph"
)

// TaskBindings provides Wails bindings for the task management system.
type TaskBindings struct{}

// NewTaskBindings creates a new TaskBindings instance for Wails service injection.
func NewTaskBindings() *TaskBindings {
	return &TaskBindings{}
}

// GetAgentTasks returns all agent tasks for the frontend.
func (tb *TaskBindings) GetAgentTasks() []map[string]interface{} {
	tm := ralph.GetTaskManager()
	return tm.GetTasksForFrontend()
}

// GetTask returns a single task by ID.
func (tb *TaskBindings) GetTask(taskID string) *ralph.AgentTask {
	tm := ralph.GetTaskManager()
	return tm.GetTask(taskID)
}

// ApplyTask applies all changes from a task.
func (tb *TaskBindings) ApplyTask(taskID string) (string, error) {
	tm := ralph.GetTaskManager()
	return tm.ApplyTask(taskID)
}

// RejectTask rejects all changes from a task.
func (tb *TaskBindings) RejectTask(taskID string) (string, error) {
	tm := ralph.GetTaskManager()
	return tm.RejectTask(taskID)
}

// ApplyFile applies a single file from a task.
func (tb *TaskBindings) ApplyFile(taskID string, filePath string) (string, error) {
	tm := ralph.GetTaskManager()
	return tm.ApplyFile(taskID, filePath)
}

// RejectFile rejects a single file from a task.
func (tb *TaskBindings) RejectFile(taskID string, filePath string) (string, error) {
	tm := ralph.GetTaskManager()
	return tm.RejectFile(taskID, filePath)
}

// GetFileOriginalContent returns the original content of a file for diff viewing.
func (tb *TaskBindings) GetFileOriginalContent(taskID string, filePath string) (string, error) {
	tm := ralph.GetTaskManager()
	return tm.GetFileOriginalContent(taskID, filePath)
}

// CleanupOldTasks removes tasks older than 24 hours.
func (tb *TaskBindings) CleanupOldTasks() int {
	tm := ralph.GetTaskManager()
	return tm.CleanupOldTasks(24 * 60 * 60 * 1000000000) // 24 hours in nanoseconds
}
