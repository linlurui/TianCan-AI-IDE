package providers

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Windows credential storage helper
// Uses Windows Credential Manager API to store/retrieve credentials

// WindowsClaudeDesktopDiscoverer Windows 平台的 Claude Desktop 发现器
type WindowsClaudeDesktopDiscoverer struct{}

// Name 返回发现器名称
func (d *WindowsClaudeDesktopDiscoverer) Name() string {
	return "Claude Desktop (Windows)"
}

// Priority 返回优先级
func (d *WindowsClaudeDesktopDiscoverer) Priority() int {
	return 5
}

// Discover 发现 Windows 上的 Claude Desktop
func (d *WindowsClaudeDesktopDiscoverer) Discover() (*Provider, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("not a Windows system")
	}

	// 1. 尝试从 Claude Desktop 安装目录读取配置
	p := d.tryFindFromInstall()

	// 2. 尝试从 %APPDATA%\Claude 读取
	if p == nil {
		p = d.tryFindFromAppData()
	}

	// 3. 尝试从 Windows Credential Manager 读取
	if p == nil {
		p = d.tryFindFromCredentialManager()
	}

	if p == nil {
		return nil, fmt.Errorf("Claude Desktop credentials not found on Windows")
	}

	return p, nil
}

// tryFindFromInstall 尝试从 Claude Desktop 安装目录查找
func (d *WindowsClaudeDesktopDiscoverer) tryFindFromInstall() *Provider {
	// 常见安装位置
	installPaths := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Claude", "Claude.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Claude", "Claude.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Claude", "Claude.exe"),
	}

	var installDir string
	for _, p := range installPaths {
		if _, err := os.Stat(p); err == nil {
			installDir = filepath.Dir(p)
			break
		}
	}

	if installDir == "" {
		return nil
	}

	// 尝试读取配置文件
	configPaths := []string{
		filepath.Join(installDir, "config.json"),
		filepath.Join(installDir, "settings.json"),
		filepath.Join(os.Getenv("APPDATA"), "Claude", "config.json"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Claude", "config.json"),
	}

	for _, configPath := range configPaths {
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		var config map[string]interface{}
		if err := json.Unmarshal(data, &config); err != nil {
			continue
		}

		// 检查 API key
		apiKey, _ := config["api_key"].(string)
		if apiKey == "" {
			if v, ok := config["apiKey"].(string); ok {
				apiKey = v
			}
		}

		if apiKey != "" {
			baseURL, _ := config["base_url"].(string)
			if baseURL == "" {
				baseURL = "https://api.anthropic.com"
			}

			return &Provider{
				ID:       "claude-desktop",
				Name:     "Claude Desktop",
				Type:     ProviderTypeDesktop,
				Status:   ProviderStatusAvailable,
				BaseURL:  baseURL,
				APIKey:   apiKey,
				Priority: 5,
				Icon:     "🖥️",
				Color:    "#6366f1",
				AuthFile: configPath,
			}
		}
	}

	return nil
}

// tryFindFromAppData 尝试从 %APPDATA% 读取
func (d *WindowsClaudeDesktopDiscoverer) tryFindFromAppData() *Provider {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil
	}

	claudeDir := filepath.Join(appData, "Claude")

	// 检查目录是否存在
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		return nil
	}

	// 尝试读取用户配置
	configPath := filepath.Join(claudeDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// 尝试新版位置
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData != "" {
			// 查找 Claude_* 包目录
			packagesDir := filepath.Join(localAppData, "Packages")
			entries, err := os.ReadDir(packagesDir)
			if err == nil {
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "Claude") {
						claudeDir = filepath.Join(packagesDir, entry.Name(), "LocalCache", "Roaming", "Claude")
						configPath = filepath.Join(claudeDir, "config.json")
						data, err = os.ReadFile(configPath)
						if err == nil {
							break
						}
					}
				}
			}
		}
	}

	if data == nil {
		return nil
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}

	apiKey, _ := config["api_key"].(string)
	if apiKey == "" {
		if v, ok := config["apiKey"].(string); ok {
			apiKey = v
		}
	}

	if apiKey == "" {
		return nil
	}

	baseURL, _ := config["base_url"].(string)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	return &Provider{
		ID:       "claude-desktop",
		Name:     "Claude Desktop",
		Type:     ProviderTypeDesktop,
		Status:   ProviderStatusAvailable,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Priority: 5,
		Icon:     "🖥️",
		Color:    "#6366f1",
		AuthFile: configPath,
	}
}

// tryFindFromCredentialManager 尝试从 Windows Credential Manager 读取
func (d *WindowsClaudeDesktopDiscoverer) tryFindFromCredentialManager() *Provider {
	// 使用 cmdkey 命令列出存储的凭据
	cmd := exec.Command("cmdkey", "/list:Claude*")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	// cmdkey 输出格式：
	// Target: Claude
	// Type: Generic
	// User: sk-xxx
	// Local machine

	// 查找包含 API key 的凭据
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "target:") {
			target := strings.TrimSpace(strings.TrimPrefix(line, "Target:"))
			if strings.Contains(strings.ToLower(target), "claude") {
				// 凭据存在，尝试使用 PowerShell 读取
				return d.readCredentialWithPowerShell(target)
			}
		}
	}

	return nil
}

// readCredentialWithPowerShell 使用 PowerShell 读取凭据
func (d *WindowsClaudeDesktopDiscoverer) readCredentialWithPowerShell(target string) *Provider {
	// PowerShell 脚本读取凭据
	script := fmt.Sprintf(`
		Add-Type -AssemblyName System.Security
		$cred = Get-StoredCredential -Target '%s' -ErrorAction SilentlyContinue
		if ($cred) {
			$cred.GetNetworkCredential().Password
		}
	`, target)

	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	apiKey := strings.TrimSpace(string(output))
	if apiKey == "" || strings.HasPrefix(apiKey, "Get-StoredCredential") {
		// PowerShell 命令不可用，尝试使用 /generic 方式
		return d.readGenericCredential(target)
	}

	return &Provider{
		ID:       "claude-desktop",
		Name:     "Claude Desktop",
		Type:     ProviderTypeDesktop,
		Status:   ProviderStatusAvailable,
		BaseURL:  "https://api.anthropic.com",
		APIKey:   apiKey,
		Priority: 5,
		Icon:     "🖥️",
		Color:    "#6366f1",
	}
}

// readGenericCredential 使用通用方式读取凭据
func (d *WindowsClaudeDesktopDiscoverer) readGenericCredential(target string) *Provider {
	// 使用 Windows API 直接读取
	// 这需要 cgo，这里简化处理
	return nil
}

// Test 测试 Windows Claude Desktop Provider
func (d *WindowsClaudeDesktopDiscoverer) Test(p *Provider) error {
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

// WindowsRegistryHelper Windows 注册表帮助类
type WindowsRegistryHelper struct{}

// ReadClaudeDesktopConfig 从注册表读取 Claude Desktop 配置
func (h *WindowsRegistryHelper) ReadClaudeDesktopConfig() (map[string]string, error) {
	result := make(map[string]string)

	// 尝试从注册表读取
	keyPaths := []string{
		`SOFTWARE\Claude`,
		`SOFTWARE\WOW6432Node\Claude`,
		`HKCU\Software\Claude`,
	}

	for _, keyPath := range keyPaths {
		key, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		defer key.Close()

		// 读取值
		values := []string{"APIKey", "BaseURL", "OrgID"}
		for _, val := range values {
			if v, _, err := key.GetStringValue(val); err == nil {
				result[val] = v
			}
		}

		if len(result) > 0 {
			break
		}
	}

	return result, nil
}

// ReadDPAPICookie 尝试读取 DPAPI 保护的 Cookie
// 注意：完整的 DPAPI 解密需要使用 Windows API，这里提供框架
func ReadDPAPICookie(encryptedB64 string) (string, error) {
	// DPAPI 加密的数据是 base64 编码的
	// 需要使用 Windows CryptUnprotectData API 解密

	// 创建一个临时 PowerShell 脚本来处理
	// 这是一个简化实现
	return "", fmt.Errorf("DPAPI decryption requires Windows API calls. Use API key configuration instead.")
}

// DecodeBase64Cookie 尝试解码 base64 编码的 Cookie
func DecodeBase64Cookie(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid base64 encoding: %w", err)
	}

	// 检查是否是 DPAPI 加密格式
	// DPAPI 加密数据通常以特定字节序列开头
	if len(data) > 20 && bytes.HasPrefix(data, []byte{0x01, 0x00, 0x00, 0x00}) {
		return "", fmt.Errorf("DPAPI encrypted data detected, requires Windows API to decrypt")
	}

	return string(data), nil
}
