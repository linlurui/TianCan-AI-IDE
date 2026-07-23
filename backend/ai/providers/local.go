package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// LocalDiscoverer 发现本地运行的 AI 服务 (LM Studio / Ollama)
type LocalDiscoverer struct{}

// Name 返回发现器名称
func (d *LocalDiscoverer) Name() string {
	return "Local Providers"
}

// Priority 返回优先级
func (d *LocalDiscoverer) Priority() int {
	return 20
}

// Discover 发现所有本地 Provider
func (d *LocalDiscoverer) Discover() (*Provider, error) {
	var providers []*Provider

	// 1. 检测 LM Studio
	if p := d.discoverLMStudio(); p != nil {
		providers = append(providers, p)
	}

	// 2. 检测 Ollama
	if p := d.discoverOllama(); p != nil {
		providers = append(providers, p)
	}

	// 3. 检测 Jan
	if p := d.discoverJan(); p != nil {
		providers = append(providers, p)
	}

	// 4. 检测 LocalAI
	if p := d.discoverLocalAI(); p != nil {
		providers = append(providers, p)
	}

	// 如果没有任何本地 Provider，返回 nil
	if len(providers) == 0 {
		return nil, fmt.Errorf("no local providers found")
	}

	// 返回第一个可用的本地 Provider
	// 优先级: LM Studio > Ollama > Jan > LocalAI
	return providers[0], nil
}

// discoverLMStudio 检测 LM Studio
func (d *LocalDiscoverer) discoverLMStudio() *Provider {
	// 检查 LM Studio 服务器是否运行
	urls := []string{
		"http://127.0.0.1:1234/v1/models",
		"http://localhost:1234/v1/models",
	}

	client := &http.Client{Timeout: 2 * time.Second}

	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return &Provider{
				ID:       "lm-studio",
				Name:     "LM Studio",
				Type:     ProviderTypeLocal,
				Status:   ProviderStatusAvailable,
				BaseURL:  "http://127.0.0.1:1234/v1",
				Priority: 20,
				Icon:     "🖥️",
				Color:    "#10b981",
				AuthFile: "localhost:1234",
			}
		}
	}

	// 检查 LM Studio 是否已安装
	if d.isLMStudioInstalled() {
		return &Provider{
			ID:       "lm-studio",
			Name:     "LM Studio",
			Type:     ProviderTypeLocal,
			Status:   ProviderStatusUnavailable,
			BaseURL:  "http://127.0.0.1:1234/v1",
			Priority: 20,
			Icon:     "🖥️",
			Color:    "#10b981",
			Error:    "LM Studio installed but server not running",
		}
	}

	return nil
}

// isLMStudioInstalled 检查 LM Studio 是否安装
func (d *LocalDiscoverer) isLMStudioInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		paths := []string{
			"/Applications/LM Studio.app",
			filepath.Join(os.Getenv("HOME"), "Applications", "LM Studio.app"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	case "windows":
		paths := []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "LM Studio", "LM Studio.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "LM Studio", "LM Studio.exe"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	case "linux":
		// 检查常见安装位置
		paths := []string{
			"/usr/bin/lm-studio",
			"/usr/local/bin/lm-studio",
			filepath.Join(os.Getenv("HOME"), ".local", "bin", "lm-studio"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}

// discoverOllama 检测 Ollama
func (d *LocalDiscoverer) discoverOllama() *Provider {
	// 检查 Ollama 服务器
	urls := []string{
		"http://127.0.0.1:11434/api/tags",
		"http://localhost:11434/api/tags",
	}

	client := &http.Client{Timeout: 2 * time.Second}

	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return &Provider{
				ID:       "ollama",
				Name:     "Ollama",
				Type:     ProviderTypeLocal,
				Status:   ProviderStatusAvailable,
				BaseURL:  "http://127.0.0.1:11434",
				Priority: 30,
				Icon:     "🦙",
				Color:    "#f97316",
				AuthFile: "localhost:11434",
			}
		}
	}

	// 检查 Ollama 是否已安装
	if d.isOllamaInstalled() {
		return &Provider{
			ID:       "ollama",
			Name:     "Ollama",
			Type:     ProviderTypeLocal,
			Status:   ProviderStatusUnavailable,
			BaseURL:  "http://127.0.0.1:11434",
			Priority: 30,
			Icon:     "🦙",
			Color:    "#f97316",
			Error:    "Ollama installed but service not running",
		}
	}

	return nil
}

// isOllamaInstalled 检查 Ollama 是否安装
func (d *LocalDiscoverer) isOllamaInstalled() bool {
	cmd := exec.Command("ollama", "list")
	if err := cmd.Run(); err == nil {
		return true
	}
	return false
}

// discoverJan 检测 Jan
func (d *LocalDiscoverer) discoverJan() *Provider {
	// Jan 默认端口 1337
	urls := []string{
		"http://127.0.0.1:1337/v1/models",
		"http://localhost:1337/v1/models",
	}

	client := &http.Client{Timeout: 2 * time.Second}

	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return &Provider{
				ID:       "jan",
				Name:     "Jan",
				Type:     ProviderTypeLocal,
				Status:   ProviderStatusAvailable,
				BaseURL:  "http://127.0.0.1:1337/v1",
				Priority: 40,
				Icon:     "📦",
				Color:    "#8b5cf6",
				AuthFile: "localhost:1337",
			}
		}
	}

	return nil
}

// discoverLocalAI 检测 LocalAI
func (d *LocalDiscoverer) discoverLocalAI() *Provider {
	// LocalAI 默认端口 8080
	urls := []string{
		"http://127.0.0.1:8080/v1/models",
		"http://localhost:8080/v1/models",
	}

	client := &http.Client{Timeout: 2 * time.Second}

	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return &Provider{
				ID:       "local-ai",
				Name:     "LocalAI",
				Type:     ProviderTypeLocal,
				Status:   ProviderStatusAvailable,
				BaseURL:  "http://127.0.0.1:8080/v1",
				Priority: 50,
				Icon:     "🤖",
				Color:    "#06b6d4",
				AuthFile: "localhost:8080",
			}
		}
	}

	return nil
}

// Test 测试本地 Provider
func (d *LocalDiscoverer) Test(p *Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("no base URL")
	}

	// 根据 Provider 类型选择测试端点
	var testURL string
	switch p.ID {
	case "lm-studio", "jan", "local-ai":
		testURL = p.BaseURL + "/models"
	case "ollama":
		testURL = p.BaseURL + "/api/tags"
	default:
		testURL = p.BaseURL + "/v1/models"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
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

	// 如果是 Ollama，尝试获取模型列表
	if p.ID == "ollama" {
		var result struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
			if len(result.Models) > 0 {
				p.Model = result.Models[0].Name
			}
		}
	}

	return nil
}

// ListLocalProviders 列出所有本地 Provider（不注册到全局 Registry）
func ListLocalProviders() []*Provider {
	var result []*Provider

	providers := []struct {
		id   string
		name string
		url  string
		icon string
		color string
	}{
		{"lm-studio", "LM Studio", "http://127.0.0.1:1234", "🖥️", "#10b981"},
		{"ollama", "Ollama", "http://127.0.0.1:11434", "🦙", "#f97316"},
		{"jan", "Jan", "http://127.0.0.1:1337", "📦", "#8b5cf6"},
		{"local-ai", "LocalAI", "http://127.0.0.1:8080", "🤖", "#06b6d4"},
	}

	client := &http.Client{Timeout: 2 * time.Second}

	for _, p := range providers {
		var testURL string
		switch p.id {
		case "lm-studio", "jan", "local-ai":
			testURL = p.url + "/v1/models"
		case "ollama":
			testURL = p.url + "/api/tags"
		}

		resp, err := client.Get(testURL)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			result = append(result, &Provider{
				ID:       p.id,
				Name:     p.name,
				Type:     ProviderTypeLocal,
				Status:   ProviderStatusAvailable,
				BaseURL:  p.url + "/v1",
				Priority: 20,
				Icon:     p.icon,
				Color:    p.color,
			})
		}
	}

	return result
}
