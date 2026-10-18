package sandbox

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/linlurui/tiancan-ai-ide/backend/browser"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// Service wraps the Manager for Wails binding.
// This provides a cleaner API surface for frontend bindings.
type Service struct {
	mgr *Manager
}

// NewService creates a new sandbox service.
func NewService() *Service {
	return &Service{
		mgr: NewManager(),
	}
}

// GetManager returns the underlying manager.
func (s *Service) GetManager() *Manager {
	return s.mgr
}

// GetProviderStatus returns the status of all sandbox providers.
func (s *Service) GetProviderStatus() []ProviderStatus {
	return s.mgr.GetProviderStatuses()
}

// GetEnvStatus returns the status of required runtime environments.
func (s *Service) GetEnvStatus() EnvStatus {
	return EnvStatus{
		DockerAvailable:  isCommandAvailable("docker"),
		DaytonaAvailable: isCommandAvailable("daytona"),
		BrowserReady:    browser.Available(),
		NodeAvailable:    isCommandAvailable("node"),
	}
}

// SetProvider sets the active sandbox provider.
func (s *Service) SetProvider(name string) error {
	return s.mgr.SetProvider(name)
}

// GetCurrentProvider returns the name of the current provider.
func (s *Service) GetCurrentProvider() string {
	provider := envconfig.EnvString("TIANCAN_SANDBOX_PROVIDER")
	if provider == "" {
		provider = "docker" // default
	}
	return provider
}

// CreateSandbox creates a new sandbox.
func (s *Service) CreateSandbox(image string, cpuLimit float64, memoryMB int) (*SandboxInfo, error) {
	cfg := DefaultConfig()
	cfg.Image = image
	cfg.CPULimit = cpuLimit
	cfg.MemoryMB = memoryMB

	return s.mgr.Create(context.Background(), cfg)
}

// StopSandbox stops a sandbox.
func (s *Service) StopSandbox(id string) error {
	return s.mgr.Stop(context.Background(), id)
}

// ListSandboxes returns all sandboxes.
func (s *Service) ListSandboxes() []*SandboxInfo {
	return s.mgr.List()
}

// ExecInSandbox executes a command in a sandbox.
func (s *Service) ExecInSandbox(id string, cmd string) (*ExecResult, error) {
	return s.mgr.Exec(context.Background(), id, cmd)
}

// InstallProvider installs a sandbox provider.
func (s *Service) InstallProvider(name string) (string, error) {
	var installCmd *exec.Cmd

	switch name {
	case "docker":
		if isCommandAvailable("docker") {
			return "", fmt.Errorf("Docker 已安装")
		}
		installCmd = exec.Command("brew", "install", "--cask", "docker")
	case "daytona":
		if isCommandAvailable("daytona") {
			return "", fmt.Errorf("Daytona 已安装")
		}
		installCmd = exec.Command("brew", "install", "daytona")
	default:
		return "", fmt.Errorf("不支持的 provider: %s", name)
	}

	out, err := installCmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}

	return string(out), nil
}

// EnvStatus represents the status of required runtime environments.
type EnvStatus struct {
	DockerAvailable  bool `json:"dockerAvailable"`
	DaytonaAvailable bool `json:"daytonaAvailable"`
	BrowserReady    bool `json:"browserReady"`
	NodeAvailable    bool `json:"nodeAvailable"`
}
