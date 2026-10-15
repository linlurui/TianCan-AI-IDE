package tools

import (
	"context"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/ralph"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// ─── Rollback Tools ────────────────────────────────────────────────────

// RollbackTool provides file rollback capabilities to the AI agent.
type RollbackTool struct {
	rollbackMgr *ralph.RollbackManager
}

// NewRollbackTool creates a new RollbackTool.
func NewRollbackTool(rm *ralph.RollbackManager) *RollbackTool {
	return &RollbackTool{rollbackMgr: rm}
}

// SetRollbackManager sets the rollback manager.
func (t *RollbackTool) SetRollbackManager(rm *ralph.RollbackManager) {
	t.rollbackMgr = rm
}

func (t *RollbackTool) Name() string    { return "rollback" }
func (t *RollbackTool) IsReadOnly() bool { return false }

func (t *RollbackTool) IsConcurrencySafe() bool { return false }

func (t *RollbackTool) Description() string {
	return `文件回滚工具：创建检查点、查看修改列表、比对差异、回滚到指定版本。
用法:
- 创建检查点: checkpoint_name="功能A开发"
- 查看修改: action="list_modified"
- 查看差异: action="diff", path="文件路径", snapshot=1
- 回滚单个文件: action="rollback_file", path="文件路径", snapshot=0
- 回滚所有文件: action="rollback_all"
- 回滚到检查点: action="rollback_checkpoint", checkpoint_id="cp-xxx"`
}

func (t *RollbackTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "操作类型: create_checkpoint, list_modified, diff, rollback_file, rollback_all, rollback_checkpoint, list_checkpoints",
			},
			"checkpoint_name": map[string]interface{}{
				"type":        "string",
				"description": "检查点名称（用于 create_checkpoint）",
			},
			"checkpoint_id": map[string]interface{}{
				"type":        "string",
				"description": "检查点ID（用于 rollback_checkpoint）",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "文件路径（用于 diff, rollback_file）",
			},
			"snapshot": map[string]interface{}{
				"type":        "number",
				"description": "快照索引（用于 diff, rollback_file），0=最早",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "检查点描述（可选）",
			},
		},
		"required": []string{"action"},
	}
}

func (t *RollbackTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	action, _ := args["action"].(string)
	rm := t.rollbackMgr
	if rm == nil {
		return types.ToolResult{Success: false, Error: "RollbackManager not available"}, nil
	}

	switch action {
	case "create_checkpoint":
		name := getArgString(args, "checkpoint_name", "")
		desc := getArgString(args, "description", "")
		cpID := rm.CreateCheckpoint(name, desc)
		if cpID == "" {
			return types.ToolResult{Success: false, Error: "Failed to create checkpoint (rollback may be disabled)"}, nil
		}
		return types.ToolResult{Content: "✅ 检查点已创建: " + cpID + "\n名称: " + name + "\n描述: " + desc, Success: true}, nil

	case "list_checkpoints":
		cps := rm.GetCheckpoints()
		if len(cps) == 0 {
			return types.ToolResult{Content: "暂无检查点", Success: true}, nil
		}
		content := "📍 检查点列表:\n\n"
		for _, cp := range cps {
			content += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"
			content += "ID: " + cp.ID + "\n"
			content += "名称: " + cp.Name + "\n"
			content += "时间: " + cp.Timestamp.Format("2006-01-02 15:04:05") + "\n"
			content += "描述: " + cp.Description + "\n"
			content += "文件数: " + string(rune('0'+len(cp.Snapshots))) + "\n"
		}
		return types.ToolResult{Content: content, Success: true}, nil

	case "list_modified":
		files := rm.GetModifiedFiles()
		if len(files) == 0 {
			return types.ToolResult{Content: "没有修改的文件", Success: true}, nil
		}
		content := "📝 已修改文件列表:\n\n"
		content += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"
		for _, f := range files {
			status := "✅ 无变化"
			if f.Modified {
				status = "🔴 已修改"
			}
			content += f.Path + "\n"
			content += "  状态: " + status + "\n"
			content += "  快照数: " + string(rune('0'+f.SnapshotCount%10)) + "\n"
			content += "  首次快照: " + f.FirstSnapshot.Format("15:04:05") + "\n"
			if f.Checkpoint != "" {
				content += "  检查点: " + f.Checkpoint + "\n"
			}
			content += "\n"
		}
		return types.ToolResult{Content: content, Success: true}, nil

	case "diff":
		path := getArgString(args, "path", "")
		snapshot := 0
		if v, ok := args["snapshot"].(float64); ok {
			snapshot = int(v)
		}
		if path == "" {
			return types.ToolResult{Success: false, Error: "path is required for diff"}, nil
		}
		diff, err := rm.GetFileDiff(path, snapshot)
		if err != nil {
			return types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return types.ToolResult{Content: diff, Success: true}, nil

	case "rollback_file":
		path := getArgString(args, "path", "")
		snapshot := 0
		if v, ok := args["snapshot"].(float64); ok {
			snapshot = int(v)
		}
		if path == "" {
			return types.ToolResult{Success: false, Error: "path is required for rollback_file"}, nil
		}
		if err := rm.RollbackFileToSnapshot(path, snapshot); err != nil {
			return types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return types.ToolResult{Content: "✅ 文件已回滚: " + path + " → 快照#" + string(rune('0'+snapshot)), Success: true}, nil

	case "rollback_all":
		if err := rm.Rollback(); err != nil {
			return types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return types.ToolResult{Content: "✅ 所有文件已回滚到上一次快照", Success: true}, nil

	case "rollback_checkpoint":
		cpID := getArgString(args, "checkpoint_id", "")
		if cpID == "" {
			return types.ToolResult{Success: false, Error: "checkpoint_id is required for rollback_checkpoint"}, nil
		}
		if err := rm.RollbackToCheckpoint(cpID); err != nil {
			return types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return types.ToolResult{Content: "✅ 已回滚到检查点: " + cpID, Success: true}, nil

	default:
		return types.ToolResult{Success: false, Error: "Unknown action: " + action}, nil
	}
}

func getArgString(args map[string]interface{}, key, defaultVal string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return defaultVal
}
