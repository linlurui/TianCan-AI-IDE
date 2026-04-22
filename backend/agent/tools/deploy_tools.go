package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Deploy build/run/status/rollback tools ─────────────────────

type DeployBuildTool struct{ rootPath string }

func (t *DeployBuildTool) Name() string        { return "deploy_build" }
func (t *DeployBuildTool) IsReadOnly() bool     { return false }
func (t *DeployBuildTool) IsConcurrencySafe() bool { return false }
func (t *DeployBuildTool) Description() string {
	return "Build the project for deployment. Args: command (build cmd), workDir, env (object), timeout (seconds, default 300)"
}
func (t *DeployBuildTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Build command (e.g. 'go build -o app .', 'npm run build')"},
			"workDir": map[string]interface{}{"type": "string", "description": "Working directory (default project root)"},
			"env":     map[string]interface{}{"type": "object", "description": "Environment variables"},
			"timeout": map[string]interface{}{"type": "integer", "description": "Timeout seconds (default 300)"},
		},
		"required": []string{"command"},
	}
}
func (t *DeployBuildTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	cmdStr, _ := args["command"].(string)
	if cmdStr == "" {
		return types.ToolResult{Content: "command is required", Success: false}, nil
	}
	workDir, _ := args["workDir"].(string)
	if workDir == "" { workDir = t.rootPath } else { workDir = resolvePath(workDir, t.rootPath) }
	timeoutSec := toInt(args["timeout"], 300)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Dir = workDir
	if env, ok := args["env"].(map[string]interface{}); ok {
		for k, v := range env {
			if s, ok := v.(string); ok {
				cmd.Env = append(cmd.Environ(), k+"="+s)
			}
		}
	}

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	result := fmt.Sprintf("Build: %s\nDir: %s\nDuration: %v\n\n%s", cmdStr, workDir, elapsed, string(out))
	if err != nil {
		result += fmt.Sprintf("\nError: %v", err)
		return types.ToolResult{Content: result, Success: false}, nil
	}
	return types.ToolResult{Content: result, Success: true}, nil
}

// ── DeployRunTool ──────────────────────────────────────────────

type DeployRunTool struct{ rootPath string }

func (t *DeployRunTool) Name() string        { return "deploy_run" }
func (t *DeployRunTool) IsReadOnly() bool     { return false }
func (t *DeployRunTool) IsConcurrencySafe() bool { return false }
func (t *DeployRunTool) Description() string {
	return "Deploy/run the project (local or remote via SSH). Args: type (local/ssh/docker/k8s), command, host, user, keyPath, dockerImage, k8sManifest, namespace"
}
func (t *DeployRunTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":        map[string]interface{}{"type": "string", "description": "local/ssh/docker/k8s"},
			"command":     map[string]interface{}{"type": "string", "description": "Deploy command or script"},
			"host":        map[string]interface{}{"type": "string"},
			"port":        map[string]interface{}{"type": "integer"},
			"user":        map[string]interface{}{"type": "string"},
			"keyPath":     map[string]interface{}{"type": "string"},
			"password":    map[string]interface{}{"type": "string"},
			"dockerImage": map[string]interface{}{"type": "string"},
			"k8sManifest": map[string]interface{}{"type": "string"},
			"namespace":   map[string]interface{}{"type": "string"},
			"timeout":     map[string]interface{}{"type": "integer"},
		},
		"required": []string{"type"},
	}
}
func (t *DeployRunTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	deployType, _ := args["type"].(string)
	timeoutSec := toInt(args["timeout"], 300)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	switch deployType {
	case "local":
		cmdStr, _ := args["command"].(string)
		if cmdStr == "" {
			return types.ToolResult{Content: "command required for local deploy", Success: false}, nil
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Dir = t.rootPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			return types.ToolResult{Content: fmt.Sprintf("Local deploy error: %v\n%s", err, string(out)), Success: false}, nil
		}
		return types.ToolResult{Content: fmt.Sprintf("Local deploy OK:\n%s", string(out)), Success: true}, nil

	case "ssh":
		host, _ := args["host"].(string)
		user, _ := args["user"].(string)
		cmdStr, _ := args["command"].(string)
		if host == "" || cmdStr == "" {
			return types.ToolResult{Content: "host and command required for SSH deploy", Success: false}, nil
		}
		port := toInt(args["port"], 22)
		keyPath, _ := args["keyPath"].(string)
		password, _ := args["password"].(string)

		sshCmd := buildSSHCmd(host, port, user, keyPath, password, cmdStr)
		out, err := runShellCmd(sshCmd, timeoutSec)
		if err != nil {
			return types.ToolResult{Content: fmt.Sprintf("SSH deploy error: %v\n%s", err, out), Success: false}, nil
		}
		return types.ToolResult{Content: fmt.Sprintf("SSH deploy OK:\n%s", out), Success: true}, nil

	case "docker":
		image, _ := args["dockerImage"].(string)
		if image == "" { image = "app:latest" }
		cmdStr, _ := args["command"].(string)
		if cmdStr == "" {
			cmdStr = fmt.Sprintf("docker run -d --name app %s", image)
		}
		out, err := runShellCmd(cmdStr, timeoutSec)
		if err != nil {
			return types.ToolResult{Content: fmt.Sprintf("Docker deploy error: %v\n%s", err, out), Success: false}, nil
		}
		return types.ToolResult{Content: fmt.Sprintf("Docker deploy OK:\n%s", out), Success: true}, nil

	case "k8s":
		manifest, _ := args["k8sManifest"].(string)
		ns, _ := args["namespace"].(string)
		if ns == "" { ns = "default" }
		if manifest != "" {
			// Write manifest to temp and apply
			tmpFile := fmt.Sprintf("/tmp/k8s-deploy-%d.yaml", time.Now().UnixNano())
			writeCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", tmpFile, manifest)
			if out, err := runShellCmd(writeCmd, 10); err != nil {
				return types.ToolResult{Content: fmt.Sprintf("Write manifest error: %v\n%s", err, out), Success: false}, nil
			}
			applyCmd := fmt.Sprintf("kubectl apply -f %s -n %s", tmpFile, ns)
			out, err := runShellCmd(applyCmd, timeoutSec)
			if err != nil {
				return types.ToolResult{Content: fmt.Sprintf("kubectl apply error: %v\n%s", err, out), Success: false}, nil
			}
			return types.ToolResult{Content: fmt.Sprintf("K8s deploy OK:\n%s", out), Success: true}, nil
		}
		cmdStr, _ := args["command"].(string)
		if cmdStr == "" {
			return types.ToolResult{Content: "k8sManifest or command required", Success: false}, nil
		}
		out, err := runShellCmd(cmdStr, timeoutSec)
		if err != nil {
			return types.ToolResult{Content: fmt.Sprintf("K8s error: %v\n%s", err, out), Success: false}, nil
		}
		return types.ToolResult{Content: fmt.Sprintf("K8s OK:\n%s", out), Success: true}, nil

	default:
		return types.ToolResult{Content: fmt.Sprintf("Unknown deploy type: %s (local/ssh/docker/k8s)", deployType), Success: false}, nil
	}
}

// ── DeployStatusTool ───────────────────────────────────────────

type DeployStatusTool struct{}

func (t *DeployStatusTool) Name() string        { return "deploy_status" }
func (t *DeployStatusTool) IsReadOnly() bool     { return true }
func (t *DeployStatusTool) IsConcurrencySafe() bool { return true }
func (t *DeployStatusTool) Description() string {
	return "Check deployment status. Args: type (docker/k8s/process), name, namespace (for k8s)"
}
func (t *DeployStatusTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":      map[string]interface{}{"type": "string", "description": "docker/k8s/process"},
			"name":      map[string]interface{}{"type": "string"},
			"namespace": map[string]interface{}{"type": "string"},
		},
		"required": []string{"type"},
	}
}
func (t *DeployStatusTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	checkType, _ := args["type"].(string)
	name, _ := args["name"].(string)
	ns, _ := args["namespace"].(string)
	if ns == "" { ns = "default" }

	var cmdStr string
	switch checkType {
	case "docker":
		if name != "" {
			cmdStr = fmt.Sprintf("docker inspect %s --format '{{.State.Status}}'", name)
		} else {
			cmdStr = "docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'"
		}
	case "k8s":
		if name != "" {
			cmdStr = fmt.Sprintf("kubectl get %s -n %s -o wide", name, ns)
		} else {
			cmdStr = fmt.Sprintf("kubectl get all -n %s", ns)
		}
	case "process":
		if name == "" {
			return types.ToolResult{Content: "name required for process status", Success: false}, nil
		}
		cmdStr = fmt.Sprintf("ps aux | grep %s | grep -v grep", name)
	default:
		return types.ToolResult{Content: fmt.Sprintf("Unknown type: %s", checkType), Success: false}, nil
	}

	out, err := runShellCmd(cmdStr, 30)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Status error: %v\n%s", err, out), Success: false}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// ── DeployRollbackTool ─────────────────────────────────────────

type DeployRollbackTool struct{}

func (t *DeployRollbackTool) Name() string        { return "deploy_rollback" }
func (t *DeployRollbackTool) IsReadOnly() bool     { return false }
func (t *DeployRollbackTool) IsConcurrencySafe() bool { return false }
func (t *DeployRollbackTool) Description() string {
	return "Rollback a deployment. Args: type (docker/k8s), name, namespace, revision (for k8s)"
}
func (t *DeployRollbackTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":      map[string]interface{}{"type": "string", "description": "docker/k8s"},
			"name":      map[string]interface{}{"type": "string"},
			"namespace": map[string]interface{}{"type": "string"},
			"revision":  map[string]interface{}{"type": "integer", "description": "K8s rollout revision"},
		},
		"required": []string{"type", "name"},
	}
}
func (t *DeployRollbackTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	rollbackType, _ := args["type"].(string)
	name, _ := args["name"].(string)
	if name == "" {
		return types.ToolResult{Content: "name is required", Success: false}, nil
	}
	ns, _ := args["namespace"].(string)
	if ns == "" { ns = "default" }

	var cmdStr string
	switch rollbackType {
	case "docker":
		cmdStr = fmt.Sprintf("docker stop %s && docker rm %s", name, name)
	case "k8s":
		rev := toInt(args["revision"], 0)
		if rev > 0 {
			cmdStr = fmt.Sprintf("kubectl rollout undo deployment/%s -n %s --to-revision=%d", name, ns, rev)
		} else {
			cmdStr = fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", name, ns)
		}
	default:
		return types.ToolResult{Content: fmt.Sprintf("Unknown: %s (docker/k8s)", rollbackType), Success: false}, nil
	}

	out, err := runShellCmd(cmdStr, 120)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Rollback error: %v\n%s", err, out), Success: false}, nil
	}
	return types.ToolResult{Content: fmt.Sprintf("Rollback OK:\n%s", out), Success: true}, nil
}

// buildSSHCmd constructs an SSH command string.
func buildSSHCmd(host string, port int, user, keyPath, password, cmd string) string {
	if password != "" {
		// Use sshpass for password auth
		return fmt.Sprintf("sshpass -p '%s' ssh -o StrictHostKeyChecking=no -p %d %s@%s '%s'",
			strings.ReplaceAll(password, "'", "'\\''"), port, user, host,
			strings.ReplaceAll(cmd, "'", "'\\''"))
	}
	if keyPath != "" {
		return fmt.Sprintf("ssh -o StrictHostKeyChecking=no -i %s -p %d %s@%s '%s'",
			keyPath, port, user, host, strings.ReplaceAll(cmd, "'", "'\\''"))
	}
	return fmt.Sprintf("ssh -o StrictHostKeyChecking=no -p %d %s@%s '%s'",
		port, user, host, strings.ReplaceAll(cmd, "'", "'\\''"))
}

// DeployHistoryTool shows recent deployment logs.
type DeployHistoryTool struct{}

func (t *DeployHistoryTool) Name() string        { return "deploy_logs" }
func (t *DeployHistoryTool) IsReadOnly() bool     { return true }
func (t *DeployHistoryTool) IsConcurrencySafe() bool { return true }
func (t *DeployHistoryTool) Description() string {
	return "View deployment logs. Args: type (docker/k8s), name, namespace, lines (default 100), follow (default false)"
}
func (t *DeployHistoryTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":      map[string]interface{}{"type": "string"},
			"name":      map[string]interface{}{"type": "string"},
			"namespace": map[string]interface{}{"type": "string"},
			"lines":     map[string]interface{}{"type": "integer"},
		},
		"required": []string{"type", "name"},
	}
}
func (t *DeployHistoryTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	logType, _ := args["type"].(string)
	name, _ := args["name"].(string)
	if name == "" {
		return types.ToolResult{Content: "name required", Success: false}, nil
	}
	ns, _ := args["namespace"].(string)
	if ns == "" { ns = "default" }
	lines := toInt(args["lines"], 100)

	var cmdStr string
	switch logType {
	case "docker":
		cmdStr = fmt.Sprintf("docker logs --tail %d %s", lines, name)
	case "k8s":
		cmdStr = fmt.Sprintf("kubectl logs --tail %d deployment/%s -n %s", lines, name, ns)
	default:
		return types.ToolResult{Content: fmt.Sprintf("Unknown: %s", logType), Success: false}, nil
	}

	out, err := runShellCmd(cmdStr, 30)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Logs error: %v\n%s", err, out), Success: false}, nil
	}
	return types.ToolResult{Content: out, Success: true}, nil
}

// Helper for deploy tools JSON output
func deployResultJSON(v interface{}) string {
	d, _ := json.MarshalIndent(v, "", "  ")
	return string(d)
}
