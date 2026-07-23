package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SandboxProvider defines the interface for sandbox backends.
type SandboxProvider interface {
	Name() string
	Create(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error)
	Exec(ctx context.Context, id string, cmd string) (*ExecResult, error)
	Stop(ctx context.Context, id string) error
}

type SandboxConfig struct {
	Image          string            `json:"image"`
	CPULimit       float64           `json:"cpuLimit"`
	MemoryMB       int               `json:"memoryMB"`
	NetworkMode    string            `json:"networkMode"`
	ReadOnlyFS     bool              `json:"readOnlyFS"`
	EnvVars        map[string]string `json:"envVars"`
	MountProject   bool              `json:"mountProject"`
	ProjectPath    string            `json:"projectPath"`
	AutoRemove     bool              `json:"autoRemove"`
	MaxLifetimeSec int               `json:"maxLifetimeSec"`
}

func DefaultConfig() SandboxConfig {
	return SandboxConfig{
		Image: "ubuntu:22.04", CPULimit: 1.0, MemoryMB: 512,
		NetworkMode: "none", ReadOnlyFS: true, MountProject: true,
		AutoRemove: true, MaxLifetimeSec: 300,
	}
}

type SandboxState string

const (
	StateCreating SandboxState = "creating"
	StateRunning  SandboxState = "running"
	StateStopped  SandboxState = "stopped"
	StateError    SandboxState = "error"
)

type SandboxInfo struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	State     SandboxState  `json:"state"`
	Image     string        `json:"image"`
	CreatedAt time.Time     `json:"createdAt"`
	Config    SandboxConfig `json:"config"`
	ExitCode  int           `json:"exitCode,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type ExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type DaytonaClient struct {
	serverURL         string
	apiKey            string
	httpClient        *http.Client
	useDockerFallback bool
}

func NewDaytonaClient() *DaytonaClient {
	url := os.Getenv("DAYTONA_SERVER_URL")
	key := os.Getenv("DAYTONA_API_KEY")
	fallback := url == ""
	if !fallback {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(url + "/health")
		if err != nil || resp.StatusCode != 200 {
			fallback = true
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	return &DaytonaClient{serverURL: url, apiKey: key,
		httpClient: &http.Client{Timeout: 30 * time.Second}, useDockerFallback: fallback}
}

func (c *DaytonaClient) IsDaytonaMode() bool { return !c.useDockerFallback }

func (c *DaytonaClient) doRequest(method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.serverURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

type Manager struct {
	mu         sync.RWMutex
	client     *DaytonaClient
	provider   SandboxProvider
	sandboxes  map[string]*SandboxInfo
	installDir string // directory for auto-installed binaries
}

func NewManager() *Manager {
	installDir := os.Getenv("TIANCAN_SANDBOX_INSTALL_DIR")
	if installDir == "" {
		home, _ := os.UserHomeDir()
		installDir = home + "/.tiancan/bin"
	}
	m := &Manager{
		client:     NewDaytonaClient(),
		sandboxes:  make(map[string]*SandboxInfo),
		installDir: installDir,
	}
	m.provider = m.resolveProvider()
	return m
}

// resolveProvider selects the sandbox provider based on TIANCAN_SANDBOX_PROVIDER env.
// Options: "daytona", "docker", "e2b". Default: auto-detect (daytona if available, else docker).
func (m *Manager) resolveProvider() SandboxProvider {
	switch os.Getenv("TIANCAN_SANDBOX_PROVIDER") {
	case "e2b":
		return &E2BProvider{apiKey: os.Getenv("E2B_API_KEY")}
	case "daytona":
		if m.client.IsDaytonaMode() {
			return &DaytonaProvider{client: m.client, mgr: m}
		}
		return &DockerProvider{mgr: m}
	case "docker":
		return &DockerProvider{mgr: m}
	default:
		// auto-detect
		if m.client.IsDaytonaMode() {
			return &DaytonaProvider{client: m.client, mgr: m}
		}
		return &DockerProvider{mgr: m}
	}
}

// GetProvider returns the active sandbox provider.
func (m *Manager) GetProvider() SandboxProvider { return m.provider }

// ProviderStatus returns the availability status of each sandbox provider.
// Used by the frontend to show which providers are available/configured.
type ProviderStatus struct {
	Name        string `json:"name"`
	Installed   bool   `json:"installed"`  // Binary/tool is installed
	Running     bool   `json:"running"`    // Service is running
	Configured  bool   `json:"configured"` // API keys / env vars are set
	Version     string `json:"version,omitempty"`
	InstallPath string `json:"installPath,omitempty"`
}

// GetProviderStatuses checks availability of all sandbox providers.
func (m *Manager) GetProviderStatuses() []ProviderStatus {
	var statuses []ProviderStatus

	// Daytona
	daytonaInstalled := isCommandAvailable("daytona")
	daytonaRunning := false
	daytonaVersion := ""
	if daytonaInstalled {
		out, err := exec.Command("daytona", "version").CombinedOutput()
		if err == nil {
			daytonaVersion = strings.TrimSpace(string(out))
		}
		daytonaRunning = m.client.IsDaytonaMode()
	}
	statuses = append(statuses, ProviderStatus{
		Name:        "daytona",
		Installed:   daytonaInstalled,
		Running:     daytonaRunning,
		Configured:  os.Getenv("DAYTONA_SERVER_URL") != "" && os.Getenv("DAYTONA_API_KEY") != "",
		Version:     daytonaVersion,
		InstallPath: whichCommand("daytona"),
	})

	// Docker
	dockerInstalled := isCommandAvailable("docker")
	dockerRunning := false
	dockerVersion := ""
	if dockerInstalled {
		out, err := exec.Command("docker", "version", "--format", "{{.Client.Version}}").CombinedOutput()
		if err == nil {
			dockerVersion = strings.TrimSpace(string(out))
		}
		out, err = exec.Command("docker", "info").CombinedOutput()
		dockerRunning = err == nil
	}
	statuses = append(statuses, ProviderStatus{
		Name:        "docker",
		Installed:   dockerInstalled,
		Running:     dockerRunning,
		Configured:  dockerInstalled,
		Version:     dockerVersion,
		InstallPath: whichCommand("docker"),
	})

	// E2B
	statuses = append(statuses, ProviderStatus{
		Name:       "e2b",
		Installed:  true, // E2B is cloud-based, no local install needed
		Running:    true,
		Configured: os.Getenv("E2B_API_KEY") != "",
	})

	return statuses
}

// AutoInstallProvider auto-downloads and installs a sandbox provider on first run.
// Returns installation logs for UI feedback.
func (m *Manager) AutoInstallProvider(providerName string) ([]string, error) {
	os.MkdirAll(m.installDir, 0755)
	switch providerName {
	case "daytona":
		return m.installDaytona()
	case "docker":
		return m.installDocker()
	default:
		return nil, fmt.Errorf("unsupported provider for auto-install: %s", providerName)
	}
}

// installDaytona downloads and installs the Daytona CLI.
func (m *Manager) installDaytona() ([]string, error) {
	var logs []string
	logs = append(logs, "正在下载 Daytona CLI...")

	var downloadURL string
	switch runtime.GOOS {
	case "darwin":
		downloadURL = "https://download.daytona.io/daytona/latest/daytona-darwin-x64"
		if runtime.GOARCH == "arm64" {
			downloadURL = "https://download.daytona.io/daytona/latest/daytona-darwin-arm64"
		}
	case "linux":
		downloadURL = "https://download.daytona.io/daytona/latest/daytona-linux-x64"
	case "windows":
		downloadURL = "https://download.daytona.io/daytona/latest/daytona-windows-x64.exe"
	default:
		return logs, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	destPath := m.installDir + "/daytona"
	if runtime.GOOS == "windows" {
		destPath += ".exe"
	}

	resp, err := http.Get(downloadURL)
	if err != nil {
		return logs, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return logs, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return logs, fmt.Errorf("read download: %w", err)
	}

	if err := os.WriteFile(destPath, data, 0755); err != nil {
		return logs, fmt.Errorf("write binary: %w", err)
	}

	logs = append(logs, fmt.Sprintf("Daytona CLI 已安装到 %s", destPath))
	log.Printf("sandbox: Daytona installed to %s", destPath)
	return logs, nil
}

// installDocker provides instructions and downloads Docker Desktop.
func (m *Manager) installDocker() ([]string, error) {
	var logs []string

	// Docker Desktop is a large GUI app — we can't silently install it.
	// Instead, download the installer and let the user run it.
	logs = append(logs, "Docker Desktop 需要手动安装，正在下载安装程序...")

	var downloadURL string
	switch runtime.GOOS {
	case "darwin":
		downloadURL = "https://desktop.docker.com/mac/main/arm64/Docker.dmg"
		if runtime.GOARCH == "amd64" {
			downloadURL = "https://desktop.docker.com/mac/main/amd64/Docker.dmg"
		}
	case "linux":
		// On Linux, try to install via convenience script
		logs = append(logs, "正在通过官方脚本安装 Docker Engine...")
		cmd := exec.Command("sh", "-c", "curl -fsSL https://get.docker.com | sh")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return logs, fmt.Errorf("Docker install script failed: %s: %w", string(out[:min(len(out), 300)]), err)
		}
		logs = append(logs, "Docker Engine 安装完成")
		return logs, nil
	case "windows":
		downloadURL = "https://desktop.docker.com/win/main/amd64/Docker%%20Desktop%%20Installer.exe"
	default:
		return logs, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	destPath := m.installDir + "/docker-installer"
	if runtime.GOOS == "darwin" {
		destPath += ".dmg"
	} else {
		destPath += ".exe"
	}

	resp, err := http.Get(downloadURL)
	if err != nil {
		return logs, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return logs, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return logs, fmt.Errorf("read download: %w", err)
	}

	if err := os.WriteFile(destPath, data, 0755); err != nil {
		return logs, fmt.Errorf("write installer: %w", err)
	}

	logs = append(logs, fmt.Sprintf("Docker 安装程序已下载到 %s，请手动运行安装", destPath))
	return logs, nil
}

// SetE2BAPIKey configures the E2B API key (called from frontend UI).
func (m *Manager) SetE2BAPIKey(apiKey string) {
	os.Setenv("E2B_API_KEY", apiKey)
	// Re-resolve provider if current is E2B
	if m.provider != nil && m.provider.Name() == "e2b" {
		m.mu.Lock()
		m.provider = &E2BProvider{apiKey: apiKey}
		m.mu.Unlock()
	}
}

// SetProvider manually selects the sandbox provider.
func (m *Manager) SetProvider(providerName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch providerName {
	case "daytona":
		if !isCommandAvailable("daytona") {
			return fmt.Errorf("Daytona 未安装，请先自动安装")
		}
		m.provider = &DaytonaProvider{client: m.client, mgr: m}
	case "docker":
		if !isCommandAvailable("docker") {
			return fmt.Errorf("Docker 未安装，请先自动安装")
		}
		m.provider = &DockerProvider{mgr: m}
	case "e2b":
		apiKey := os.Getenv("E2B_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("E2B API Key 未配置")
		}
		m.provider = &E2BProvider{apiKey: apiKey}
	default:
		return fmt.Errorf("未知 provider: %s", providerName)
	}
	os.Setenv("TIANCAN_SANDBOX_PROVIDER", providerName)
	return nil
}

// isCommandAvailable checks if a command is available in PATH.
func isCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// whichCommand returns the full path of a command.
func whichCommand(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) Create(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	if m.provider != nil {
		return m.provider.Create(ctx, cfg)
	}
	// Fallback for legacy Manager without provider
	if m.client.IsDaytonaMode() {
		return m.createViaDaytona(ctx, cfg)
	}
	return m.createViaDocker(ctx, cfg)
}

func (m *Manager) Exec(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	if m.provider != nil {
		return m.provider.Exec(ctx, id, cmd)
	}
	if m.client.IsDaytonaMode() {
		return m.execViaDaytona(ctx, id, cmd)
	}
	return m.execViaDocker(ctx, id, cmd)
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if m.provider != nil {
		return m.provider.Stop(ctx, id)
	}
	if m.client.IsDaytonaMode() {
		return m.stopViaDaytona(ctx, id)
	}
	return m.stopViaDocker(ctx, id)
}

func (m *Manager) GetInfo(id string) (*SandboxInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info, ok := m.sandboxes[id]
	return info, ok
}

func (m *Manager) List() []*SandboxInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*SandboxInfo, 0, len(m.sandboxes))
	for _, info := range m.sandboxes {
		out = append(out, info)
	}
	return out
}

func (m *Manager) ReadFile(ctx context.Context, id, path string) (string, error) {
	res, err := m.Exec(ctx, id, fmt.Sprintf("cat %q", path))
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

func (m *Manager) WriteFile(ctx context.Context, id, path, content string) error {
	// Use heredoc to avoid shell escaping issues
	cmd := fmt.Sprintf("cat > %q << 'SANDBOX_EOF'\n%s\nSANDBOX_EOF", path, content)
	res, err := m.Exec(ctx, id, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write failed: %s", res.Stderr)
	}
	return nil
}

// ─── Sandbox Provider Implementations ────────────────────────────────

// DaytonaProvider implements SandboxProvider using Daytona server.
type DaytonaProvider struct {
	client *DaytonaClient
	mgr    *Manager // reference to parent Manager for shared state
}

func (p *DaytonaProvider) Name() string { return "daytona" }
func (p *DaytonaProvider) Create(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	return p.mgr.createViaDaytona(ctx, cfg)
}
func (p *DaytonaProvider) Exec(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	return p.mgr.execViaDaytona(ctx, id, cmd)
}
func (p *DaytonaProvider) Stop(ctx context.Context, id string) error {
	return p.mgr.stopViaDaytona(ctx, id)
}

// DockerProvider implements SandboxProvider using local Docker.
type DockerProvider struct {
	mgr *Manager // reference to parent Manager for shared state
}

func (p *DockerProvider) Name() string { return "docker" }
func (p *DockerProvider) Create(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	return p.mgr.createViaDocker(ctx, cfg)
}
func (p *DockerProvider) Exec(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	return p.mgr.execViaDocker(ctx, id, cmd)
}
func (p *DockerProvider) Stop(ctx context.Context, id string) error {
	return p.mgr.stopViaDocker(ctx, id)
}

// E2BProvider implements SandboxProvider using E2B micro-VM sandbox.
type E2BProvider struct {
	apiKey string
}

func (p *E2BProvider) Name() string { return "e2b" }
func (p *E2BProvider) Create(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("E2B_API_KEY not configured")
	}

	// E2B sandbox create API
	body := map[string]interface{}{
		"templateID": "base",
		"metadata":   map[string]string{"image": cfg.Image},
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.e2b.dev/sandboxes", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("E2B create failed: %w", err)
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("E2B read response: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respData, &result); err != nil {
		return nil, fmt.Errorf("E2B invalid response: %w", err)
	}

	sandboxID, _ := result["sandboxID"].(string)
	if sandboxID == "" {
		return nil, fmt.Errorf("E2B create returned no sandboxID")
	}

	info := &SandboxInfo{
		ID:        sandboxID,
		Name:      "e2b-" + sandboxID[:8],
		State:     StateRunning,
		Image:     cfg.Image,
		CreatedAt: time.Now(),
		Config:    cfg,
	}
	return info, nil
}

func (p *E2BProvider) Exec(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("E2B_API_KEY not configured")
	}

	body := map[string]interface{}{
		"command": cmd,
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://api.e2b.dev/sandboxes/%s/execute", id), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("E2B exec failed: %w", err)
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("E2B read exec response: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respData, &result); err != nil {
		return nil, fmt.Errorf("E2B invalid exec response: %w", err)
	}

	exitCode := 0
	if v, ok := result["exitCode"].(float64); ok {
		exitCode = int(v)
	}
	stdout, _ := result["stdout"].(string)
	stderr, _ := result["stderr"].(string)

	return &ExecResult{ExitCode: exitCode, Stdout: stdout, Stderr: stderr}, nil
}

func (p *E2BProvider) Stop(ctx context.Context, id string) error {
	if p.apiKey == "" {
		return fmt.Errorf("E2B_API_KEY not configured")
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("https://api.e2b.dev/sandboxes/%s", id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("E2B stop failed: %w", err)
	}
	defer resp.Body.Close()
	return nil
}
