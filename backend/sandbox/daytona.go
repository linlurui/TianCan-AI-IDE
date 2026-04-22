package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// TODO: Add E2B sandbox integration as alternative to Daytona (micro-VM level isolation)
// TODO: Add sandbox provider abstraction (Daytona | Docker | E2B) selectable via config
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
	mu        sync.RWMutex
	client    *DaytonaClient
	sandboxes map[string]*SandboxInfo
}

func NewManager() *Manager {
	return &Manager{client: NewDaytonaClient(), sandboxes: make(map[string]*SandboxInfo)}
}

func (m *Manager) Create(ctx context.Context, cfg SandboxConfig) (*SandboxInfo, error) {
	if m.client.IsDaytonaMode() {
		return m.createViaDaytona(ctx, cfg)
	}
	return m.createViaDocker(ctx, cfg)
}

func (m *Manager) Exec(ctx context.Context, id string, cmd string) (*ExecResult, error) {
	if m.client.IsDaytonaMode() {
		return m.execViaDaytona(ctx, id, cmd)
	}
	return m.execViaDocker(ctx, id, cmd)
}

func (m *Manager) Stop(ctx context.Context, id string) error {
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
