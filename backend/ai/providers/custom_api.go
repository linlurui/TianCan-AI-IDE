package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CustomAPIDiscoverer 自定义 API Provider 发现器
type CustomAPIDiscoverer struct{}

// Name 返回发现器名称
func (d *CustomAPIDiscoverer) Name() string {
	return "Custom API"
}

// Priority 返回优先级
func (d *CustomAPIDiscoverer) Priority() int {
	return 100 // 自定义 Provider 优先级最低
}

// Discover 发现已配置的自定义 API Provider
func (d *CustomAPIDiscoverer) Discover() (*Provider, error) {
	// 从配置文件读取已保存的自定义 Provider
	providers, err := LoadCustomProviders()
	if err != nil || len(providers) == 0 {
		return nil, fmt.Errorf("no custom providers configured")
	}

	// 返回第一个自定义 Provider
	return providers[0], nil
}

// Test 测试自定义 API Provider
func (d *CustomAPIDiscoverer) Test(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	testURL := strings.TrimSuffix(p.BaseURL, "/") + "/v1/models"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status: %d", resp.StatusCode)
	}

	return nil
}

// CustomProvidersManager 管理自定义 Provider 配置
type CustomProvidersManager struct {
	mu        sync.RWMutex
	providers map[string]*Provider
	configDir string
}

var customProvidersManager *CustomProvidersManager
var customProvidersOnce sync.Once

// GetCustomProvidersManager 获取全局管理器
func GetCustomProvidersManager() *CustomProvidersManager {
	customProvidersOnce.Do(func() {
		home, _ := os.UserHomeDir()
		customProvidersManager = &CustomProvidersManager{
			providers: make(map[string]*Provider),
			configDir: filepath.Join(home, ".tiancan", "providers"),
		}
	})
	return customProvidersManager
}

// Add 添加或更新自定义 Provider
func (m *CustomProvidersManager) Add(p *Provider) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 验证 Provider 配置
	if p.ID == "" {
		p.ID = generateProviderID(p.Name)
	}
	if p.BaseURL == "" {
		return fmt.Errorf("base URL is required")
	}

	// 测试连接
	if p.APIKey != "" {
		if err := m.testProvider(p); err != nil {
			p.Status = ProviderStatusError
			p.Error = err.Error()
		}
	}

	m.providers[p.ID] = p
	return m.saveConfig()
}

// Remove 移除自定义 Provider
func (m *CustomProvidersManager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.providers[id]; !ok {
		return fmt.Errorf("provider not found: %s", id)
	}

	delete(m.providers, id)
	return m.saveConfig()
}

// List 返回所有自定义 Provider
func (m *CustomProvidersManager) List() []*Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Provider, 0, len(m.providers))
	for _, p := range m.providers {
		result = append(result, p)
	}
	return result
}

// Get 获取指定 Provider
func (m *CustomProvidersManager) Get(id string) *Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providers[id]
}

// LoadCustomProviders 从文件加载已保存的 Provider
func LoadCustomProviders() ([]*Provider, error) {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".tiancan", "providers", "custom.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Provider{}, nil
		}
		return nil, err
	}

	var providers []*Provider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, err
	}

	return providers, nil
}

// saveConfig 保存配置到文件
func (m *CustomProvidersManager) saveConfig() error {
	if err := os.MkdirAll(m.configDir, 0755); err != nil {
		return err
	}

	configPath := filepath.Join(m.configDir, "custom.json")

	providers := make([]*Provider, 0, len(m.providers))
	for _, p := range m.providers {
		// 不保存敏感信息到文件
		safeProvider := &Provider{
			ID:       p.ID,
			Name:     p.Name,
			Type:     p.Type,
			Status:   p.Status,
			BaseURL:  p.BaseURL,
			Model:    p.Model,
			Priority: p.Priority,
			Icon:     p.Icon,
			Color:    p.Color,
			Error:    p.Error,
		}
		providers = append(providers, safeProvider)
	}

	data, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

// testProvider 测试 Provider 连接
func (m *CustomProvidersManager) testProvider(p *Provider) error {
	testURL := strings.TrimSuffix(p.BaseURL, "/") + "/v1/models"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status: %d", resp.StatusCode)
	}

	p.Status = ProviderStatusAvailable
	p.Error = ""
	return nil
}

// generateProviderID 生成 Provider ID
func generateProviderID(name string) string {
	// 简单的 ID 生成：转换为小写，移除空格和特殊字符
	id := strings.ToLower(name)
	id = strings.ReplaceAll(id, " ", "-")
	id = strings.ReplaceAll(id, "_", "-")
	id = strings.ReplaceAll(id, ".", "")
	return id
}

// CommonProviderConfigs 常用 Provider 配置模板
var CommonProviderConfigs = []struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"baseUrl"`
	Icon     string `json:"icon"`
	Color    string `json:"color"`
}{
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Icon: "🌊", Color: "#1e3a5f"},
	{ID: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1", Icon: "⚡", Color: "#f97316"},
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Icon: "🔵", Color: "#10a37f"},
	{ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com", Icon: "🟣", Color: "#d97709"},
	{ID: "gemini", Name: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Icon: "💎", Color: "#4285f4"},
	{ID: "ollama", Name: "Ollama (Local)", BaseURL: "http://localhost:11434/v1", Icon: "🦙", Color: "#f97316"},
	{ID: "lm-studio", Name: "LM Studio (Local)", BaseURL: "http://localhost:1234/v1", Icon: "🖥️", Color: "#10b981"},
	{ID: "together", Name: "Together AI", BaseURL: "https://api.together.xyz/v1", Icon: "🤝", Color: "#6366f1"},
	{ID: "mistral", Name: "Mistral", BaseURL: "https://api.mistral.ai/v1", Icon: "🌫️", Color: "#eb6f4c"},
	{ID: "fireworks", Name: "Fireworks AI", BaseURL: "https://api.fireworks.ai/inference/v1", Icon: "🎆", Color: "#f43f5e"},
}

// GetCommonProviders 获取常用 Provider 列表（用于 UI 选择）
func GetCommonProviders() []Provider {
	providers := make([]Provider, len(CommonProviderConfigs))
	for i, cfg := range CommonProviderConfigs {
		providers[i] = Provider{
			ID:       cfg.ID,
			Name:     cfg.Name,
			Type:     ProviderTypeAPI,
			BaseURL:  cfg.BaseURL,
			Icon:     cfg.Icon,
			Color:    cfg.Color,
			Priority: 90,
		}
	}
	return providers
}
