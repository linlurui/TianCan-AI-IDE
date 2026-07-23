package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ProviderType Provider 类型
type ProviderType string

const (
	ProviderTypeCLI     ProviderType = "cli"
	ProviderTypeDesktop ProviderType = "desktop"
	ProviderTypeAPI     ProviderType = "api"
	ProviderTypeLocal   ProviderType = "local"
)

// ProviderStatus Provider 状态
type ProviderStatus string

const (
	ProviderStatusAvailable   ProviderStatus = "available"
	ProviderStatusUnavailable ProviderStatus = "unavailable"
	ProviderStatusNeedsLogin  ProviderStatus = "needs_login"
	ProviderStatusError       ProviderStatus = "error"
)

// Quota 配额信息
type Quota struct {
	Used      int     `json:"used"`
	Limit     int     `json:"limit"`
	Remaining int     `json:"remaining"`
	ResetAt   string  `json:"resetAt,omitempty"`
	Percent   float64 `json:"percent"` // 使用百分比
}

// Provider 代表一个 AI Provider
type Provider struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        ProviderType   `json:"type"`
	Status      ProviderStatus `json:"status"`
	BaseURL     string         `json:"baseUrl,omitempty"`
	APIKey      string         `json:"-"`
	Model       string         `json:"model,omitempty"`
	Quota       *Quota         `json:"quota,omitempty"`
	Priority    int            `json:"priority"` // 优先级，数字越小优先级越高
	Error       string         `json:"error,omitempty"`
	AuthFile    string         `json:"authFile,omitempty"`     // 认证文件路径
	Icon        string         `json:"icon,omitempty"`         // 图标 emoji
	Color       string         `json:"color,omitempty"`        // 主题色
}

// Discoverer Provider 发现器接口
type Discoverer interface {
	// Name 返回发现器名称
	Name() string
	// Priority 返回优先级
	Priority() int
	// Discover 发现 Provider
	Discover() (*Provider, error)
	// Test 测试 Provider 是否可用
	Test(p *Provider) error
}

// Registry Provider 注册表
type Registry struct {
	mu         sync.RWMutex
	providers  map[string]*Provider
	discoverers []Discoverer
}

// 全局 Registry 实例
var globalRegistry *Registry
var registryOnce sync.Once

// GetRegistry 获取全局 Registry
func GetRegistry() *Registry {
	registryOnce.Do(func() {
		globalRegistry = &Registry{
			providers:   make(map[string]*Provider),
			discoverers: []Discoverer{},
		}
		// 注册内置发现器
		globalRegistry.registerDiscoverers()
	})
	return globalRegistry
}

// registerDiscoverers 注册内置发现器
func (r *Registry) registerDiscoverers() {
	r.discoverers = append(r.discoverers,
		&ClaudeCodeDiscoverer{},
		&ClaudeDesktopDiscoverer{},
		&LocalDiscoverer{},
		&KiroDiscoverer{},
		&OpenCodeDiscoverer{},
		&GeminiCLIDiscoverer{},
		&VSCodeCopilotDiscoverer{},
		&CustomAPIDiscoverer{},
	)

	// Windows 特定的发现器（有 cgo 依赖）
	if runtime.GOOS == "windows" {
		// WindowsClaudeDesktopDiscoverer 需要 Windows API，暂时不启用
		// r.discoverers = append(r.discoverers, &WindowsClaudeDesktopDiscoverer{})
	}
}

// Register 注册自定义发现器
func (r *Registry) Register(d Discoverer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discoverers = append(r.discoverers, d)
}

// DiscoverAll 发现所有可用的 Provider
func (r *Registry) DiscoverAll() ([]*Provider, error) {
	r.mu.Lock()
	r.providers = make(map[string]*Provider)
	r.mu.Unlock()

	var found []*Provider
	for _, d := range r.discoverers {
		p, err := d.Discover()
		if err != nil {
			// 记录错误但继续
			fmt.Printf("[providers] %s discover failed: %v\n", d.Name(), err)
			continue
		}
		if p != nil {
			// 测试可用性
			if err := d.Test(p); err != nil {
				p.Status = ProviderStatusUnavailable
				p.Error = err.Error()
			}

			r.mu.Lock()
			r.providers[p.ID] = p
			r.mu.Unlock()
			found = append(found, p)
		}
	}

	return found, nil
}

// List 返回所有已发现的 Provider
func (r *Registry) List() []*Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Provider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

// Get 获取指定 Provider
func (r *Registry) Get(id string) *Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[id]
}

// SetActive 设置活跃 Provider
func (r *Registry) SetActive(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.providers[id]
	if !ok {
		return fmt.Errorf("provider not found: %s", id)
	}
	if p.Status == ProviderStatusUnavailable {
		return fmt.Errorf("provider unavailable: %s", id)
	}

	return nil
}

// TestProvider 测试 Provider 可用性
func (r *Registry) TestProvider(id string) error {
	p := r.Get(id)
	if p == nil {
		return fmt.Errorf("provider not found: %s", id)
	}

	// 根据 Provider 类型选择测试方法
	switch p.Type {
	case ProviderTypeCLI, ProviderTypeDesktop:
		return r.testRemoteProvider(p)
	case ProviderTypeLocal:
		return r.testLocalProvider(p)
	case ProviderTypeAPI:
		return r.testAPIProvider(p)
	}

	return fmt.Errorf("unknown provider type: %s", p.Type)
}

// testRemoteProvider 测试远程 Provider (CLI/Desktop)
func (r *Registry) testRemoteProvider(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL configured")
	}

	// Claude API 测试端点
	testURL := p.BaseURL
	if !strings.HasSuffix(testURL, "/") {
		testURL += "/"
	}
	testURL += "v1/models"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}

	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")

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

// testLocalProvider 测试本地 Provider (LM Studio/Ollama)
func (r *Registry) testLocalProvider(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", p.BaseURL+"/models", nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 5 * time.Second}
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

// testAPIProvider 测试 API Provider
func (r *Registry) testAPIProvider(p *Provider) error {
	return r.testRemoteProvider(p)
}

// AddCustomProvider 添加自定义 Provider
func (r *Registry) AddCustomProvider(cfg CustomProviderConfig) *Provider {
	r.mu.Lock()
	defer r.mu.Unlock()

	p := &Provider{
		ID:       cfg.ID,
		Name:     cfg.Name,
		Type:     ProviderTypeAPI,
		Status:   ProviderStatusAvailable,
		BaseURL:  cfg.BaseURL,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Priority: 100, // 自定义 Provider 优先级较低
		Icon:     "⚙️",
		Color:    "#6366f1",
	}

	r.providers[p.ID] = p
	return p
}

// RemoveCustomProvider 移除自定义 Provider
func (r *Registry) RemoveCustomProvider(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, id)
}

// CustomProviderConfig 自定义 Provider 配置
type CustomProviderConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// ProviderInfo 前端显示用的简化信息
type ProviderInfo struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     ProviderType   `json:"type"`
	Status   ProviderStatus `json:"status"`
	Icon     string         `json:"icon"`
	Color    string         `json:"color"`
	HasQuota bool           `json:"hasQuota"`
	Quota    *Quota         `json:"quota,omitempty"`
}

// ToInfo 转换为前端显示信息
func (p *Provider) ToInfo() *ProviderInfo {
	return &ProviderInfo{
		ID:       p.ID,
		Name:     p.Name,
		Type:     p.Type,
		Status:   p.Status,
		Icon:     p.Icon,
		Color:    p.Color,
		HasQuota: p.Quota != nil,
		Quota:    p.Quota,
	}
}

// MarshalJSON 自定义序列化，排除敏感字段
func (p *Provider) MarshalJSON() ([]byte, error) {
	type Alias Provider
	return json.Marshal(&struct {
		*Alias
		APIKey string `json:"-"`
	}{
		Alias:  (*Alias)(p),
		APIKey: "",
	})
}

// RefreshQuota 获取并更新 Provider 的配额信息
func (r *Registry) RefreshQuota(providerID string) (*Quota, error) {
	p := r.Get(providerID)
	if p == nil {
		return nil, fmt.Errorf("provider not found: %s", providerID)
	}

	var quota *Quota
	var err error

	switch p.Type {
	case ProviderTypeAPI:
		quota, err = r.fetchAPIQuota(p)
	case ProviderTypeCLI, ProviderTypeDesktop:
		quota, err = r.fetchRemoteQuota(p)
	case ProviderTypeLocal:
		quota = &Quota{
			Used:      0,
			Limit:     0,
			Remaining: 0,
			Percent:   0,
		}
	}

	if err != nil {
		return nil, err
	}

	// 更新 Provider 的配额
	p.Quota = quota

	return quota, nil
}

// RefreshAllQuotas 刷新所有 Provider 的配额
func (r *Registry) RefreshAllQuotas() map[string]*Quota {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make(map[string]*Quota)

	for id := range r.providers {
		quota, err := r.RefreshQuota(id)
		if err == nil && quota != nil {
			results[id] = quota
		}
	}

	return results
}

// fetchAPIQuota 从 API Provider 获取配额
func (r *Registry) fetchAPIQuota(p *Provider) (*Quota, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("no base URL")
	}

	// 不同 Provider 有不同的配额查询方式
	// 这里实现通用方式，具体 Provider 可以覆盖
	switch {
	case strings.Contains(p.ID, "deepseek"):
		return r.fetchDeepSeekQuota(p)
	case strings.Contains(p.ID, "groq"):
		return r.fetchGroqQuota(p)
	case strings.Contains(p.ID, "openai"):
		return r.fetchOpenAIQuota(p)
	case strings.Contains(p.ID, "anthropic"):
		return r.fetchAnthropicQuota(p)
	default:
		// 通用方式：尝试调用 /usage 端点
		return r.fetchGenericQuota(p)
	}
}

// fetchRemoteQuota 从远程 Provider 获取配额
func (r *Registry) fetchRemoteQuota(p *Provider) (*Quota, error) {
	// Claude Desktop/Code 通常没有公共的配额 API
	// 这里返回提示信息
	return &Quota{
		Used:      0,
		Limit:     0,
		Remaining: 0,
		Percent:   0,
	}, nil
}

// fetchDeepSeekQuota 获取 DeepSeek 配额
func (r *Registry) fetchDeepSeekQuota(p *Provider) (*Quota, error) {
	// DeepSeek API 没有公开的配额查询端点
	// 返回估计值
	return &Quota{
		Used:      0,
		Limit:     0,
		Remaining: 0,
		Percent:   0,
	}, nil
}

// fetchGroqQuota 获取 Groq 配额
func (r *Registry) fetchGroqQuota(p *Provider) (*Quota, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("no API key")
	}

	// Groq 有公开的速率限制 API
	url := "https://api.groq.com/openai/v1/usage"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}

	var result struct {
		Usage struct {
			Used int `json:"used"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	// Groq 免费额度
	limit := 14400 // RPU per minute (近似)
	remaining := limit - result.Usage.Used
	if remaining < 0 {
		remaining = 0
	}

	return &Quota{
		Used:      result.Usage.Used,
		Limit:     limit,
		Remaining: remaining,
		Percent:   float64(result.Usage.Used) / float64(limit) * 100,
	}, nil
}

// fetchOpenAIQuota 获取 OpenAI 配额
func (r *Registry) fetchOpenAIQuota(p *Provider) (*Quota, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("no API key")
	}

	// OpenAI 使用组织级别的配额
	url := "https://api.openai.com/v1/organization"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}

	// OpenAI API 不直接返回配额，这里返回结构
	return &Quota{
		Used:      0,
		Limit:     0,
		Remaining: 0,
		Percent:   0,
	}, nil
}

// fetchAnthropicQuota 获取 Anthropic 配额
func (r *Registry) fetchAnthropicQuota(p *Provider) (*Quota, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("no API key")
	}

	// Anthropic API 速率限制响应头包含配额信息
	// 发送测试请求获取
	url := strings.TrimSuffix(p.BaseURL, "/") + "/v1/messages"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reqBody := map[string]interface{}{
		"model":            "claude-3-5-sonnet-20241022",
		"max_tokens":        1,
		"messages":          []map[string]string{{"role": "user", "content": "ping"}},
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Anthropic 在响应头中包含速率限制信息
	// 注意：这只是一个测试请求，实际使用时需要处理
	limitHeader := resp.Header.Get("x-ratelimit-limit-tokens")
	remainingHeader := resp.Header.Get("x-ratelimit-remaining-tokens")

	var limit, remaining int
	fmt.Sscanf(limitHeader, "%d", &limit)
	fmt.Sscanf(remainingHeader, "%d", &remaining)

	used := limit - remaining
	if limit > 0 {
		return &Quota{
			Used:      used,
			Limit:     limit,
			Remaining: remaining,
			Percent:   float64(used) / float64(limit) * 100,
		}, nil
	}

	return &Quota{
		Used:      0,
		Limit:     0,
		Remaining: 0,
		Percent:   0,
	}, nil
}

// fetchGenericQuota 通用配额获取
func (r *Registry) fetchGenericQuota(p *Provider) (*Quota, error) {
	if p.APIKey == "" || p.BaseURL == "" {
		return nil, fmt.Errorf("missing API key or base URL")
	}

	// 尝试常见的配额查询端点
	endpoints := []string{
		"/v1/usage",
		"/v1/quota",
		"/v1/account",
		"/v1/billing",
	}

	for _, ep := range endpoints {
		url := strings.TrimSuffix(p.BaseURL, "/") + ep

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+p.APIKey)

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			// 解析响应获取配额信息
			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
				// 尝试提取 usage 信息
				if usage, ok := result["usage"].(map[string]interface{}); ok {
					used := 0
					limit := 0
					if u, ok := usage["used"].(float64); ok {
						used = int(u)
					}
					if l, ok := usage["limit"].(float64); ok {
						limit = int(l)
					}
					if limit > 0 {
						return &Quota{
							Used:      used,
							Limit:     limit,
							Remaining: limit - used,
							Percent:   float64(used) / float64(limit) * 100,
						}, nil
					}
				}
			}
		}
	}

	return &Quota{
		Used:      0,
		Limit:     0,
		Remaining: 0,
		Percent:   0,
	}, nil
}
