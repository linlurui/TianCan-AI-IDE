package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ClaudeCodeDiscoverer 发现 Claude Code 的认证信息
type ClaudeCodeDiscoverer struct{}

// Name 返回发现器名称
func (d *ClaudeCodeDiscoverer) Name() string {
	return "Claude Code"
}

// Priority 返回优先级
func (d *ClaudeCodeDiscoverer) Priority() int {
	return 10
}

// Discover 发现 Claude Code Provider
func (d *ClaudeCodeDiscoverer) Discover() (*Provider, error) {
	// 1. 检查环境变量
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")

	// 如果环境变量配置了，直接使用
	if apiKey != "" {
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		return &Provider{
			ID:       "claude-code",
			Name:     "Claude Code",
			Type:     ProviderTypeCLI,
			Status:   ProviderStatusAvailable,
			BaseURL:  baseURL,
			APIKey:   apiKey,
			Priority: 10,
			Icon:     "🤖",
			Color:    "#d97709",
			AuthFile: "环境变量",
		}, nil
	}

	// 2. 查找 Claude Code 配置目录
	configDir := d.findConfigDir()
	if configDir == "" {
		return nil, fmt.Errorf("Claude Code not found")
	}

	// 3. 读取认证配置
	creds, err := d.readCredentials(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials: %w", err)
	}

	if creds.APIKey == "" && creds.SessionToken == "" {
		return nil, fmt.Errorf("no credentials found in Claude Code config")
	}

	// 4. 构建 Provider
	p := &Provider{
		ID:       "claude-code",
		Name:     "Claude Code",
		Type:     ProviderTypeCLI,
		Status:   ProviderStatusAvailable,
		Priority: 10,
		AuthFile: filepath.Join(configDir, "credentials.json"),
		Icon:     "🤖",
		Color:    "#d97709",
	}

	// 使用 API Key 或从 session token 获取
	if creds.APIKey != "" {
		p.APIKey = creds.APIKey
		p.BaseURL = creds.BaseURL
		if p.BaseURL == "" {
			p.BaseURL = "https://api.anthropic.com"
		}
	} else if creds.SessionToken != "" {
		// Claude Code 使用 session token 方式
		p.APIKey = creds.SessionToken
		p.BaseURL = "https://api.anthropicic.com"
	}

	return p, nil
}

// findConfigDir 查找 Claude Code 配置目录
func (d *ClaudeCodeDiscoverer) findConfigDir() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, ".claude")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "claude")
		}
		home := os.Getenv("USERPROFILE")
		return filepath.Join(home, "AppData", "Roaming", "claude")
	case "linux":
		return filepath.Join(home, ".claude")
	}

	return ""
}

// ClaudeCodeCredentials Claude Code 认证信息
type ClaudeCodeCredentials struct {
	APIKey       string `json:"api_key,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
}

// readCredentials 读取 Claude Code 认证文件
func (d *ClaudeCodeDiscoverer) readCredentials(configDir string) (*ClaudeCodeCredentials, error) {
	// 尝试多种可能的认证文件
	paths := []string{
		filepath.Join(configDir, "credentials.json"),
		filepath.Join(configDir, "settings.json"),
		filepath.Join(configDir, "auth.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var creds ClaudeCodeCredentials
		if err := json.Unmarshal(data, &creds); err != nil {
			continue
		}

		if creds.APIKey != "" || creds.SessionToken != "" {
			return &creds, nil
		}

		// 尝试解析其他格式
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err == nil {
			// 可能存储在不同的 key 下
			if key, ok := raw["ANTHROPIC_API_KEY"].(string); ok && key != "" {
				creds.APIKey = key
			}
			if url, ok := raw["base_url"].(string); ok {
				creds.BaseURL = url
			}
			if token, ok := raw["session_token"].(string); ok && token != "" {
				creds.SessionToken = token
			}
			if key, ok := raw["apiKey"].(string); ok && key != "" {
				creds.APIKey = key
			}
			if key, ok := raw["api_key"].(string); ok && key != "" {
				creds.APIKey = key
			}
		}

		if creds.APIKey != "" || creds.SessionToken != "" {
			return &creds, nil
		}
	}

	return nil, fmt.Errorf("credentials file not found")
}

// Test 测试 Claude Code Provider 可用性
func (d *ClaudeCodeDiscoverer) Test(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	// 尝试调用 models 端点
	testURL := strings.TrimSuffix(p.BaseURL, "/") + "/v1/models"

	cmd := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"-H", "Authorization: Bearer "+p.APIKey,
		testURL)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("curl failed: %w", err)
	}

	code := strings.TrimSpace(string(output))
	if code != "200" {
		return fmt.Errorf("API returned status: %s", code)
	}

	return nil
}
