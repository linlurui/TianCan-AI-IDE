package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// KiroDiscoverer 发现 Kiro 的认证信息
type KiroDiscoverer struct{}

// Name 返回发现器名称
func (d *KiroDiscoverer) Name() string {
	return "Kiro"
}

// Priority 返回优先级
func (d *KiroDiscoverer) Priority() int {
	return 12
}

// Discover 发现 Kiro Provider
func (d *KiroDiscoverer) Discover() (*Provider, error) {
	switch runtime.GOOS {
	case "darwin":
		return d.discoverMacOS()
	case "windows":
		return d.discoverWindows()
	default:
		return nil, fmt.Errorf("Kiro not supported on %s", runtime.GOOS)
	}
}

// discoverMacOS 从 macOS 发现 Kiro
func (d *KiroDiscoverer) discoverMacOS() (*Provider, error) {
	home, _ := os.UserHomeDir()

	// Kiro 配置目录
	configPaths := []string{
		filepath.Join(home, "Library", "Application Support", "kiro"),
		filepath.Join(home, ".kiro"),
	}

	var configDir string
	for _, p := range configPaths {
		if _, err := os.Stat(p); err == nil {
			configDir = p
			break
		}
	}

	if configDir == "" {
		return nil, fmt.Errorf("Kiro not found")
	}

	// 读取配置
	configPath := filepath.Join(configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Kiro config: %w", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro config: %w", err)
	}

	// 检查 API 配置
	apiKey, _ := config["api_key"].(string)
	awsRegion, _ := config["region"].(string)

	if apiKey == "" {
		// 尝试从环境变量或凭证文件获取
		apiKey = os.Getenv("KIRO_API_KEY")
		if apiKey == "" {
			// 尝试读取凭证文件
			credPath := filepath.Join(configDir, "credentials.json")
			if credData, err := os.ReadFile(credPath); err == nil {
				var creds map[string]interface{}
				if json.Unmarshal(credData, &creds) == nil {
					if key, ok := creds["api_key"].(string); ok {
						apiKey = key
					}
				}
			}
		}
	}

	if apiKey == "" {
		return nil, fmt.Errorf("Kiro API key not found")
	}

	// Kiro 使用 AWS AI21 Labs 兼容 API
	baseURL := "https://api.ai21.com"
	if awsRegion != "" {
		baseURL = fmt.Sprintf("https://api.%s.ai21.com", awsRegion)
	}

	return &Provider{
		ID:       "kiro",
		Name:     "Kiro",
		Type:     ProviderTypeCLI,
		Status:   ProviderStatusAvailable,
		BaseURL:  baseURL + "/v1",
		APIKey:   apiKey,
		Model:    "jamba-1.5-large",
		Priority: 12,
		Icon:     "⚡",
		Color:    "#ff6b35",
		AuthFile: configPath,
	}, nil
}

// discoverWindows 从 Windows 发现 Kiro
func (d *KiroDiscoverer) discoverWindows() (*Provider, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil, fmt.Errorf("APPDATA not set")
	}

	configPath := filepath.Join(appData, "kiro", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("Kiro config not found: %w", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro config: %w", err)
	}

	apiKey, _ := config["api_key"].(string)
	if apiKey == "" {
		apiKey = os.Getenv("KIRO_API_KEY")
	}

	if apiKey == "" {
		return nil, fmt.Errorf("Kiro API key not found")
	}

	awsRegion, _ := config["region"].(string)
	baseURL := "https://api.ai21.com"
	if awsRegion != "" {
		baseURL = fmt.Sprintf("https://api.%s.ai21.com", awsRegion)
	}

	return &Provider{
		ID:       "kiro",
		Name:     "Kiro",
		Type:     ProviderTypeCLI,
		Status:   ProviderStatusAvailable,
		BaseURL:  baseURL + "/v1",
		APIKey:   apiKey,
		Model:    "jamba-1.5-large",
		Priority: 12,
		Icon:     "⚡",
		Color:    "#ff6b35",
		AuthFile: configPath,
	}, nil
}

// Test 测试 Kiro Provider
func (d *KiroDiscoverer) Test(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	testURL := strings.TrimSuffix(p.BaseURL, "/") + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

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

// OpenCodeDiscoverer 发现 OpenCode 的认证信息
type OpenCodeDiscoverer struct{}

// Name 返回发现器名称
func (d *OpenCodeDiscoverer) Name() string {
	return "OpenCode"
}

// Priority 返回优先级
func (d *OpenCodeDiscoverer) Priority() int {
	return 15
}

// Discover 发现 OpenCode Provider
func (d *OpenCodeDiscoverer) Discover() (*Provider, error) {
	home, _ := os.UserHomeDir()

	var configPaths []string
	switch runtime.GOOS {
	case "darwin", "linux":
		configPaths = []string{
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(home, ".opencode"),
		}
	case "windows":
		configPaths = []string{
			filepath.Join(os.Getenv("APPDATA"), "opencode"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "opencode"),
		}
	}

	for _, configPath := range configPaths {
		// 尝试读取配置文件
		settingsPath := filepath.Join(configPath, "settings.json")
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			continue
		}

		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err != nil {
			continue
		}

		// 检查 API 配置
		apiKey, _ := settings["api_key"].(string)
		baseURL, _ := settings["base_url"].(string)

		if apiKey != "" {
			if baseURL == "" {
				baseURL = "https://api.opencode.ai/v1"
			}
			return &Provider{
				ID:       "opencode",
				Name:     "OpenCode",
				Type:     ProviderTypeCLI,
				Status:   ProviderStatusAvailable,
				BaseURL:  baseURL,
				APIKey:   apiKey,
				Model:    "opencode-model",
				Priority: 15,
				Icon:     "🔓",
				Color:    "#22c55e",
				AuthFile: settingsPath,
			}, nil
		}
	}

	return nil, fmt.Errorf("OpenCode not found")
}

// Test 测试 OpenCode Provider
func (d *OpenCodeDiscoverer) Test(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	testURL := strings.TrimSuffix(p.BaseURL, "/") + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

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

// VSCodeCopilotDiscoverer 发现 VS Code Copilot
type VSCodeCopilotDiscoverer struct{}

// Name 返回发现器名称
func (d *VSCodeCopilotDiscoverer) Name() string {
	return "VS Code Copilot"
}

// Priority 返回优先级
func (d *VSCodeCopilotDiscoverer) Priority() int {
	return 25
}

// Discover 发现 VS Code Copilot Provider
func (d *VSCodeCopilotDiscoverer) Discover() (*Provider, error) {
	// VS Code Copilot 使用 GitHub Copilot 授权
	// 需要读取 VS Code 的认证存储
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		// 尝试从 VS Code 认证存储读取
		copilotDir := filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "github.copilot")
		if data, err := os.ReadFile(filepath.Join(copilotDir, "config.json")); err == nil {
			var config map[string]interface{}
			if json.Unmarshal(data, &config) == nil {
				if apiKey, ok := config["apiKey"].(string); ok && apiKey != "" {
					return &Provider{
						ID:      "vscode-copilot",
						Name:    "VS Code Copilot",
						Type:    ProviderTypeCLI,
						Status:  ProviderStatusAvailable,
						APIKey:  apiKey,
						Model:   "gpt-4",
						Icon:    "🔷",
						Color:   "#007acc",
					}, nil
				}
			}
		}
	case "windows":
		copilotDir := filepath.Join(os.Getenv("APPDATA"), "Code", "User", "globalStorage", "github.copilot")
		if data, err := os.ReadFile(filepath.Join(copilotDir, "config.json")); err == nil {
			var config map[string]interface{}
			if json.Unmarshal(data, &config) == nil {
				if apiKey, ok := config["apiKey"].(string); ok && apiKey != "" {
					return &Provider{
						ID:      "vscode-copilot",
						Name:    "VS Code Copilot",
						Type:    ProviderTypeCLI,
						Status:  ProviderStatusAvailable,
						APIKey:  apiKey,
						Model:   "gpt-4",
						Icon:    "🔷",
						Color:   "#007acc",
					}, nil
				}
			}
		}
	}

	// Copilot 授权信息通常存储在特定目录
	// 这里简化处理，提示用户使用 GitHub API Key
	return nil, fmt.Errorf("VS Code Copilot requires manual GitHub authentication. Use GitHub Models API instead.")
}

// Test 测试 VS Code Copilot Provider
func (d *VSCodeCopilotDiscoverer) Test(p *Provider) error {
	return fmt.Errorf("not implemented")
}

// GeminiCLIDiscoverer 发现 Gemini CLI
type GeminiCLIDiscoverer struct{}

// Name 返回发现器名称
func (d *GeminiCLIDiscoverer) Name() string {
	return "Gemini CLI"
}

// Priority 返回优先级
func (d *GeminiCLIDiscoverer) Priority() int {
	return 18
}

// Discover 发现 Gemini CLI Provider
func (d *GeminiCLIDiscoverer) Discover() (*Provider, error) {
	home, _ := os.UserHomeDir()

	// Gemini CLI 使用 OAuth 认证，凭证存储在 ~/.gemini/
	configPath := filepath.Join(home, ".gemini", "oauth_creds.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("Gemini CLI not found or not authenticated")
	}

	var creds map[string]interface{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini CLI credentials")
	}

	// Gemini CLI 使用 Google AI API
	return &Provider{
		ID:       "gemini-cli",
		Name:     "Gemini CLI",
		Type:     ProviderTypeCLI,
		Status:   ProviderStatusAvailable,
		BaseURL:  "https://generativelanguage.googleapis.com/v1beta",
		Priority: 18,
		Icon:    "💎",
		Color:    "#4285f4",
		AuthFile: configPath,
	}, nil
}

// Test 测试 Gemini CLI Provider
func (d *GeminiCLIDiscoverer) Test(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	testURL := p.BaseURL + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	// Google API 可能返回 401 但仍说明服务可达
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("status: %d", resp.StatusCode)
	}

	return nil
}
