package ralph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// FileSnapshot captures file state for rollback.
type FileSnapshot struct {
	Path       string    `json:"path"`
	Content    string    `json:"content"`
	Exists     bool      `json:"exists"`
	ModTime    time.Time `json:"modTime"`
	Checkpoint string    `json:"checkpoint"` // 所属检查点ID
}

// Checkpoint 标记一个修改时间点
type Checkpoint struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`         // 可选名称
	Timestamp   time.Time       `json:"timestamp"`
	Description string          `json:"description"`  // 描述此检查点
	Snapshots   []FileSnapshot  `json:"snapshots"`   // 此检查点的快照
}

// RollbackManager tracks file changes and supports reverting them.
type RollbackManager struct {
	mu           sync.Mutex
	snapshots    map[string][]FileSnapshot    // path -> snapshots stack
	checkpoints  map[string]*Checkpoint       // checkpoint ID -> checkpoint
	checkpointOrder []string                  // 检查点顺序（用于列表显示）
	rootPath     string
	enabled      bool
	currentCheckpoint string                  // 当前活跃检查点
}

func NewRollbackManager(rootPath string) *RollbackManager {
	return &RollbackManager{
		snapshots:   make(map[string][]FileSnapshot),
		checkpoints: make(map[string]*Checkpoint),
		rootPath:    rootPath,
		enabled:    os.Getenv("TIANCAN_DISABLE_ROLLBACK") != "1",
	}
}

// CreateCheckpoint 创建一个新的修改检查点
func (rm *RollbackManager) CreateCheckpoint(name string, description string) string {
	if !rm.enabled {
		return ""
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()

	cp := &Checkpoint{
		ID:          fmt.Sprintf("cp-%d", time.Now().UnixNano()),
		Name:        name,
		Timestamp:   time.Now(),
		Description: description,
		Snapshots:   make([]FileSnapshot, 0),
	}

	rm.checkpoints[cp.ID] = cp
	rm.checkpointOrder = append(rm.checkpointOrder, cp.ID)
	rm.currentCheckpoint = cp.ID

	// 将当前所有快照复制到检查点
	for _, snaps := range rm.snapshots {
		for i := range snaps {
			snaps[i].Checkpoint = cp.ID
			cp.Snapshots = append(cp.Snapshots, snaps[i])
		}
	}

	return cp.ID
}

// Snapshot 记录文件的当前状态
func (rm *RollbackManager) Snapshot(path string) error {
	if !rm.enabled {
		return nil
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.snapshotUnlocked(path)
}

func (rm *RollbackManager) snapshotUnlocked(path string) error {
	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(rm.rootPath, path)
	}
	snap := FileSnapshot{Path: absPath, ModTime: time.Now(), Checkpoint: rm.currentCheckpoint}
	data, err := os.ReadFile(absPath)
	if err != nil {
		snap.Exists = false
	} else {
		snap.Exists = true
		snap.Content = string(data)
	}
	rm.snapshots[absPath] = append(rm.snapshots[absPath], snap)
	return nil
}

// SnapshotMultiple 批量快照多个文件
func (rm *RollbackManager) SnapshotMultiple(paths []string) {
	if !rm.enabled {
		return
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for _, p := range paths {
		rm.snapshotUnlocked(p)
	}
}

// GetCheckpoints 返回所有检查点列表
func (rm *RollbackManager) GetCheckpoints() []*Checkpoint {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	result := make([]*Checkpoint, 0, len(rm.checkpointOrder))
	for _, id := range rm.checkpointOrder {
		if cp, ok := rm.checkpoints[id]; ok {
			result = append(result, cp)
		}
	}
	return result
}

// GetCheckpoint 获取指定检查点详情
func (rm *RollbackManager) GetCheckpoint(id string) (*Checkpoint, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	cp, ok := rm.checkpoints[id]
	return cp, ok
}

// GetModifiedFiles 返回当前已修改的文件列表
func (rm *RollbackManager) GetModifiedFiles() []ModifiedFileInfo {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	var files []ModifiedFileInfo
	for path, snaps := range rm.snapshots {
		if len(snaps) == 0 {
			continue
		}
		latest := snaps[len(snaps)-1]

		// 检查文件是否与最新快照不同
		currentExists := true
		currentContent := ""
		if data, err := os.ReadFile(path); err == nil {
			currentContent = string(data)
		} else {
			currentExists = false
		}

		modified := currentExists != latest.Exists ||
			(currentExists && currentContent != latest.Content)

		files = append(files, ModifiedFileInfo{
			Path:           path,
			Modified:       modified,
			SnapshotCount:  len(snaps),
			FirstSnapshot:  snaps[0].ModTime,
			LatestSnapshot: latest.ModTime,
			Checkpoint:     latest.Checkpoint,
		})
	}
	return files
}

// ModifiedFileInfo 修改文件信息
type ModifiedFileInfo struct {
	Path           string    `json:"path"`
	Modified       bool      `json:"modified"`       // 是否有未保存的修改
	SnapshotCount  int       `json:"snapshotCount"`  // 快照数量
	FirstSnapshot  time.Time `json:"firstSnapshot"`  // 首次快照时间
	LatestSnapshot time.Time `json:"latestSnapshot"` // 最新快照时间
	Checkpoint     string    `json:"checkpoint"`    // 所属检查点
}

// GetFileDiff 获取文件与指定快照的差异，返回 unified diff 格式
func (rm *RollbackManager) GetFileDiff(path string, snapshotIndex int) (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(rm.rootPath, path)
	}

	snaps, ok := rm.snapshots[absPath]
	if !ok || len(snaps) == 0 {
		return "", fmt.Errorf("no snapshots for %s", path)
	}

	if snapshotIndex < 0 || snapshotIndex >= len(snaps) {
		return "", fmt.Errorf("snapshot index out of range: %d (available: 0-%d)", snapshotIndex, len(snaps)-1)
	}

	// 获取当前文件内容
	currentContent := ""
	if data, err := os.ReadFile(absPath); err == nil {
		currentContent = string(data)
	}

	snapshot := snaps[snapshotIndex]
	snapshotTime := snapshot.ModTime.Format("2006-01-02 15:04:05")

	// 生成 unified diff
	diff := rm.generateUnifiedDiff(path, snapshot.Content, currentContent, snapshotTime)

	return diff, nil
}

// GetFileDiffBetweenSnapshots 获取两个快照之间的差异
func (rm *RollbackManager) GetFileDiffBetweenSnapshots(path string, fromIndex, toIndex int) (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(rm.rootPath, path)
	}

	snaps, ok := rm.snapshots[absPath]
	if !ok || len(snaps) == 0 {
		return "", fmt.Errorf("no snapshots for %s", path)
	}

	if fromIndex < 0 || fromIndex >= len(snaps) {
		return "", fmt.Errorf("fromIndex out of range: %d", fromIndex)
	}
	if toIndex < 0 || toIndex >= len(snaps) {
		return "", fmt.Errorf("toIndex out of range: %d", toIndex)
	}

	fromSnap := snaps[fromIndex]
	toSnap := snaps[toIndex]

	return rm.generateUnifiedDiff(
		path,
		fromSnap.Content,
		toSnap.Content,
		fmt.Sprintf("%s → %s", fromSnap.ModTime.Format("15:04:05"), toSnap.ModTime.Format("15:04:05")),
	), nil
}

// GetAllDiffs 获取所有已修改文件的差异
func (rm *RollbackManager) GetAllDiffs() (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	var result strings.Builder
	files := rm.GetModifiedFilesUnlocked()

	if len(files) == 0 {
		return "没有已修改的文件", nil
	}

	result.WriteString(fmt.Sprintf("=== 所有文件差异 (共 %d 个文件) ===\n\n", len(files)))

	for _, f := range files {
		snaps := rm.snapshots[f.Path]
		if len(snaps) == 0 {
			continue
		}

		currentContent := ""
		if data, err := os.ReadFile(f.Path); err == nil {
			currentContent = string(data)
		}

		snapshot := snaps[len(snaps)-1] // 最新快照
		diff := rm.generateUnifiedDiff(f.Path, snapshot.Content, currentContent, snapshot.ModTime.Format("15:04:05"))

		result.WriteString(fmt.Sprintf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"))
		result.WriteString(fmt.Sprintf("📄 %s\n", f.Path))
		result.WriteString(fmt.Sprintf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"))
		result.WriteString(diff)
		result.WriteString("\n\n")
	}

	return result.String(), nil
}

// GetModifiedFilesUnlocked 返回当前已修改的文件列表（内部使用，不加锁）
func (rm *RollbackManager) GetModifiedFilesUnlocked() []ModifiedFileInfo {
	var files []ModifiedFileInfo
	for path, snaps := range rm.snapshots {
		if len(snaps) == 0 {
			continue
		}
		latest := snaps[len(snaps)-1]

		currentExists := true
		currentContent := ""
		if data, err := os.ReadFile(path); err == nil {
			currentContent = string(data)
		} else {
			currentExists = false
		}

		modified := currentExists != latest.Exists ||
			(currentExists && currentContent != latest.Content)

		files = append(files, ModifiedFileInfo{
			Path:           path,
			Modified:       modified,
			SnapshotCount:  len(snaps),
			FirstSnapshot:  snaps[0].ModTime,
			LatestSnapshot: latest.ModTime,
			Checkpoint:     latest.Checkpoint,
		})
	}
	return files
}

// generateUnifiedDiff 生成 unified diff 格式的差异
func (rm *RollbackManager) generateUnifiedDiff(path, oldContent, newContent, timeLabel string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var diff strings.Builder
	diff.WriteString(fmt.Sprintf("--- a/%s\t%s\n", path, timeLabel))
	diff.WriteString(fmt.Sprintf("+++ b/%s\t(current)\n", path))

	// 简单的行比较算法
	lcs := rm.computeLCS(oldLines, newLines)

	i, j, k := 0, 0, 0
	hunkStart := -1

	addLine := func(line string) {
		if hunkStart == -1 {
			hunkStart = j
		}
		diff.WriteString(fmt.Sprintf("+%s\n", line))
		j++
	}

	delLine := func(line string) {
		if hunkStart == -1 {
			hunkStart = i
		}
		diff.WriteString(fmt.Sprintf("-%s\n", line))
		i++
	}

	contextLine := func(line string, isOld bool) {
		if hunkStart == -1 {
			hunkStart = i
		}
		if isOld {
			i++
		} else {
			j++
		}
		diff.WriteString(fmt.Sprintf(" %s\n", line))
	}

	flushHunk := func() {
		if hunkStart == -1 {
			return
		}
		// 输出 hunk header
		diff.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", i-(j-hunkStart), j-hunkStart, j-(j-hunkStart), j-hunkStart))
		hunkStart = -1
	}

	for k < len(lcs) {
		lcsItem := lcs[k]

		// 输出删除行（old 中有，new 中没有）
		for i < lcsItem.OldIdx {
			delLine(oldLines[i])
		}
		// 输出添加行（new 中有，old 中没有）
		for j < lcsItem.NewIdx {
			addLine(newLines[j])
		}
		// 输出共同行
		if i < lcsItem.OldIdx {
			contextLine(oldLines[i], true)
		}
		if j < lcsItem.NewIdx {
			contextLine(newLines[j], false)
		}
		k++
	}

	// 处理剩余行
	for i < len(oldLines) {
		delLine(oldLines[i])
	}
	for j < len(newLines) {
		addLine(newLines[j])
	}

	flushHunk()

	if diff.Len() == 0 {
		diff.WriteString(" (文件内容相同)\n")
	}

	return diff.String()
}

// LCS item for diff computation
type lcsItem struct {
	OldIdx int
	NewIdx int
}

// computeLCS 计算最长公共子序列
func (rm *RollbackManager) computeLCS(old, new []string) []lcsItem {
	m, n := len(old), len(new)
	// 简化版本：只返回匹配的索引对
	var result []lcsItem

	// 使用简单的动态规划找 LCS
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if old[i-1] == new[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				if dp[i-1][j] > dp[i][j-1] {
					dp[i][j] = dp[i-1][j]
				} else {
					dp[i][j] = dp[i][j-1]
				}
			}
		}
	}

	// 回溯找 LCS
	i, j := m, n
	var pairs []lcsItem
	for i > 0 && j > 0 {
		if old[i-1] == new[j-1] {
			pairs = append(pairs, lcsItem{OldIdx: i - 1, NewIdx: j - 1})
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
			i--
		} else {
			j--
		}
	}

	// 反转结果
	for i := len(pairs) - 1; i >= 0; i-- {
		result = append(result, pairs[i])
	}

	return result
}

// Rollback 回滚到上一次快照
func (rm *RollbackManager) Rollback() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.rollbackUnlocked()
}

func (rm *RollbackManager) rollbackUnlocked() error {
	var errs []string
	for path, stack := range rm.snapshots {
		if len(stack) == 0 {
			continue
		}
		snap := stack[len(stack)-1]
		rm.snapshots[path] = stack[:len(stack)-1]
		if !snap.Exists {
			os.Remove(path)
		} else {
			os.MkdirAll(filepath.Dir(path), 0755)
			if err := os.WriteFile(path, []byte(snap.Content), 0644); err != nil {
				errs = append(errs, fmt.Sprintf("write %s: %v", path, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RollbackToCheckpoint 回滚到指定检查点
func (rm *RollbackManager) RollbackToCheckpoint(checkpointID string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	cp, ok := rm.checkpoints[checkpointID]
	if !ok {
		return fmt.Errorf("checkpoint not found: %s", checkpointID)
	}

	// 获取检查点创建时的快照状态
	var errs []string

	// 首先恢复所有文件到检查点创建时的状态
	// 我们需要找到每个文件在检查点时的快照
	for _, snap := range cp.Snapshots {
		if snap.Checkpoint != checkpointID {
			continue
		}

		if !snap.Exists {
			os.Remove(snap.Path)
		} else {
			os.MkdirAll(filepath.Dir(snap.Path), 0755)
			if err := os.WriteFile(snap.Path, []byte(snap.Content), 0644); err != nil {
				errs = append(errs, fmt.Sprintf("write %s: %v", snap.Path, err))
			}
		}
	}

	// 清理检查点之后的快照
	for path := range rm.snapshots {
		// 保留检查点创建之后的快照，但移除检查点时的快照
		newSnaps := make([]FileSnapshot, 0)
		for _, snap := range rm.snapshots[path] {
			if snap.Checkpoint != checkpointID {
				newSnaps = append(newSnaps, snap)
			}
		}
		rm.snapshots[path] = newSnaps
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RollbackFileToSnapshot 回滚单个文件到指定快照
func (rm *RollbackManager) RollbackFileToSnapshot(path string, snapshotIndex int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(rm.rootPath, path)
	}

	snaps, ok := rm.snapshots[absPath]
	if !ok || len(snaps) == 0 {
		return fmt.Errorf("no snapshots for %s", path)
	}

	if snapshotIndex < 0 || snapshotIndex >= len(snaps) {
		return fmt.Errorf("snapshot index out of range: %d", snapshotIndex)
	}

	snap := snaps[snapshotIndex]
	if !snap.Exists {
		return os.Remove(absPath)
	}

	os.MkdirAll(filepath.Dir(absPath), 0755)
	return os.WriteFile(absPath, []byte(snap.Content), 0644)
}

// RollbackToIndex 回滚到指定快照索引（全局）
func (rm *RollbackManager) RollbackToIndex(snapshotIndex int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// 找到所有文件的第 snapshotIndex 个快照并恢复
	var errs []string
	for path, snaps := range rm.snapshots {
		if snapshotIndex >= len(snaps) {
			continue
		}
		snap := snaps[snapshotIndex]
		if !snap.Exists {
			os.Remove(path)
		} else {
			os.MkdirAll(filepath.Dir(path), 0755)
			if err := os.WriteFile(path, []byte(snap.Content), 0644); err != nil {
				errs = append(errs, fmt.Sprintf("write %s: %v", path, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ClearSnapshots 清除所有快照
func (rm *RollbackManager) ClearSnapshots() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.snapshots = make(map[string][]FileSnapshot)
	rm.currentCheckpoint = ""
}

// ClearCheckpoints 清除所有检查点
func (rm *RollbackManager) ClearCheckpoints() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.checkpoints = make(map[string]*Checkpoint)
	rm.checkpointOrder = nil
	rm.currentCheckpoint = ""
}

// DeleteCheckpoint 删除指定检查点
func (rm *RollbackManager) DeleteCheckpoint(checkpointID string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if _, ok := rm.checkpoints[checkpointID]; !ok {
		return fmt.Errorf("checkpoint not found: %s", checkpointID)
	}

	delete(rm.checkpoints, checkpointID)

	// 从顺序列表中移除
	newOrder := make([]string, 0)
	for _, id := range rm.checkpointOrder {
		if id != checkpointID {
			newOrder = append(newOrder, id)
		}
	}
	rm.checkpointOrder = newOrder

	return nil
}

// GetSnapshotCount 获取快照数量
func (rm *RollbackManager) GetSnapshotCount() int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	count := 0
	for _, snaps := range rm.snapshots {
		count += len(snaps)
	}
	return count
}

// RalphConfig holds Ralph Loop V2 configuration.
type RalphConfig struct {
	MaxIterations      int  `json:"maxIterations"`
	EnableRollback     bool `json:"enableRollback"`
	EnableVerification bool `json:"enableVerification"`
	AutoRollbackOnFail bool `json:"autoRollbackOnFail"`
}

func DefaultRalphConfig() RalphConfig {
	return RalphConfig{
		MaxIterations: 3, EnableRollback: true,
		EnableVerification: true, AutoRollbackOnFail: false,
	}
}

// RalphLoopV2 is an enhanced self-correction loop with rollback support.
type RalphLoopV2 struct {
	chatFn   ChatFunc
	toolExec ToolExecFunc
	config   RalphConfig
	rollback *RollbackManager
	rootPath string
}

// NewRalphLoopV2 creates an enhanced Ralph Loop with rollback.
func NewRalphLoopV2(chatFn ChatFunc, toolExec ToolExecFunc, rootPath string, cfg RalphConfig) *RalphLoopV2 {
	return &RalphLoopV2{
		chatFn:   chatFn,
		toolExec: toolExec,
		config:   cfg,
		rollback: NewRollbackManager(rootPath),
		rootPath: rootPath,
	}
}

// Run executes the enhanced Ralph Loop: implement → verify → fix with rollback.
func (r *RalphLoopV2) Run(ctx context.Context, task string, agentDef types.AgentDefinition) (*types.RalphLoopResult, error) {
	var allMessages []types.Message
	iterations := 0
	fixesApplied := 0
	verdict := types.VerdictFail

	for iterations < r.config.MaxIterations {
		iterations++
		select {
		case <-ctx.Done():
			return &types.RalphLoopResult{
				Verdict: types.VerdictPartial, Iterations: iterations,
				FixesApplied: fixesApplied, Messages: allMessages,
			}, ctx.Err()
		default:
		}

		if r.config.EnableRollback {
			r.rollback.ClearSnapshots()
		}

		// Phase 1: Implement or Fix
		prompt := task
		if iterations > 1 {
			prompt = fmt.Sprintf("Fix issues:\n%s\n\nOriginal: %s",
				lastVerificationReport(allMessages), task)
		}

		implMsgs, err := r.runAgentTurn(ctx, prompt, agentDef)
		if err != nil {
			if r.config.AutoRollbackOnFail {
				r.rollback.Rollback()
			}
			return &types.RalphLoopResult{
				Verdict: types.VerdictFail, Iterations: iterations,
				FixesApplied: fixesApplied, Report: fmt.Sprintf("Error: %v", err),
				Messages: allMessages,
			}, err
		}
		allMessages = append(allMessages, implMsgs...)
		if iterations > 1 {
			fixesApplied++
		}

		// Phase 2: Verify
		if !r.config.EnableVerification {
			verdict = types.VerdictPass
			break
		}

		verifyMsgs, err := r.runVerifyTurn(ctx, fmt.Sprintf("Verify: %s\n%s",
			task, summarizeActions(implMsgs)))
		if err != nil {
			allMessages = append(allMessages, types.Message{
				Role: types.RoleSystem, Content: fmt.Sprintf("Verify error: %v", err),
				Timestamp: time.Now(),
			})
			continue
		}
		allMessages = append(allMessages, verifyMsgs...)

		// Phase 3: Check verdict
		verdict = extractVerdict(verifyMsgs)
		if verdict == types.VerdictPass {
			break
		}
		if verdict == types.VerdictFail && r.config.AutoRollbackOnFail {
			r.rollback.Rollback()
		}
	}

	return &types.RalphLoopResult{
		Verdict: verdict, Iterations: iterations, FixesApplied: fixesApplied,
		Report:   buildReport(allMessages, verdict, iterations, fixesApplied),
		Messages: allMessages,
	}, nil
}

// runAgentTurn runs a single agent turn using the existing RalphLoop helpers.
func (r *RalphLoopV2) runAgentTurn(ctx context.Context, prompt string, agentDef types.AgentDefinition) ([]types.Message, error) {
	var messages []types.Message
	messages = append(messages, types.Message{
		Role: types.RoleUser, Content: prompt, Timestamp: time.Now(),
	})
	response, err := r.chatFn(agentDef.SystemPrompt, messages)
	if err != nil {
		return messages, err
	}
	assistantMsg := types.Message{Role: types.RoleAssistant, Content: response, Timestamp: time.Now()}
	messages = append(messages, assistantMsg)

	// Parse and execute tool calls
	toolCalls := ParseToolCallsEnhanced(response)
	for _, tc := range toolCalls {
		result, err := r.toolExec(ctx, tc.Name, tc.Args)
		if err != nil {
			result = types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}
		messages = append(messages, types.Message{
			Role: types.RoleTool, Content: result.Content,
			ToolName: tc.Name, ToolUseID: tc.Name, Timestamp: time.Now(),
		})
	}
	return messages, nil
}

// runVerifyTurn runs a verification turn.
func (r *RalphLoopV2) runVerifyTurn(ctx context.Context, prompt string) ([]types.Message, error) {
	verifyDef := types.AgentDefinition{
		AgentType:       types.RoleVerifier,
		SystemPrompt:    verifierSystemPrompt,
		MaxTurns:        15,
		DisallowedTools: []string{"file_write", "file_edit"},
		PermissionMode:  "auto",
	}
	return r.runAgentTurn(ctx, prompt, verifyDef)
}
