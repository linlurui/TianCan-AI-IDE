package agent

import (
	"sync"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/ralph"
)

// RollbackBindings 提供给前端的 Rollback API 绑定
type RollbackBindings struct {
	mu      sync.RWMutex
	manager *ralph.RollbackManager
	rootPath string
}

// NewRollbackBindings 创建新的 Rollback 绑定
func NewRollbackBindings(rootPath string) *RollbackBindings {
	return &RollbackBindings{
		manager:  ralph.NewRollbackManager(rootPath),
		rootPath: rootPath,
	}
}

// SetRootPath 更新 RollbackBindings 的根路径
func (rb *RollbackBindings) SetRootPath(rootPath string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.rootPath = rootPath
	// Re-create manager with new path
	rb.manager = ralph.NewRollbackManager(rootPath)
}

// GetRootPath 获取当前根路径
func (rb *RollbackBindings) GetRootPath() string {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.rootPath
}

// GetManager 返回底层的 RollbackManager
func (rb *RollbackBindings) GetManager() *ralph.RollbackManager {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.manager
}

// ─── Checkpoint APIs ─────────────────────────────────────────────

// CreateCheckpoint 创建检查点
func (rb *RollbackBindings) CreateCheckpoint(name string, description string) string {
	return rb.manager.CreateCheckpoint(name, description)
}

// GetCheckpoints 返回所有检查点
func (rb *RollbackBindings) GetCheckpoints() []*ralph.Checkpoint {
	return rb.manager.GetCheckpoints()
}

// DeleteCheckpoint 删除检查点
func (rb *RollbackBindings) DeleteCheckpoint(id string) error {
	return rb.manager.DeleteCheckpoint(id)
}

// ─── Modified Files APIs ────────────────────────────────────────

// GetModifiedFiles 返回已修改的文件列表
func (rb *RollbackBindings) GetModifiedFiles() []ralph.ModifiedFileInfo {
	return rb.manager.GetModifiedFiles()
}

// ─── Rollback APIs ──────────────────────────────────────────────

// RollbackAll 回滚所有文件
func (rb *RollbackBindings) RollbackAll() error {
	return rb.manager.Rollback()
}

// RollbackFileToSnapshot 回滚单个文件到指定快照
func (rb *RollbackBindings) RollbackFileToSnapshot(path string, snapshotIndex int) error {
	return rb.manager.RollbackFileToSnapshot(path, snapshotIndex)
}

// RollbackToCheckpoint 回滚到指定检查点
func (rb *RollbackBindings) RollbackToCheckpoint(checkpointID string) error {
	return rb.manager.RollbackToCheckpoint(checkpointID)
}

// ─── Diff APIs ──────────────────────────────────────────────────

// GetFileDiff 获取文件差异
func (rb *RollbackBindings) GetFileDiff(path string, snapshotIndex int) (string, error) {
	return rb.manager.GetFileDiff(path, snapshotIndex)
}

// GetFileSnapshotContent 获取指定快照的内容
func (rb *RollbackBindings) GetFileSnapshotContent(path string, snapshotIndex int) (string, error) {
	return rb.manager.GetFileDiff(path, snapshotIndex)
}

// ─── Snapshot APIs ──────────────────────────────────────────────

// CreateSnapshot 手动创建快照
func (rb *RollbackBindings) CreateSnapshot(path string) error {
	return rb.manager.Snapshot(path)
}

// GetSnapshotCount 获取快照数量
func (rb *RollbackBindings) GetSnapshotCount() int {
	return rb.manager.GetSnapshotCount()
}

// ClearSnapshots 清除所有快照
func (rb *RollbackBindings) ClearSnapshots() {
	rb.manager.ClearSnapshots()
}
