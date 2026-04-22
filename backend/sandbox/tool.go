package sandbox

import (
	"context"
	"encoding/json"
	"fmt"

	agenttypes "github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// SandboxCreateTool creates a new container sandbox.
type SandboxCreateTool struct {
	Manager *Manager
}

func (t *SandboxCreateTool) Name() string { return "sandbox_create" }
func (t *SandboxCreateTool) Description() string {
	return "Create an isolated container sandbox for safe code execution. The sandbox has restricted network, limited CPU/memory, and read-only filesystem by default."
}
func (t *SandboxCreateTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"image": map[string]interface{}{
				"type":        "string",
				"description": "Docker image to use (default: ubuntu:22.04)",
			},
			"cpu_limit": map[string]interface{}{
				"type":        "number",
				"description": "CPU core limit (default: 1.0)",
			},
			"memory_mb": map[string]interface{}{
				"type":        "number",
				"description": "Memory limit in MB (default: 512)",
			},
			"network_mode": map[string]interface{}{
				"type":        "string",
				"description": "Network mode: none, bridge, host (default: none)",
			},
			"mount_project": map[string]interface{}{
				"type":        "boolean",
				"description": "Mount project directory into sandbox (default: true)",
			},
			"project_path": map[string]interface{}{
				"type":        "string",
				"description": "Host project path to mount",
			},
			"max_lifetime_sec": map[string]interface{}{
				"type":        "number",
				"description": "Auto-kill after N seconds (default: 300)",
			},
		},
	}
}
func (t *SandboxCreateTool) IsReadOnly() bool        { return true }
func (t *SandboxCreateTool) IsConcurrencySafe() bool { return true }
func (t *SandboxCreateTool) Execute(ctx context.Context, args map[string]interface{}) (agenttypes.ToolResult, error) {
	cfg := DefaultConfig()
	if v, ok := args["image"].(string); ok && v != "" {
		cfg.Image = v
	}
	if v, ok := args["cpu_limit"].(float64); ok {
		cfg.CPULimit = v
	}
	if v, ok := args["memory_mb"].(float64); ok {
		cfg.MemoryMB = int(v)
	}
	if v, ok := args["network_mode"].(string); ok {
		cfg.NetworkMode = v
	}
	if v, ok := args["mount_project"].(bool); ok {
		cfg.MountProject = v
	}
	if v, ok := args["project_path"].(string); ok {
		cfg.ProjectPath = v
	}
	if v, ok := args["max_lifetime_sec"].(float64); ok {
		cfg.MaxLifetimeSec = int(v)
	}

	info, err := t.Manager.Create(ctx, cfg)
	if err != nil {
		return agenttypes.ToolResult{Success: false, Error: err.Error()}, nil
	}

	data, _ := json.MarshalIndent(info, "", "  ")
	return agenttypes.ToolResult{Success: true, Content: string(data)}, nil
}

// SandboxExecTool executes a command inside a sandbox.
type SandboxExecTool struct {
	Manager *Manager
}

func (t *SandboxExecTool) Name() string { return "sandbox_exec" }
func (t *SandboxExecTool) Description() string {
	return "Execute a command inside a container sandbox. Returns stdout, stderr, and exit code."
}
func (t *SandboxExecTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sandbox_id": map[string]interface{}{
				"type":        "string",
				"description": "ID of the sandbox container",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command to execute inside the sandbox",
			},
		},
		"required": []string{"sandbox_id", "command"},
	}
}
func (t *SandboxExecTool) IsReadOnly() bool        { return false }
func (t *SandboxExecTool) IsConcurrencySafe() bool { return false }
func (t *SandboxExecTool) Execute(ctx context.Context, args map[string]interface{}) (agenttypes.ToolResult, error) {
	id, _ := args["sandbox_id"].(string)
	cmd, _ := args["command"].(string)
	if id == "" || cmd == "" {
		return agenttypes.ToolResult{Success: false, Error: "sandbox_id and command are required"}, nil
	}

	result, err := t.Manager.Exec(ctx, id, cmd)
	if err != nil {
		return agenttypes.ToolResult{Success: false, Error: err.Error()}, nil
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return agenttypes.ToolResult{Success: result.ExitCode == 0, Content: string(data)}, nil
}

// SandboxStopTool stops and removes a sandbox.
type SandboxStopTool struct {
	Manager *Manager
}

func (t *SandboxStopTool) Name() string { return "sandbox_stop" }
func (t *SandboxStopTool) Description() string {
	return "Stop and remove a container sandbox, freeing all resources."
}
func (t *SandboxStopTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sandbox_id": map[string]interface{}{
				"type":        "string",
				"description": "ID of the sandbox to stop",
			},
		},
		"required": []string{"sandbox_id"},
	}
}
func (t *SandboxStopTool) IsReadOnly() bool        { return false }
func (t *SandboxStopTool) IsConcurrencySafe() bool { return true }
func (t *SandboxStopTool) Execute(ctx context.Context, args map[string]interface{}) (agenttypes.ToolResult, error) {
	id, _ := args["sandbox_id"].(string)
	if id == "" {
		return agenttypes.ToolResult{Success: false, Error: "sandbox_id is required"}, nil
	}

	err := t.Manager.Stop(ctx, id)
	if err != nil {
		return agenttypes.ToolResult{Success: false, Error: err.Error()}, nil
	}

	return agenttypes.ToolResult{Success: true, Content: fmt.Sprintf("Sandbox %s stopped and removed", id)}, nil
}
