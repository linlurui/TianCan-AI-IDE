package ralph

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/conversation"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/memdb"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// memdbPath returns the unified memory DB path. Overridable in tests so the
// rollback manager can run against a temp database instead of the real one.
var memdbPath = memdb.DefaultPath

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
	ID          string         `json:"id"`
	Name        string         `json:"name"`        // 可选名称
	Timestamp   time.Time      `json:"timestamp"`
	Description string         `json:"description"` // 描述此检查点
	Snapshots   []FileSnapshot `json:"snapshots"`    // 此检查点的快照
}

// ModifiedFileInfo 修改文件信息
type ModifiedFileInfo struct {
	Path           string    `json:"path"`
	Modified       bool      `json:"modified"`      // 是否有未保存的修改
	SnapshotCount  int       `json:"snapshotCount"` // 快照数量
	FirstSnapshot  time.Time `json:"firstSnapshot"` // 首次快照时间
	LatestSnapshot time.Time `json:"latestSnapshot"`
	Checkpoint     string    `json:"checkpoint"` // 所属检查点
}

// RollbackManager tracks file changes and supports reverting them. State now lives in
// the unified memdb, so snapshots and checkpoints survive an app restart (previously
// they were only in memory and were lost on quit — a problem for users without git).
type RollbackManager struct {
	mu                sync.Mutex
	db                *memdb.DB
	projectHash       string
	rootPath          string
	enabled           bool
	currentCheckpoint string
	loadedCP          bool
}

func NewRollbackManager(rootPath string) *RollbackManager {
	return &RollbackManager{
		rootPath:    rootPath,
		projectHash: conversation.ProjectHash(rootPath),
		enabled:     !envconfig.EnvBool("TIANCAN_DISABLE_ROLLBACK"),
	}
}

// dbh returns the (lazily opened, cached) unified store handle.
func (rm *RollbackManager) dbh() (*memdb.DB, error) {
	if rm.db != nil {
		return rm.db, nil
	}
	db, err := memdb.Open(memdbPath())
	if err != nil {
		return nil, err
	}
	if err := db.EnsureProject(rm.projectHash, rm.rootPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	rm.db = db
	return db, nil
}

func (rm *RollbackManager) abs(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(rm.rootPath, path)
}

// curCp returns the active checkpoint id, loading it from the store on first use.
func (rm *RollbackManager) curCp() string {
	if !rm.loadedCP {
		if db, err := rm.dbh(); err == nil {
			cp, _ := db.GetCurrentCheckpoint(rm.projectHash)
			rm.currentCheckpoint = cp
		}
		rm.loadedCP = true
	}
	return rm.currentCheckpoint
}

func (rm *RollbackManager) setCp(id string) {
	rm.currentCheckpoint = id
	rm.loadedCP = true
	if db, err := rm.dbh(); err == nil {
		_ = db.SetCurrentCheckpoint(rm.projectHash, id)
	}
}

// CreateCheckpoint 创建一个新的修改检查点
func (rm *RollbackManager) CreateCheckpoint(name string, description string) string {
	if !rm.enabled {
		return ""
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return ""
	}
	id := fmt.Sprintf("cp-%d", time.Now().UnixNano())
	if err := db.CreateCheckpoint(rm.projectHash, id, name, description); err != nil {
		return ""
	}
	rm.setCp(id)
	return id
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
	if !rm.enabled {
		return nil
	}
	db, err := rm.dbh()
	if err != nil {
		return err
	}
	absPath := rm.abs(path)
	content, existed := readFileContent(absPath)
	cp := rm.curCp()
	if _, err := db.SnapshotFile(rm.projectHash, cp, absPath, []byte(content), existed); err != nil {
		return err
	}
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
		_ = rm.snapshotUnlocked(p)
	}
}

// GetCheckpoints 返回所有检查点列表
func (rm *RollbackManager) GetCheckpoints() []*Checkpoint {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return nil
	}
	cpRows, err := db.GetCheckpoints(rm.projectHash)
	if err != nil {
		return nil
	}
	out := make([]*Checkpoint, 0, len(cpRows))
	for _, c := range cpRows {
		cp := &Checkpoint{
			ID:          c.ID,
			Name:        c.Name,
			Description: c.Desc,
			Timestamp:   time.UnixMilli(c.TS),
		}
		snaps, _ := db.SnapshotsForCheckpoint(rm.projectHash, c.ID)
		for _, s := range snaps {
			fs := FileSnapshot{
				Path:       s.Path,
				Exists:     s.Existed,
				Checkpoint: c.ID,
				ModTime:    time.UnixMilli(s.TS),
			}
			if s.Existed {
				if content, ok, _ := db.SnapshotContent(s); ok {
					fs.Content = content
				}
			}
			cp.Snapshots = append(cp.Snapshots, fs)
		}
		out = append(out, cp)
	}
	return out
}

// GetCheckpoint 获取指定检查点详情
func (rm *RollbackManager) GetCheckpoint(id string) (*Checkpoint, bool) {
	cps := rm.GetCheckpoints()
	for _, c := range cps {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

// GetModifiedFiles 返回当前已修改的文件列表
func (rm *RollbackManager) GetModifiedFiles() []ModifiedFileInfo {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.GetModifiedFilesUnlocked()
}

// GetModifiedFilesUnlocked 返回当前已修改的文件列表（内部使用，不加锁）
func (rm *RollbackManager) GetModifiedFilesUnlocked() []ModifiedFileInfo {
	db, err := rm.dbh()
	if err != nil {
		return nil
	}
	paths, err := db.DistinctSnapshotPaths(rm.projectHash)
	if err != nil {
		return nil
	}
	var files []ModifiedFileInfo
	for _, p := range paths {
		snaps, err := db.SnapshotsForPath(rm.projectHash, p)
		if err != nil || len(snaps) == 0 {
			continue
		}
		latest := snaps[len(snaps)-1]
		currentContent, currentExists := readFileContent(p)
		snapContent, snapExisted, _ := db.SnapshotContent(latest)
		modified := currentExists != snapExisted ||
			(currentExists && currentContent != snapContent)
		files = append(files, ModifiedFileInfo{
			Path:           p,
			Modified:       modified,
			SnapshotCount:  len(snaps),
			FirstSnapshot:  time.UnixMilli(snaps[0].TS),
			LatestSnapshot: time.UnixMilli(latest.TS),
			Checkpoint:     latest.CheckpointID,
		})
	}
	return files
}

// GetFileDiff 获取文件与指定快照的差异，返回 unified diff 格式
func (rm *RollbackManager) GetFileDiff(path string, snapshotIndex int) (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	absPath := rm.abs(path)
	db, err := rm.dbh()
	if err != nil {
		return "", err
	}
	snaps, err := db.SnapshotsForPath(rm.projectHash, absPath)
	if err != nil {
		return "", err
	}
	if len(snaps) == 0 {
		return "", fmt.Errorf("no snapshots for %s", path)
	}
	if snapshotIndex < 0 || snapshotIndex >= len(snaps) {
		return "", fmt.Errorf("snapshot index out of range: %d (available: 0-%d)", snapshotIndex, len(snaps)-1)
	}
	currentContent, currentExists := readFileContent(absPath)
	_ = currentExists
	snap := snaps[snapshotIndex]
	snapContent, _, _ := db.SnapshotContent(snap)
	snapTime := time.UnixMilli(snap.TS).Format("2006-01-02 15:04:05")
	return rm.generateUnifiedDiff(absPath, snapContent, currentContent, snapTime), nil
}

// GetFileDiffBetweenSnapshots 获取两个快照之间的差异
func (rm *RollbackManager) GetFileDiffBetweenSnapshots(path string, fromIndex, toIndex int) (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	absPath := rm.abs(path)
	db, err := rm.dbh()
	if err != nil {
		return "", err
	}
	snaps, err := db.SnapshotsForPath(rm.projectHash, absPath)
	if err != nil {
		return "", err
	}
	if len(snaps) == 0 {
		return "", fmt.Errorf("no snapshots for %s", path)
	}
	if fromIndex < 0 || fromIndex >= len(snaps) {
		return "", fmt.Errorf("fromIndex out of range: %d", fromIndex)
	}
	if toIndex < 0 || toIndex >= len(snaps) {
		return "", fmt.Errorf("toIndex out of range: %d", toIndex)
	}
	fromContent, _, _ := db.SnapshotContent(snaps[fromIndex])
	toContent, _, _ := db.SnapshotContent(snaps[toIndex])
	return rm.generateUnifiedDiff(
		absPath, fromContent, toContent,
		fmt.Sprintf("%s → %s", time.UnixMilli(snaps[fromIndex].TS).Format("15:04:05"), time.UnixMilli(snaps[toIndex].TS).Format("15:04:05")),
	), nil
}

// GetAllDiffs 获取所有已修改文件的差异
func (rm *RollbackManager) GetAllDiffs() (string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return "", err
	}
	paths, err := db.DistinctSnapshotPaths(rm.projectHash)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "没有已修改的文件", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== 所有文件差异 (共 %d 个文件) ===\n\n", len(paths)))
	for _, p := range paths {
		snaps, _ := db.SnapshotsForPath(rm.projectHash, p)
		if len(snaps) == 0 {
			continue
		}
		currentContent, _ := readFileContent(p)
		latest := snaps[len(snaps)-1]
		snapContent, _, _ := db.SnapshotContent(latest)
		sb.WriteString(fmt.Sprintf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"))
		sb.WriteString(fmt.Sprintf("📄 %s\n", p))
		sb.WriteString(fmt.Sprintf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"))
		sb.WriteString(rm.generateUnifiedDiff(p, snapContent, currentContent, time.UnixMilli(latest.TS).Format("15:04:05")))
		sb.WriteString("\n\n")
	}
	return sb.String(), nil
}

// Rollback 回滚到上一次快照
func (rm *RollbackManager) Rollback() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.rollbackUnlocked()
}

func (rm *RollbackManager) rollbackUnlocked() error {
	db, err := rm.dbh()
	if err != nil {
		return err
	}
	paths, err := db.DistinctSnapshotPaths(rm.projectHash)
	if err != nil {
		return err
	}
	var errs []string
	for _, p := range paths {
		snaps, _ := db.SnapshotsForPath(rm.projectHash, p)
		if len(snaps) == 0 {
			continue
		}
		latest := snaps[len(snaps)-1]
		if err := db.RestoreSnapshot(latest); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", p, err))
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
	db, err := rm.dbh()
	if err != nil {
		return err
	}
	return db.RollbackToCheckpoint(rm.projectHash, checkpointID)
}

// RollbackFileToSnapshot 回滚单个文件到指定快照
func (rm *RollbackManager) RollbackFileToSnapshot(path string, snapshotIndex int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	absPath := rm.abs(path)
	db, err := rm.dbh()
	if err != nil {
		return err
	}
	snaps, err := db.SnapshotsForPath(rm.projectHash, absPath)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return fmt.Errorf("no snapshots for %s", path)
	}
	if snapshotIndex < 0 || snapshotIndex >= len(snaps) {
		return fmt.Errorf("snapshot index out of range: %d", snapshotIndex)
	}
	return db.RestoreSnapshot(snaps[snapshotIndex])
}

// RollbackToIndex 回滚到指定快照索引（全局）
func (rm *RollbackManager) RollbackToIndex(snapshotIndex int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return err
	}
	paths, err := db.DistinctSnapshotPaths(rm.projectHash)
	if err != nil {
		return err
	}
	var errs []string
	for _, p := range paths {
		snaps, _ := db.SnapshotsForPath(rm.projectHash, p)
		if snapshotIndex < 0 || snapshotIndex >= len(snaps) {
			continue
		}
		if err := db.RestoreSnapshot(snaps[snapshotIndex]); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", p, err))
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
	db, err := rm.dbh()
	if err != nil {
		return
	}
	_ = db.DeleteAllSnapshots(rm.projectHash)
}

// ClearCheckpoints 清除所有检查点
func (rm *RollbackManager) ClearCheckpoints() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return
	}
	_ = db.DeleteAllCheckpoints(rm.projectHash)
	rm.currentCheckpoint = ""
	rm.loadedCP = true
}

// DeleteCheckpoint 删除指定检查点
func (rm *RollbackManager) DeleteCheckpoint(checkpointID string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return err
	}
	return db.DeleteCheckpoint(rm.projectHash, checkpointID)
}

// GetSnapshotCount 获取快照数量
func (rm *RollbackManager) GetSnapshotCount() int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	db, err := rm.dbh()
	if err != nil {
		return 0
	}
	paths, err := db.DistinctSnapshotPaths(rm.projectHash)
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range paths {
		snaps, err := db.SnapshotsForPath(rm.projectHash, p)
		if err == nil {
			n += len(snaps)
		}
	}
	return n
}

// --- diff helpers (pure, unchanged from the in-memory implementation) --------

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

// lcsItem for diff computation
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

// readFileContent returns the file content and whether the file currently exists.
func readFileContent(path string) (content string, existed bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}
