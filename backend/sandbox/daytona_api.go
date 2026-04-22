package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// createViaDaytona creates a sandbox via Daytona Server API.
func (m *Manager) createViaDaytona(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	payload := map[string]interface{}{
		"name":     fmt.Sprintf("tiancan-sandbox-%d", time.Now().UnixNano()),
		"image":    cfg.Image,
		"env":      cfg.EnvVars,
		"language": "go", // Daytona workspace language hint
	}
	if cfg.ProjectPath != "" && cfg.MountProject {
		payload["projectPath"] = cfg.ProjectPath
	}

	data, err := m.client.doRequest("POST", "/api/workspace", payload)
	if err != nil {
		return nil, fmt.Errorf("daytona create: %w", err)
	}

	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("daytona parse: %w", err)
	}

	info := &SandboxInfo{
		ID:        result.ID,
		Name:      result.Name,
		State:     StateCreating,
		Image:     cfg.Image,
		CreatedAt: time.Now(),
		Config:    cfg,
	}

	// Wait for workspace to be running
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		status, err := m.getDaytonaStatus(result.ID)
		if err != nil {
			continue
		}
		if status == "running" {
			info.State = StateRunning
			break
		}
	}

	if info.State != StateRunning {
		info.State = StateError
		info.Error = "timeout waiting for sandbox to start"
	}

	m.mu.Lock()
	m.sandboxes[info.ID] = info
	m.mu.Unlock()

	// Set auto-kill timer
	if cfg.MaxLifetimeSec > 0 {
		go func() {
			time.Sleep(time.Duration(cfg.MaxLifetimeSec) * time.Second)
			m.Stop(context.Background(), info.ID)
		}()
	}

	return info, nil
}

// execViaDaytona runs a command in a Daytona workspace.
func (m *Manager) execViaDaytona(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	payload := map[string]interface{}{
		"command": cmd,
	}
	data, err := m.client.doRequest("POST", "/api/workspace/"+id+"/execute", payload)
	if err != nil {
		return nil, fmt.Errorf("daytona exec: %w", err)
	}

	var result struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("daytona parse exec: %w", err)
	}

	return &ExecResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
	}, nil
}

// stopViaDaytona stops and removes a Daytona workspace.
func (m *Manager) stopViaDaytona(ctx context.Context, id string) error {
	_, err := m.client.doRequest("DELETE", "/api/workspace/"+id, nil)
	if err != nil {
		return fmt.Errorf("daytona stop: %w", err)
	}
	m.mu.Lock()
	if info, ok := m.sandboxes[id]; ok {
		info.State = StateStopped
	}
	m.mu.Unlock()
	return nil
}

// getDaytonaStatus queries the workspace status.
func (m *Manager) getDaytonaStatus(id string) (string, error) {
	data, err := m.client.doRequest("GET", "/api/workspace/"+id, nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	return result.Status, nil
}
