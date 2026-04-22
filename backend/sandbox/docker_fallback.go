package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// createViaDocker creates a sandbox container via Docker CLI with security constraints.
func (m *Manager) createViaDocker(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	name := fmt.Sprintf("tiancan-sandbox-%d", time.Now().UnixNano())
	args := []string{"run", "-d"}

	// Name
	args = append(args, "--name", name)

	// Auto-remove on stop
	if cfg.AutoRemove {
		args = append(args, "--rm")
	}

	// Read-only root filesystem
	if cfg.ReadOnlyFS {
		args = append(args, "--read-only")
		// Need tmpfs for /tmp, /run, /var/tmp
		args = append(args, "--tmpfs", "/tmp:size=64m")
		args = append(args, "--tmpfs", "/run:size=16m")
		args = append(args, "--tmpfs", "/var/tmp:size=16m")
	}

	// CPU limit
	if cfg.CPULimit > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.1f", cfg.CPULimit))
	}

	// Memory limit
	if cfg.MemoryMB > 0 {
		args = append(args, "-m", fmt.Sprintf("%dm", cfg.MemoryMB))
	}

	// Network mode
	switch cfg.NetworkMode {
	case "none":
		args = append(args, "--network", "none")
	case "host":
		args = append(args, "--network", "host")
	case "bridge", "":
		// default
	default:
		args = append(args, "--network", cfg.NetworkMode)
	}

	// Security options
	args = append(args,
		"--cap-drop", "ALL",          // Drop all Linux capabilities
		"--security-opt", "no-new-privileges", // Prevent privilege escalation
		"--pids-limit", "64",         // Limit process count
	)

	// Environment variables
	for k, v := range cfg.EnvVars {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Mount project directory (read-only for safety)
	if cfg.MountProject && cfg.ProjectPath != "" {
		args = append(args, "-v", cfg.ProjectPath+":/workspace:ro")
	}

	// Disable stdin
	args = append(args, "-i=false")

	// Image
	args = append(args, cfg.Image)

	// Keep container alive with sleep
	args = append(args, "sh", "-c", "sleep infinity")

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %w\n%s", err, string(out))
	}

	containerID := strings.TrimSpace(string(out))

	info := &SandboxInfo{
		ID:        containerID,
		Name:      name,
		State:     StateRunning,
		Image:     cfg.Image,
		CreatedAt: time.Now(),
		Config:    cfg,
	}

	m.mu.Lock()
	m.sandboxes[containerID] = info
	m.mu.Unlock()

	// Set auto-kill timer
	if cfg.MaxLifetimeSec > 0 {
		go func() {
			time.Sleep(time.Duration(cfg.MaxLifetimeSec) * time.Second)
			m.Stop(context.Background(), containerID)
		}()
	}

	return info, nil
}

// execViaDocker runs a command inside a Docker container.
func (m *Manager) execViaDocker(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	args := []string{"exec", id, "sh", "-c", cmd}

	c := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("docker exec: %w", err)
		}
	}

	return &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

// stopViaDocker stops and removes a Docker container.
func (m *Manager) stopViaDocker(ctx context.Context, id string) error {
	// Try graceful stop first
	cmd := exec.CommandContext(ctx, "docker", "stop", id, "-t", "5")
	cmd.Run() // ignore error, container might already be gone

	// Remove (no-op if --rm was used)
	cmd = exec.CommandContext(ctx, "docker", "rm", "-f", id)
	cmd.Run()

	m.mu.Lock()
	if info, ok := m.sandboxes[id]; ok {
		info.State = StateStopped
	}
	m.mu.Unlock()
	return nil
}
