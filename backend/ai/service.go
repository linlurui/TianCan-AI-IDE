package ai

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"log"

	"github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
	"github.com/linlurui/tiancan-ai-ide/backend/events"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/safety"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/tools"
	agenttypes "github.com/linlurui/tiancan-ai-ide/backend/agent/types"
	aiprovider "github.com/linlurui/tiancan-ai-ide/backend/ai/providers"
	"github.com/linlurui/tiancan-ai-ide/backend/memory"
	"github.com/linlurui/tiancan-ai-ide/backend/router"
	"github.com/linlurui/tiancan-ai-ide/backend/sandbox"
	"github.com/linlurui/tiancan-ai-ide/backend/skills"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/browser"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/parser"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/providers"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/tokenstore"
	"github.com/linlurui/tiancan-ai-ide/backend/webai/types"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/wailsapp/wails/v3/pkg/application"
	_ "modernc.org/sqlite"
)

// APIProviderConfig stores credentials for a remote API provider.
type APIProviderConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// Service manages LM Studio integration and AI chat/agent capabilities.
type Service struct {
	mu             sync.Mutex
	apiURL         string // e.g. http://127.0.0.1:1234/v1
	model          string
	running        bool
	rootPathVal    string              // current project root path
	provider       APIProviderConfig   // active remote API provider
	webAuth        *types.AuthResult   // active web provider auth
	webID          string              // active web provider ID
	webConvID      string              // persisted conversation_id for web providers
	webLastRequest string              // last user request for follow-up context
	webLastActions string              // last turn actions+results summary for follow-up context
	App            *application.App    // set by main.go; used for Events.Emit（G3 起为 sink 的兜底桥）
	sink           events.Sink         // G3 ProgressSink：非空时优先于 App
	Router         *router.SmartRouter // hardware-aware model tier routing
	sandboxMgr     *sandbox.Manager    // persistent sandbox manager (shared state)
	requestCtx     context.Context
	requestCancel  context.CancelFunc
	requestID      uint64
	// Provider management
	providerRegistry *aiprovider.Registry
	activeProvider   string // ID of active provider
	// Agent mode
}

// SetSink 注入 G3 ProgressSink（PC 端/测试用）；传 nil 恢复 App 桥接。
func (s *Service) SetSink(sink events.Sink) { s.sink = sink }

// hasEmitter 报告当前是否有可用事件出口（Sink 或 Wails App 桥）。
func (s *Service) hasEmitter() bool { return s.sink != nil || s.App != nil }

// emit 发射事件：优先注入的 Sink，其次 Wails App 桥，最后包级默认（静默）。
func (s *Service) emit(event string, payload interface{}) {
	if s.sink != nil {
		s.sink.Emit(event, payload)
		return
	}
	if s.App != nil {
		s.App.Event.Emit(event, payload)
		return
	}
	events.Default().Emit(event, payload)
}

func (s *Service) beginRequest() (context.Context, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestCancel != nil {
		s.requestCancel()
	}
	s.requestID++
	ctx, cancel := context.WithCancel(context.Background())
	s.requestCtx = ctx
	s.requestCancel = cancel
	return ctx, s.requestID
}

func (s *Service) endRequest(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestID != id {
		return
	}
	if s.requestCancel != nil {
		s.requestCancel()
	}
	s.requestCtx = nil
	s.requestCancel = nil
}

func (s *Service) currentRequestContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestCtx != nil {
		return s.requestCtx
	}
	return context.Background()
}

// StopAgentRequest cancels the active model request and any attached CLI process.
func (s *Service) StopAgentRequest() bool {
	s.mu.Lock()
	cancel := s.requestCancel
	s.requestID++
	s.requestCtx = nil
	s.requestCancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// NewService creates a new AI service.
func NewService() *Service {
	s := &Service{
		apiURL:           "http://127.0.0.1:1234/v1",
		model:            "default",
		sandboxMgr:       sandbox.NewManager(),
		providerRegistry: aiprovider.GetRegistry(),
		activeProvider:   "lm-studio",
	}
	s.Router = router.NewSmartRouter(router.DefaultConfig())
	return s
}

// ── LM Studio lifecycle ──────────────────────────────────────────

// managedLMStudioPath returns the path where we cache LM Studio binary.
func managedLMStudioPath() string {
	return filepath.Join(envconfig.DataHome(), "bin", "lm-studio")
}

// IsLMStudioInstalled reports whether LM Studio is available (running or installed).
func (s *Service) IsLMStudioInstalled() bool {
	// 1. Check if LM Studio server is already running
	if s.isServerRunning() {
		return true
	}
	// 2. Check if lm-studio binary exists
	if p, err := exec.LookPath("lm-studio"); err == nil {
		_ = p
		return true
	}
	// 3. Check managed path
	if fi, err := os.Stat(managedLMStudioPath()); err == nil && !fi.IsDir() {
		return true
	}
	// 4. Check common install locations
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		for _, p := range []string{
			"/Applications/LM Studio.app",
			filepath.Join(home, "Applications", "LM Studio.app"),
		} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	case "windows":
		localApp := os.Getenv("LOCALAPPDATA")
		progFiles := os.Getenv("ProgramFiles")
		for _, p := range []string{
			filepath.Join(localApp, "LM Studio", "LM Studio.exe"),
			filepath.Join(progFiles, "LM Studio", "LM Studio.exe"),
		} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}

// isServerRunning checks if the LM Studio server is responding.
func (s *Service) isServerRunning() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(s.apiURL + "/models")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// InstallLMStudio downloads and installs LM Studio.
// Returns log lines as the installation progresses.
func (s *Service) InstallLMStudio() ([]string, error) {
	// Pre-check: already installed?
	if s.IsLMStudioInstalled() {
		return []string{"✓ LM Studio 已安装，无需重复安装"}, nil
	}

	var logs []string

	if runtime.GOOS == "darwin" {
		// macOS: download .dmg, mount, copy .app, unmount
		logs = append(logs, "正在下载 LM Studio for macOS...")
		url := "https://releases.lmstudio.ai/mac/latest/LM%20Studio.dmg"
		tmpDmg := filepath.Join(os.TempDir(), "lm-studio.dmg")

		data, src, err := fetchFastest([]string{
			"https://ghproxy.com/" + url,
			"https://gh-proxy.com/" + url,
			url,
		})
		if err != nil {
			return logs, fmt.Errorf("下载 LM Studio 失败: %w", err)
		}
		logs = append(logs, fmt.Sprintf("已从 %s 下载 (%d bytes)", src, len(data)))

		if err := os.WriteFile(tmpDmg, data, 0o644); err != nil {
			return logs, fmt.Errorf("写入 dmg 失败: %w", err)
		}

		// Mount
		logs = append(logs, "正在挂载 DMG...")
		cmd := exec.Command("hdiutil", "attach", tmpDmg, "-nobrowse", "-quiet")
		if out, err := cmd.CombinedOutput(); err != nil {
			return logs, fmt.Errorf("挂载 DMG 失败: %s: %w", string(out), err)
		}

		// Copy .app
		logs = append(logs, "正在安装 LM Studio.app...")
		cmd = exec.Command("cp", "-R", "/Volumes/LM Studio/LM Studio.app", "/Applications/LM Studio.app")
		if out, err := cmd.CombinedOutput(); err != nil {
			return logs, fmt.Errorf("复制 .app 失败: %s: %w", string(out), err)
		}

		// Unmount
		exec.Command("hdiutil", "detach", "/Volumes/LM Studio", "-quiet").Run()
		os.Remove(tmpDmg)

		logs = append(logs, "✓ LM Studio 已安装到 /Applications/LM Studio.app")
		return logs, nil
	}

	if runtime.GOOS == "windows" {
		logs = append(logs, "正在下载 LM Studio for Windows...")
		url := "https://releases.lmstudio.ai/win/latest/LM-Studio-Setup.exe"
		tmpExe := filepath.Join(os.TempDir(), "LM-Studio-Setup.exe")

		data, src, err := fetchFastest([]string{
			"https://ghproxy.com/" + url,
			"https://gh-proxy.com/" + url,
			url,
		})
		if err != nil {
			return logs, fmt.Errorf("下载 LM Studio 失败: %w", err)
		}
		logs = append(logs, fmt.Sprintf("已从 %s 下载 (%d bytes)", src, len(data)))

		if err := os.WriteFile(tmpExe, data, 0o644); err != nil {
			return logs, fmt.Errorf("写入安装包失败: %w", err)
		}

		logs = append(logs, "正在运行安装程序...")
		cmd := exec.Command(tmpExe, "/S") // silent install
		cmd.SysProcAttr = hideWindowAttr()
		if err := cmd.Run(); err != nil {
			return logs, fmt.Errorf("安装失败: %w", err)
		}

		logs = append(logs, "✓ LM Studio 已安装")
		return logs, nil
	}

	// Linux: AppImage
	logs = append(logs, "正在下载 LM Studio for Linux...")
	url := "https://releases.lmstudio.ai/linux/latest/LM_Studio.AppImage"
	dest := managedLMStudioPath()

	data, src, err := fetchFastest([]string{
		"https://ghproxy.com/" + url,
		"https://gh-proxy.com/" + url,
		url,
	})
	if err != nil {
		return logs, fmt.Errorf("下载 LM Studio 失败: %w", err)
	}
	logs = append(logs, fmt.Sprintf("已从 %s 下载 (%d bytes)", src, len(data)))

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return logs, fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		return logs, fmt.Errorf("写入 AppImage 失败: %w", err)
	}

	logs = append(logs, "✓ LM Studio 已安装到 "+dest)
	return logs, nil
}

// LaunchLMStudio starts the LM Studio application.
func (s *Service) LaunchLMStudio() error {
	if s.isServerRunning() {
		return nil // already running
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-a", "LM Studio")
	case "windows":
		// Try common install paths
		candidates := []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "LM Studio", "LM Studio.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "LM Studio", "LM Studio.exe"),
			"LM Studio.exe",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				cmd = exec.Command(c)
				break
			}
		}
		if cmd == nil {
			cmd = exec.Command(filepath.Join(os.Getenv("LOCALAPPDATA"), "LM Studio", "LM Studio.exe"))
		}
	default:
		cmd = exec.Command(managedLMStudioPath())
	}

	cmd.SysProcAttr = setPgidAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 LM Studio 失败: %w", err)
	}

	// Wait for server to become available (up to 60s)
	log.Printf("ai: waiting for LM Studio server to start...")
	for i := 0; i < 60; i++ {
		time.Sleep(1 * time.Second)
		if s.isServerRunning() {
			log.Printf("ai: LM Studio server is ready")
			return nil
		}
	}
	return fmt.Errorf("LM Studio 启动超时（60秒），请确认 LM Studio 已打开并启用了本地服务器")
}

// ── Server status & model management ──────────────────────────────

// ModelInfo describes a model loaded in LM Studio.
type ModelInfo struct {
	ID            string  `json:"id"`
	Object        string  `json:"object"`
	SizeGB        float64 `json:"size_gb,omitempty"`        // Model file size in GB (estimated from ID heuristics or API)
	ParamsB       float64 `json:"params_b,omitempty"`       // Parameter count in billions (e.g. 7.0 for 7B)
	MinRAMGB      float64 `json:"min_ram_gb,omitempty"`     // Minimum system RAM required in GB
	MinVRAMGB     float64 `json:"min_vram_gb,omitempty"`    // Minimum GPU VRAM required in GB (0 = CPU-only OK)
	Available     bool    `json:"available"`                // Whether this model can run on current hardware
	DisableReason string  `json:"disable_reason,omitempty"` // Why model is unavailable
}

// ListModels returns the list of loaded models in LM Studio.
func (s *Service) ListModels() ([]ModelInfo, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(s.apiURL + "/models")
	if err != nil {
		return nil, fmt.Errorf("LM Studio 服务器未响应: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}

	// Enrich models with hardware requirements and availability
	s.enrichModelHardwareInfo(result.Data)
	return result.Data, nil
}

// ListModelsAvailable returns models with hardware availability status.
// Models that cannot run on current hardware are marked Available=false with a reason.
// If ALL models are unavailable, it automatically stops LM Studio to free resources.
func (s *Service) ListModelsAvailable() ([]ModelInfo, error) {
	models, err := s.ListModels()
	if err != nil {
		return nil, err
	}

	allUnavailable := true
	for i := range models {
		if models[i].Available {
			allUnavailable = false
		}
	}

	// If no local model can run, stop LM Studio to free memory
	if allUnavailable && len(models) > 0 {
		log.Printf("ai: all local models unavailable on current hardware, stopping LM Studio")
		s.StopLMStudio()
	}

	return models, nil
}

// enrichModelHardwareInfo estimates hardware requirements from model ID and checks availability.
func (s *Service) enrichModelHardwareInfo(models []ModelInfo) {
	ramGB, vramGB := s.detectSystemMemory()

	for i := range models {
		m := &models[i]
		m.ParamsB = estimateParamsFromID(m.ID)
		m.SizeGB = estimateSizeFromParams(m.ParamsB)
		m.MinRAMGB = m.SizeGB * 1.3  // 30% overhead for runtime (KV cache, etc.)
		m.MinVRAMGB = m.SizeGB * 0.8 // GPU-offloadable portion

		m.Available = true

		// Check RAM constraint (hard requirement — model won't load without enough RAM)
		if m.MinRAMGB > 0 && ramGB > 0 && m.MinRAMGB > ramGB {
			m.Available = false
			m.DisableReason = fmt.Sprintf("需要 %.0fGB 内存，当前 %.0fGB", m.MinRAMGB, ramGB)
		}

		// If model specifies VRAM requirement and we have GPU info, check it
		// (VRAM is soft — model can fall back to CPU, just slower)
		if m.MinVRAMGB > 0 && vramGB > 0 && m.MinVRAMGB > vramGB && m.Available {
			// Model can still run but will be slow (CPU offload)
			m.DisableReason = fmt.Sprintf("GPU显存不足(需%.0fGB/有%.0fGB)，将使用CPU推理(较慢)", m.MinVRAMGB, vramGB)
			// Don't mark unavailable — just warn. User can still choose it.
		}
	}
}

// detectSystemMemory returns (total RAM GB, total VRAM GB).
func (s *Service) detectSystemMemory() (float64, float64) {
	var ramGB, vramGB float64

	// System RAM via gopsutil
	if vmStat, err := mem.VirtualMemory(); err == nil {
		ramGB = float64(vmStat.Available) / 1024 / 1024 / 1024 // use AVAILABLE, not total
	}

	// GPU VRAM from env (TIANCAN_GPU_MEMORY_GB) — we can't reliably detect this programmatically
	if v := envconfig.EnvFloat("TIANCAN_GPU_MEMORY_GB", 0); v != 0 {
		vramGB = v
	}

	return ramGB, vramGB
}

// estimateParamsFromID extracts parameter count from model ID heuristics.
// e.g. "qwen2.5-7b-instruct" → 7.0, "llama-3-70b" → 70.0
func estimateParamsFromID(id string) float64 {
	lower := strings.ToLower(id)

	// Common patterns: 0.5b, 1.5b, 3b, 7b, 8b, 13b, 14b, 32b, 70b, etc.
	// Try to find a number followed by 'b' (billion)
	for i := 0; i < len(lower)-1; i++ {
		if lower[i] == 'b' && i > 0 {
			// Walk backwards to find the number
			end := i
			start := i - 1
			for start >= 0 && (lower[start] >= '0' && lower[start] <= '9' || lower[start] == '.') {
				start--
			}
			numStr := lower[start+1 : end]
			if v, err := strconv.ParseFloat(numStr, 64); err == nil && v > 0 {
				return v
			}
		}
	}

	// Fallback: estimate from model name patterns
	switch {
	case strings.Contains(lower, "mini") || strings.Contains(lower, "tiny"):
		return 1.0
	case strings.Contains(lower, "small"):
		return 3.0
	case strings.Contains(lower, "medium"):
		return 7.0
	case strings.Contains(lower, "large") || strings.Contains(lower, "xl"):
		return 13.0
	default:
		return 7.0 // assume 7B as default
	}
}

// estimateSizeFromParams estimates model file size in GB from parameter count.
// Rule of thumb: ~1.2GB per billion parameters for Q4_K_M quantization.
func estimateSizeFromParams(paramsB float64) float64 {
	if paramsB <= 0 {
		return 5.0 // default ~5GB
	}
	return paramsB * 1.2
}

// StopLMStudio stops the LM Studio server process to free system resources.
func (s *Service) StopLMStudio() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find LM Studio process and kill it
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "pkill -f 'LM Studio' || pkill -f lm-studio"
	case "windows":
		cmd = "taskkill /F /IM 'LM Studio.exe' 2>nul || taskkill /F /IM lm-studio.exe 2>nul"
	default:
		cmd = "pkill -f lm-studio"
	}

	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		log.Printf("ai: stop LM Studio: %s (%v)", string(out), err)
		return fmt.Errorf("停止 LM Studio 失败: %v", err)
	}

	s.running = false
	log.Printf("ai: LM Studio stopped to free system resources")
	return nil
}

// SetModel sets the active model for chat.
func (s *Service) SetModel(modelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = modelID
}

// SetAPIURL sets the LM Studio API base URL.
func (s *Service) SetAPIURL(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiURL = strings.TrimRight(url, "/")
}

// GetAPIURL returns the current API URL.
func (s *Service) GetAPIURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiURL
}

// SetAPIProvider sets the active remote API provider (name, base URL, key, model).
func (s *Service) SetAPIProvider(name, baseURL, apiKey, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provider = APIProviderConfig{
		Name:    name,
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
	}
}

// GetAPIProvider returns the current API provider config.
func (s *Service) GetAPIProvider() APIProviderConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider
}

// ListAPIProviders returns the built-in list of known API providers.
func (s *Service) ListAPIProviders() []APIProviderConfig {
	return []APIProviderConfig{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o"},
		{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
		{Name: "qwen", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus"},
		{Name: "siliconflow", BaseURL: "https://api.siliconflow.cn/v1", Model: "Qwen/Qwen2.5-Coder-32B-Instruct"},
		{Name: "custom", BaseURL: "", Model: ""},
	}
}

// ── Provider Management ───────────────────────────────────────────

// DiscoverProviders discovers all available AI providers.
func (s *Service) DiscoverProviders() ([]*aiprovider.Provider, error) {
	return s.providerRegistry.DiscoverAll()
}

// ListProviders returns all discovered providers with their status.
func (s *Service) ListProviders() []*aiprovider.ProviderInfo {
	all := s.providerRegistry.List()
	result := make([]*aiprovider.ProviderInfo, 0, len(all))
	for _, p := range all {
		result = append(result, p.ToInfo())
	}
	return result
}

// GetActiveProvider returns the currently active provider.
func (s *Service) GetActiveProvider() *aiprovider.Provider {
	s.mu.Lock()
	defer s.mu.Unlock()

	activeID := s.activeProvider
	if activeID == "" {
		activeID = "lm-studio"
	}
	return s.providerRegistry.Get(activeID)
}

// SetActiveProvider sets the active provider by ID.
func (s *Service) SetActiveProvider(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.providerRegistry.Get(id)
	if p == nil {
		return fmt.Errorf("provider not found: %s", id)
	}

	s.activeProvider = id

	// Update service configuration to match the provider
	s.apiURL = p.BaseURL
	s.model = p.Model

	return nil
}

// TestProvider tests the connectivity of a provider.
func (s *Service) TestProvider(id string) error {
	return s.providerRegistry.TestProvider(id)
}

// AddCustomProvider adds a custom API provider.
func (s *Service) AddCustomProvider(id, name, baseURL, apiKey, model string) error {
	cfg := aiprovider.CustomProviderConfig{
		ID:      id,
		Name:    name,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	}
	p := s.providerRegistry.AddCustomProvider(cfg)
	return s.providerRegistry.TestProvider(p.ID)
}

// RemoveProvider removes a provider.
func (s *Service) RemoveProvider(id string) {
	// Only allow removing custom providers
	if strings.HasPrefix(id, "custom-") {
		s.providerRegistry.RemoveCustomProvider(id)
	}
}

// GetCommonProviders returns the list of common provider templates.
func (s *Service) GetCommonProviders() []aiprovider.Provider {
	return aiprovider.GetCommonProviders()
}

// ChatWithProvider sends a chat request using a specific provider.
func (s *Service) ChatWithProvider(providerID string, messages []ChatMessage) (string, error) {
	p := s.providerRegistry.Get(providerID)
	if p == nil {
		return "", fmt.Errorf("provider not found: %s", providerID)
	}

	if p.BaseURL == "" {
		return "", fmt.Errorf("provider %s has no base URL configured", p.Name)
	}

	reqBody := ChatRequest{
		Model:       p.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   4096,
		Stream:      false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed (%s): %w", p.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned %d (%s): %s", resp.StatusCode, p.Name, string(b))
	}

	var result ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty response from %s", p.Name)
	}

	return result.Choices[0].Message.Content, nil
}

// StreamChatWithProvider sends a streaming chat request using a specific provider.
func (s *Service) StreamChatWithProvider(providerID string, messages []ChatMessage, onToken func(token string)) (string, error) {
	p := s.providerRegistry.Get(providerID)
	if p == nil {
		return "", fmt.Errorf("provider not found: %s", providerID)
	}

	if p.BaseURL == "" {
		return "", fmt.Errorf("provider %s has no base URL configured", p.Name)
	}

	reqBody := ChatRequest{
		Model:       p.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   4096,
		Stream:      true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed (%s): %w", p.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned %d (%s): %s", resp.StatusCode, p.Name, string(b))
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk ChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				full.WriteString(choice.Delta.Content)
				if onToken != nil {
					onToken(choice.Delta.Content)
				}
			}
		}
	}

	return full.String(), nil
}

// ── Chat API (OpenAI-compatible) ──────────────────────────────────

// ChatMessage is a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"` // tool name for tool_call/tool_result
}

// ChatRequest is the request body sent to LM Studio.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
}

// ChatResponse is the non-streaming response from LM Studio.
type ChatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
}

// ChatDelta is a streaming chunk from LM Studio.
type ChatDelta struct {
	Delta struct {
		Content string `json:"content"`
	} `json:"delta"`
}

// ChatStreamChunk wraps a SSE chunk.
type ChatStreamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// ── Smart Router API ───────────────────────────────────────────────

// RouteRequest 请求路由
func (s *Service) RouteRequest(userMessage string) *router.TierRecommendation {
	// 获取各 Provider 的可用性分数
	providerScores := s.getProviderScores()

	// 调用路由
	return s.Router.RouteWithProviders(userMessage, providerScores)
}

// GetHardwareProfile 获取硬件配置
func (s *Service) GetHardwareProfile() *router.HardwareProfile {
	return s.Router.GetHardwareProfile()
}

// GetSystemStatus 获取系统状态
func (s *Service) GetSystemStatus() map[string]interface{} {
	return s.Router.GetSystemStatus()
}

// SetPreferredTier 设置首选层级
func (s *Service) SetPreferredTier(tier string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch router.ModelTier(tier) {
	case router.TierLocal:
		s.Router.SetConfig(router.SmartRouterConfig{PreferredTier: router.TierLocal})
	case router.TierAPI:
		s.Router.SetConfig(router.SmartRouterConfig{PreferredTier: router.TierAPI})
	case router.TierWeb:
		s.Router.SetConfig(router.SmartRouterConfig{PreferredTier: router.TierWeb})
	}
}

// SetAutoDowngrade 设置自动降级
func (s *Service) SetAutoDowngrade(enabled bool) {
	s.Router.SetAutoDowngrade(enabled)
}

// getProviderScores 获取各 Provider 的可用性分数
func (s *Service) getProviderScores() map[string]float64 {
	scores := make(map[string]float64)

	providers := s.providerRegistry.List()
	for _, p := range providers {
		score := 50.0 // 基础分

		switch p.Status {
		case aiprovider.ProviderStatusAvailable:
			score = 100.0
		case aiprovider.ProviderStatusUnavailable:
			score = 0.0
		case aiprovider.ProviderStatusNeedsLogin:
			score = 30.0
		case aiprovider.ProviderStatusError:
			score = 10.0
		}

		// 根据 Provider 类型调整分数
		switch p.Type {
		case aiprovider.ProviderTypeCLI:
			score *= 1.2 // CLI 通常更可靠
		case aiprovider.ProviderTypeDesktop:
			score *= 1.1
		case aiprovider.ProviderTypeLocal:
			// 本地服务需要检查内存
			hw := s.Router.GetHardwareProfile()
			if hw.AvailableRAMGB < 4 {
				score *= 0.5 // 内存不足降低分数
			}
		case aiprovider.ProviderTypeAPI:
			score *= 1.0
		}

		// 根据配额剩余量调整分数
		if p.Quota != nil && p.Quota.Remaining > 0 {
			// 配额紧张时降低分数
			if p.Quota.Percent > 80 {
				score *= 0.5
			} else if p.Quota.Percent > 60 {
				score *= 0.8
			}
		}

		scores[p.ID] = score
	}

	return scores
}

// ── Quota Management ───────────────────────────────────────────────

// GetProviderQuota 获取指定 Provider 的配额
func (s *Service) GetProviderQuota(providerID string) (*aiprovider.Quota, error) {
	return s.providerRegistry.RefreshQuota(providerID)
}

// GetAllQuotas 获取所有 Provider 的配额
func (s *Service) GetAllQuotas() map[string]*aiprovider.Quota {
	return s.providerRegistry.RefreshAllQuotas()
}

// ProviderQuotaInfo 配额信息（用于前端显示）
type ProviderQuotaInfo struct {
	ProviderID string  `json:"providerId"`
	Name       string  `json:"name"`
	HasQuota   bool    `json:"hasQuota"`
	Used       int     `json:"used"`
	Limit      int     `json:"limit"`
	Remaining  int     `json:"remaining"`
	Percent    float64 `json:"percent"`
	Status     string  `json:"status"`
}

// GetProviderQuotaInfo 获取配额信息（包含 Provider 名称）
func (s *Service) GetProviderQuotaInfo() []ProviderQuotaInfo {
	quotas := s.providerRegistry.RefreshAllQuotas()
	providers := s.providerRegistry.List()

	result := make([]ProviderQuotaInfo, 0)

	// 创建一个 map 便于查找
	providerMap := make(map[string]*aiprovider.Provider)
	for _, p := range providers {
		providerMap[p.ID] = p
	}

	// 添加有配额的 Provider
	for id, quota := range quotas {
		p := providerMap[id]
		name := id
		if p != nil {
			name = p.Name
		}

		result = append(result, ProviderQuotaInfo{
			ProviderID: id,
			Name:       name,
			HasQuota:   quota.Limit > 0,
			Used:       quota.Used,
			Limit:      quota.Limit,
			Remaining:  quota.Remaining,
			Percent:    quota.Percent,
			Status:     "ok",
		})
	}

	// 添加没有配额查询结果的 Provider
	for _, p := range providers {
		if _, ok := quotas[p.ID]; !ok {
			result = append(result, ProviderQuotaInfo{
				ProviderID: p.ID,
				Name:       p.Name,
				HasQuota:   false,
				Status:     "no_quota_api",
			})
		}
	}

	return result
}

// RecordFailure 记录 Provider 失败
func (s *Service) RecordFailure(providerID string, errMsg string) {
	// 尝试找到 Provider 对应的层级
	p := s.providerRegistry.Get(providerID)
	if p != nil {
		var tier router.ModelTier
		switch p.Type {
		case aiprovider.ProviderTypeLocal:
			tier = router.TierLocal
		case aiprovider.ProviderTypeAPI:
			tier = router.TierAPI
		default:
			tier = router.TierAPI
		}
		s.Router.RecordFailure(tier, errMsg)
	}
}

// ── Legacy Chat API (for backward compatibility) ────────────────────

// Chat sends a non-streaming chat request and returns the full response.
func (s *Service) Chat(messages []ChatMessage) (string, error) {
	s.mu.Lock()
	apiURL := s.apiURL
	model := s.model
	s.mu.Unlock()
	return s.chatOpenAIStream(s.currentRequestContext(), apiURL, "", model, messages, "LM Studio")
}

// CallLLM implements tools.LLMCaller interface — a simple single-turn LLM call
// for tools that need AI-driven decisions (e.g. AutoResearch hypothesis generation).
// It tries local LM Studio first, then falls back to the configured API provider.
func (s *Service) CallLLM(ctx context.Context, prompt string) (string, error) {
	// Try local LM Studio first
	messages := []ChatMessage{
		{Role: "system", Content: "You are a code optimization assistant. Respond concisely and precisely."},
		{Role: "user", Content: prompt},
	}

	// Try local
	resp, err := s.Chat(messages)
	if err == nil && resp != "" {
		return resp, nil
	}

	// Fallback to API provider if configured
	s.mu.Lock()
	p := s.provider
	s.mu.Unlock()
	if p.BaseURL != "" && p.APIKey != "" {
		s.mu.Lock()
		s.provider = p
		s.mu.Unlock()
		resp, err = s.ChatAPI(messages)
		if err == nil && resp != "" {
			return resp, nil
		}
	}

	return "", fmt.Errorf("LLM unavailable: local error=%v, API not configured", err)
}

// ChatAPI sends a non-streaming chat request to a remote API provider.
// It uses the provider's base URL, API key, and model.
func (s *Service) ChatAPI(messages []ChatMessage) (string, error) {
	s.mu.Lock()
	p := s.provider
	s.mu.Unlock()

	if p.BaseURL == "" {
		return "", fmt.Errorf("API 提供商未配置：请在设置中填写 Base URL 和 API Key")
	}
	if p.APIKey == "" {
		return "", fmt.Errorf("API Key 未配置：请在设置中填写 API Key")
	}

	return s.chatOpenAIStream(s.currentRequestContext(), p.BaseURL, p.APIKey, p.Model, messages, p.Name)
}

func (s *Service) chatOpenAIStream(ctx context.Context, baseURL, apiKey, model string, messages []ChatMessage, provider string) (string, error) {
	reqBody := ChatRequest{Model: model, Messages: messages, Temperature: 0.7, MaxTokens: 4096, Stream: true}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal %s request: %w", provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("创建 %s 请求失败: %w", provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("%s 请求失败: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s 返回错误 %d: %s", provider, resp.StatusCode, truncateString(string(b), 1200))
	}

	s.emitModelStream("start", provider, "")
	var full strings.Builder
	var raw strings.Builder
	var reasoning strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk ChatStreamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.ReasoningContent != "" {
				reasoning.WriteString(choice.Delta.ReasoningContent)
			}
			if choice.Delta.Content != "" {
				full.WriteString(choice.Delta.Content)
				s.emitModelStream("token", provider, choice.Delta.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取 %s 流失败: %w", provider, err)
	}
	if reasoning.Len() > 0 && s.hasEmitter() {
		s.emit("ai:reasoning", map[string]string{"provider": provider, "content": truncateString(reasoning.String(), 4000)})
	}
	if full.Len() == 0 {
		var result ChatResponse
		if json.Unmarshal([]byte(raw.String()), &result) == nil && len(result.Choices) > 0 {
			full.WriteString(result.Choices[0].Message.Content)
			s.emitChunkedModelOutput(provider, full.String())
		}
	}
	s.emitModelStream("end", provider, "")
	if strings.TrimSpace(full.String()) == "" {
		return "", fmt.Errorf("%s 返回空响应", provider)
	}
	if thinking, _ := splitThinkingContent(full.String()); thinking != "" && s.hasEmitter() {
		s.emit("ai:reasoning", map[string]string{
			"provider": provider,
			"content":  truncateString(thinking, 4000),
		})
	}
	return full.String(), nil
}

func openRelayBaseURL() string {
	base := strings.TrimRight(os.Getenv("OPENRELAY_BASE_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:18765/v1"
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

// ChatOpenRelay sends a non-streaming chat request through OpenRelay's
// OpenAI-compatible local endpoint. OpenRelay then routes to configured
// local/CLI/IDE providers such as Claude Code, Codex, Cursor, and Windsurf.
func (s *Service) ChatOpenRelay(messages []ChatMessage) (string, error) {
	return s.chatOpenRelay(s.currentRequestContext(), messages)
}

func (s *Service) chatOpenRelay(ctx context.Context, messages []ChatMessage) (string, error) {
	baseURL := openRelayBaseURL()
	client := &http.Client{Timeout: 120 * time.Second}

	model, err := s.firstOpenRelayModel(ctx, client, baseURL)
	if err != nil {
		return "", err
	}

	return s.chatOpenAIStream(ctx, baseURL, "unused", model, messages, "OpenRelay")
}

func (s *Service) firstOpenRelayModel(ctx context.Context, client *http.Client, baseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return "", fmt.Errorf("创建 OpenRelay 模型请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer unused")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenRelay 未运行或未响应: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenRelay 模型列表返回错误 %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析 OpenRelay 模型列表失败: %w", err)
	}
	for _, model := range result.Data {
		if strings.TrimSpace(model.ID) != "" {
			return model.ID, nil
		}
	}
	return "", fmt.Errorf("OpenRelay 没有可用模型，请在 http://localhost:18765 配置 provider 或模型组")
}

// ChatLocalCLI discovers and calls local logged-in coding CLIs directly,
// mirroring OpenRelay's "use existing local quota" idea without requiring the
// OpenRelay HTTP daemon to be running.
func (s *Service) ChatLocalCLI(messages []ChatMessage) (string, error) {
	return s.chatLocalCLI(s.currentRequestContext(), messages)
}

func (s *Service) chatLocalCLI(ctx context.Context, messages []ChatMessage) (string, error) {
	var attempts []string

	// WorkBuddy：本机安装的 WorkBuddy.app 内自带 CodeBuddy CLI（bin 名 codebuddy/cbc）。
	// 放在本地 CLI 档最前，确保优先被尝试、不被 claude/codex 的超时吃掉预算。
	if bin := findLocalCLI("codebuddy",
		"/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/bin/codebuddy",
		filepath.Join(homeDir(), ".local", "bin", "codebuddy"),
	); bin != "" {
		s.emitLLMProviderEvent("attempt", "WorkBuddy", "正在调用本地 WorkBuddy (CodeBuddy CLI)")
		// -p 非交互打印；--permission-mode bypassPermissions 自动放行避免卡交互；
		// --no-session-persistence 不落盘；--output-format text 取纯文本答案。
		resp, err := s.chatGenericLocalCLI(ctx, "WorkBuddy", bin, 45*time.Second,
			[]string{"-p", "--output-format", "text", "--permission-mode", "bypassPermissions", "--no-session-persistence", chatMessagesToCLIPrompt(messages)})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "WorkBuddy", "已返回响应")
			return resp, nil
		}
		// 超时通常意味着 codebuddy 卡在等待交互式登录（未登录时它会静默挂住）。
		attemptText := formatLLMAttemptError("WorkBuddy CLI", resp, err)
		if strings.Contains(attemptText, "超时") {
			attemptText += "（codebuddy 未登录时会静默卡住：请在终端交互登录一次 codebuddy）"
		}
		s.emitLLMProviderEvent("error", "WorkBuddy", attemptText)
		attempts = append(attempts, attemptText)
	} else {
		s.emitLLMProviderEvent("skip", "WorkBuddy", "未发现本地命令")
		attempts = append(attempts, "WorkBuddy CLI 未发现")
	}

	if claudePath := findLocalCLI("claude", "/usr/local/bin/claude", filepath.Join(homeDir(), ".local", "bin", "claude")); claudePath != "" {
		s.emitLLMProviderEvent("attempt", "Claude Code", "正在调用本地 Claude Code，允许它读取当前项目进行分析")
		resp, err := s.chatClaudeCodeCLI(ctx, messages)
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "Claude Code", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "Claude Code", formatLLMAttemptError("Claude Code", resp, err))
		attempts = append(attempts, formatLLMAttemptError("Claude Code CLI", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "Claude Code", "未发现本地命令")
		attempts = append(attempts, "Claude Code CLI 未发现")
	}

	if codexPath := findLocalCLI("codex", filepath.Join(homeDir(), ".local", "bin", "codex")); codexPath != "" {
		s.emitLLMProviderEvent("attempt", "Codex", "正在调用本地 Codex，允许它读取当前项目进行分析")
		resp, err := s.chatCodexCLI(ctx, messages)
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "Codex", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "Codex", formatLLMAttemptError("Codex", resp, err))
		attempts = append(attempts, formatLLMAttemptError("Codex CLI", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "Codex", "未发现本地命令")
		attempts = append(attempts, "Codex CLI 未发现")
	}

	// Qwen Code（阿里通义千问 CLI，基于 Gemini CLI；支持 -p 非交互模式 + --yolo 自动放行）
	if bin := findLocalCLI("qwen", filepath.Join(homeDir(), ".local", "bin", "qwen")); bin != "" {
		s.emitLLMProviderEvent("attempt", "Qwen Code", "正在调用本地 Qwen Code")
		resp, err := s.chatGenericLocalCLI(ctx, "Qwen Code", bin, 90*time.Second,
			[]string{"-p", chatMessagesToCLIPrompt(messages), "--yolo"})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "Qwen Code", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "Qwen Code", formatLLMAttemptError("Qwen Code", resp, err))
		attempts = append(attempts, formatLLMAttemptError("Qwen Code CLI", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "Qwen Code", "未发现本地命令")
		attempts = append(attempts, "Qwen Code CLI 未发现")
	}

	// DeepSeek（本地 DeepSeek CLI / harness；非交互 -p 模式）
	if bin := findLocalCLI("deepseek"); bin != "" {
		s.emitLLMProviderEvent("attempt", "DeepSeek", "正在调用本地 DeepSeek CLI")
		resp, err := s.chatGenericLocalCLI(ctx, "DeepSeek", bin, 90*time.Second,
			[]string{"-p", chatMessagesToCLIPrompt(messages)})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "DeepSeek", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "DeepSeek", formatLLMAttemptError("DeepSeek", resp, err))
		attempts = append(attempts, formatLLMAttemptError("DeepSeek CLI", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "DeepSeek", "未发现本地命令")
		attempts = append(attempts, "DeepSeek CLI 未发现")
	}

	// zcode（本地 zcode CLI；非交互 -p 模式）
	if bin := findLocalCLI("zcode"); bin != "" {
		s.emitLLMProviderEvent("attempt", "zcode", "正在调用本地 zcode")
		resp, err := s.chatGenericLocalCLI(ctx, "zcode", bin, 90*time.Second,
			[]string{"-p", chatMessagesToCLIPrompt(messages)})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "zcode", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "zcode", formatLLMAttemptError("zcode", resp, err))
		attempts = append(attempts, formatLLMAttemptError("zcode CLI", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "zcode", "未发现本地命令")
		attempts = append(attempts, "zcode CLI 未发现")
	}

	return "", fmt.Errorf("%s", strings.Join(attempts, "；"))
}

// ChatIDEProviders discovers local IDE quota providers. Cursor/Windsurf use
// proprietary ConnectRPC bridges; VS Code Copilot commonly appears as a VS Code
// auth/session. When a compatible local bridge is configured or running, this
// function calls it; otherwise it reports exact discovery state so Auto can keep
// falling back without pretending these providers do not exist.
func (s *Service) ChatIDEProviders(messages []ChatMessage) (string, error) {
	var attempts []string

	if s.cursorAuthorized() {
		if base := firstNonEmptyEnv("TIANCAN_CURSOR_API_BASE_URL", "CURSOR_API_BASE_URL"); base != "" {
			resp, err := chatOpenAICompatibleBase(base, firstNonEmptyEnv("TIANCAN_CURSOR_MODEL", "CURSOR_MODEL", "cursor-auto"), messages, "Cursor")
			if err == nil && strings.TrimSpace(resp) != "" {
				return resp, nil
			}
			attempts = append(attempts, formatLLMAttemptError("Cursor", resp, err))
		} else if isTCPPortOpen("127.0.0.1", 18780, 250*time.Millisecond) {
			attempts = append(attempts, "Cursor 已发现且 18780 代理端口可用，但需要 ConnectRPC 客户端实现")
		} else {
			attempts = append(attempts, "Cursor 已授权，但未发现可调用的本地 RPC/API 端口")
		}
	} else {
		attempts = append(attempts, "Cursor 未发现授权状态")
	}

	if s.windsurfAuthorized() {
		if base := firstNonEmptyEnv("TIANCAN_WINDSURF_API_BASE_URL", "WINDSURF_API_BASE_URL"); base != "" {
			resp, err := chatOpenAICompatibleBase(base, firstNonEmptyEnv("TIANCAN_WINDSURF_MODEL", "WINDSURF_MODEL", "windsurf-auto"), messages, "Windsurf")
			if err == nil && strings.TrimSpace(resp) != "" {
				return resp, nil
			}
			attempts = append(attempts, formatLLMAttemptError("Windsurf", resp, err))
		} else if isTCPPortOpen("127.0.0.1", 18766, 250*time.Millisecond) {
			attempts = append(attempts, "Windsurf 已发现且 18766 代理端口可用，但需要 ConnectRPC 客户端实现")
		} else {
			attempts = append(attempts, "Windsurf 已授权，但未发现可调用的本地 RPC/API 端口")
		}
	} else {
		attempts = append(attempts, "Windsurf 未发现授权状态")
	}

	if s.copilotAuthorized() {
		if base := firstNonEmptyEnv("TIANCAN_COPILOT_API_BASE_URL", "COPILOT_API_BASE_URL"); base != "" {
			resp, err := chatOpenAICompatibleBase(base, firstNonEmptyEnv("TIANCAN_COPILOT_MODEL", "COPILOT_MODEL", "copilot-auto"), messages, "Copilot")
			if err == nil && strings.TrimSpace(resp) != "" {
				return resp, nil
			}
			attempts = append(attempts, formatLLMAttemptError("Copilot", resp, err))
		} else if isTCPPortOpen("127.0.0.1", 18769, 250*time.Millisecond) {
			resp, err := chatOllamaCompatibleBase("http://127.0.0.1:18769", firstNonEmptyEnv("TIANCAN_COPILOT_MODEL", "COPILOT_MODEL", "copilot"), messages, "Copilot")
			if err == nil && strings.TrimSpace(resp) != "" {
				return resp, nil
			}
			attempts = append(attempts, formatLLMAttemptError("Copilot Ollama bridge", resp, err))
		} else {
			attempts = append(attempts, "VS Code Copilot 已发现，但未发现 Ollama/API 桥接端口")
		}
	} else {
		attempts = append(attempts, "VS Code Copilot 未发现授权状态")
	}

	return "", fmt.Errorf("%s", strings.Join(attempts, "；"))
}

func findLocalCLI(name string, candidates ...string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func (s *Service) emitLLMProviderEvent(status, provider, detail string) {
	if s.hasEmitter() {
		s.emit("ai:provider", map[string]string{
			"status":   status,
			"provider": provider,
			"detail":   truncateString(detail, 240),
		})
	}
}

func (s *Service) emitModelStream(phase, provider, content string) {
	if !s.hasEmitter() {
		return
	}
	s.emit("ai:model-stream", map[string]string{
		"phase":    phase,
		"provider": provider,
		"content":  content,
	})
}

func (s *Service) emitChunkedModelOutput(provider, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	s.emitModelStream("start", provider, "")
	const chunkRunes = 48
	runes := []rune(content)
	for len(runes) > 0 {
		size := chunkRunes
		if len(runes) < size {
			size = len(runes)
		}
		s.emitModelStream("token", provider, string(runes[:size]))
		runes = runes[size:]
		if s.hasEmitter() && len(runes) > 0 {
			time.Sleep(6 * time.Millisecond)
		}
	}
	s.emitModelStream("end", provider, "")
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(envconfig.EnvString(key)); value != "" {
			return value
		}
	}
	return ""
}

func isTCPPortOpen(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func normalizeOpenAICompatibleBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

func chatOpenAICompatibleBase(baseURL, model string, messages []ChatMessage, label string) (string, error) {
	base := normalizeOpenAICompatibleBase(baseURL)
	if base == "" {
		return "", fmt.Errorf("%s API 地址为空", label)
	}
	if strings.TrimSpace(model) == "" {
		model = "auto"
	}

	reqBody := ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   4096,
		Stream:      false,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal %s request: %w", label, err)
	}

	req, err := http.NewRequest("POST", base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("创建 %s 请求失败: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ideProviderAPIKey(label))

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s 请求失败: %w", label, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s 返回错误 %d: %s", label, resp.StatusCode, truncateString(string(b), 1200))
	}

	var result ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析 %s 响应失败: %w", label, err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("%s 返回空响应", label)
	}
	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

func ideProviderAPIKey(label string) string {
	normalized := strings.ToUpper(strings.ReplaceAll(label, " ", "_"))
	for _, key := range []string{
		"TIANCAN_" + normalized + "_API_KEY",
		normalized + "_API_KEY",
		"TIANCAN_IDE_RPC_API_KEY",
		"IDE_RPC_API_KEY",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "unused"
}

func chatOllamaCompatibleBase(baseURL, model string, messages []ChatMessage, label string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("%s Ollama 地址为空", label)
	}
	if strings.TrimSpace(model) == "" {
		model = "auto"
	}

	ollamaMessages := make([]map[string]string, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		role := message.Role
		if role == "" {
			role = "user"
		}
		ollamaMessages = append(ollamaMessages, map[string]string{
			"role":    role,
			"content": content,
		})
	}

	reqBody := map[string]interface{}{
		"model":    model,
		"messages": ollamaMessages,
		"stream":   false,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal %s Ollama request: %w", label, err)
	}

	req, err := http.NewRequest("POST", base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("创建 %s Ollama 请求失败: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		openAIResp, openAIErr := chatOpenAICompatibleBase(base, model, messages, label)
		if openAIErr == nil && strings.TrimSpace(openAIResp) != "" {
			return openAIResp, nil
		}
		return "", fmt.Errorf("%s Ollama 请求失败: %w", label, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		openAIResp, openAIErr := chatOpenAICompatibleBase(base, model, messages, label)
		if openAIErr == nil && strings.TrimSpace(openAIResp) != "" {
			return openAIResp, nil
		}
		return "", fmt.Errorf("%s Ollama 返回错误 %d: %s", label, resp.StatusCode, truncateString(string(b), 1200))
	}

	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析 %s Ollama 响应失败: %w", label, err)
	}
	if strings.TrimSpace(result.Error) != "" {
		return "", fmt.Errorf("%s Ollama 返回错误: %s", label, truncateString(result.Error, 1200))
	}
	if content := strings.TrimSpace(result.Message.Content); content != "" {
		return content, nil
	}
	if content := strings.TrimSpace(result.Response); content != "" {
		return content, nil
	}
	return "", fmt.Errorf("%s Ollama 返回空响应", label)
}

func (s *Service) cursorAuthorized() bool {
	home := homeDir()
	if home == "" {
		return false
	}
	dbPath := filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	return sqliteHasItemKey(dbPath, "cursorAuth/accessToken", "cursorAuth/refreshToken", "cursorAuth/cachedEmail")
}

func (s *Service) windsurfAuthorized() bool {
	home := homeDir()
	if home == "" {
		return false
	}
	dbPath := filepath.Join(home, "Library", "Application Support", "Windsurf", "User", "globalStorage", "state.vscdb")
	if sqliteHasItemKey(dbPath,
		"codeium.windsurf-windsurf_auth",
		`secret://{"extensionId":"codeium.windsurf","key":"windsurf_auth.sessions"}`,
		"windsurfAuthStatus",
	) {
		return true
	}
	return sqliteHasItemKeyLike(dbPath, "%windsurf%auth%", "%codeium%auth%")
}

func (s *Service) copilotAuthorized() bool {
	home := homeDir()
	if home == "" {
		return false
	}
	candidates := []string{
		filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, "Library", "Application Support", "Windsurf", "User", "globalStorage", "state.vscdb"),
	}
	for _, dbPath := range candidates {
		if sqliteHasItemKeyLike(dbPath, "%copilot%", "%github.copilot%") {
			return true
		}
	}
	return false
}

func sqliteHasItemKey(dbPath string, keys ...string) bool {
	if !fileExists(dbPath) {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return false
	}
	defer db.Close()

	tableName := sqliteItemTableName(db)
	if tableName == "" {
		return false
	}
	query := fmt.Sprintf("SELECT 1 FROM %s WHERE key = ? LIMIT 1", tableName)
	for _, key := range keys {
		var exists int
		if err := db.QueryRow(query, key).Scan(&exists); err == nil && exists == 1 {
			return true
		}
	}
	return false
}

func sqliteHasItemKeyLike(dbPath string, patterns ...string) bool {
	if !fileExists(dbPath) {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return false
	}
	defer db.Close()

	tableName := sqliteItemTableName(db)
	if tableName == "" {
		return false
	}
	query := fmt.Sprintf("SELECT 1 FROM %s WHERE lower(key) LIKE lower(?) LIMIT 1", tableName)
	for _, pattern := range patterns {
		var exists int
		if err := db.QueryRow(query, pattern).Scan(&exists); err == nil && exists == 1 {
			return true
		}
	}
	return false
}

func sqliteItemTableName(db *sql.DB) string {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return ""
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if strings.EqualFold(name, "ItemTable") {
			return quoteSQLiteIdent(name)
		}
	}
	return ""
}

func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (s *Service) chatClaudeCodeCLI(parent context.Context, messages []ChatMessage) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()

	claudePath := findLocalCLI("claude", "/usr/local/bin/claude", filepath.Join(homeDir(), ".local", "bin", "claude"))
	if claudePath == "" {
		return "", fmt.Errorf("Claude Code CLI 未发现")
	}

	cmd := exec.CommandContext(ctx, claudePath,
		"-p", chatMessagesToCLIPrompt(messages),
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", "default",
		"--no-session-persistence",
	)
	configureCommandCancellation(cmd)
	cmd.Dir = s.cliWorkDir()

	out, err := s.runClaudeStream(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("Claude Code CLI 超时")
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (s *Service) chatCodexCLI(parent context.Context, messages []ChatMessage) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()

	codexPath := findLocalCLI("codex", filepath.Join(homeDir(), ".local", "bin", "codex"))
	if codexPath == "" {
		return "", fmt.Errorf("Codex CLI 未发现")
	}

	tmp, err := os.CreateTemp("", "tiancan-codex-last-*.txt")
	if err != nil {
		return "", fmt.Errorf("创建 Codex 输出文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	args := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-rules",
		"--sandbox", "workspace-write",
		"--cd", s.cliWorkDir(),
		"--output-last-message", tmpPath,
	}
	args = append(args, codexExecApprovalArgs()...)
	args = append(args, chatMessagesToCLIPrompt(messages))

	cmd := exec.CommandContext(ctx, codexPath, args...)
	configureCommandCancellation(cmd)
	cmd.Dir = s.cliWorkDir()

	streamOutput, err := s.runCodexStream(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("Codex CLI 超时")
	}
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("读取 Codex 输出失败: %w", err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return strings.TrimSpace(streamOutput), nil
	}
	return strings.TrimSpace(string(content)), nil
}

// chatGenericLocalCLI runs an arbitrary local coding CLI in non-interactive
// single-shot mode: it feeds the conversation as a prompt, captures the final
// stdout, and bounds the run with a timeout so a misbehaving CLI cannot hang
// Auto forever. Discovery and attempt/success/error events are the caller's
// responsibility. Used for providers that don't yet have a bespoke stream
// parser (Qwen Code, DeepSeek, WorkBuddy, zcode, ...).
func (s *Service) chatGenericLocalCLI(ctx context.Context, provider, bin string, timeout time.Duration, args []string) (string, error) {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	configureCommandCancellation(cmd)
	cmd.Dir = s.cliWorkDir()

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s CLI 超时", provider)
		}
		return "", fmt.Errorf("%s CLI 执行失败: %w", provider, err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func codexExecApprovalArgs() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	codexPath := findLocalCLI("codex", filepath.Join(homeDir(), ".local", "bin", "codex"))
	if codexPath == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, codexPath, "exec", "--help").CombinedOutput()
	if err != nil {
		return nil
	}
	help := string(out)
	switch {
	case strings.Contains(help, "--ask-for-approval"):
		return []string{"--ask-for-approval", "never"}
	case strings.Contains(help, "--approval-policy"):
		return []string{"--approval-policy", "never"}
	default:
		return nil
	}
}

func (s *Service) cliWorkDir() string {
	root := s.GetRootPath()
	if root != "" {
		return root
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func chatMessagesToLLMPrompt(messages []ChatMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case "system":
			sb.WriteString("System: ")
		case "assistant":
			sb.WriteString("Assistant: ")
		default:
			sb.WriteString("User: ")
		}
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

const cliPromptMaxRunes = 24000

// chatMessagesToCLIPrompt omits TianCan's full tool schema because coding CLIs
// already have native project tools. Keeping the schemas duplicates thousands
// of tokens and can make an otherwise healthy local CLI exceed its timeout.
func chatMessagesToCLIPrompt(messages []ChatMessage) string {
	var systemParts []string
	var conversationParts []string
	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if m.Role == "system" {
			systemParts = append(systemParts, compactCLISystemPrompt(content))
			continue
		}
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		conversationParts = append(conversationParts, role+": "+limitRunes(content, 6000))
	}

	system := limitRunes(strings.Join(systemParts, "\n\n"), 10000)
	conversation := keepRecentPromptParts(conversationParts, 13500)
	prompt := strings.TrimSpace(system + "\n\n" +
		"Use your native coding tools to inspect and modify the current project when needed. " +
		"Do not reproduce tool schemas or internal workflow narration. Return a concise final response when the task is complete.\n\n" +
		conversation)
	return limitRunes(prompt, cliPromptMaxRunes)
}

func compactCLISystemPrompt(content string) string {
	const toolsMarker = "# Available Tools"
	if marker := strings.Index(content, toolsMarker); marker >= 0 {
		prefix := strings.TrimSpace(content[:marker])
		root := ""
		if rootMarker := strings.LastIndex(content, "Project root:"); rootMarker >= 0 {
			root = strings.TrimSpace(strings.SplitN(content[rootMarker:], "\n", 2)[0])
		}
		if root != "" {
			return prefix + "\n\n" + root
		}
		return prefix
	}
	return content
}

func keepRecentPromptParts(parts []string, maxRunes int) string {
	kept := make([]string, 0, len(parts))
	remaining := maxRunes
	for i := len(parts) - 1; i >= 0 && remaining > 0; i-- {
		part := parts[i]
		partRunes := []rune(part)
		if len(partRunes) > remaining {
			part = string(partRunes[len(partRunes)-remaining:])
			remaining = 0
		} else {
			remaining -= len(partRunes)
		}
		kept = append(kept, part)
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	return strings.Join(kept, "\n\n")
}

func limitRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ChatStream sends a streaming chat request. It calls onToken for each token
// received and returns the full accumulated response.
func (s *Service) ChatStream(messages []ChatMessage, onToken func(token string)) (string, error) {
	s.mu.Lock()
	apiURL := s.apiURL
	model := s.model
	s.mu.Unlock()

	reqBody := ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   4096,
		Stream:      true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(s.currentRequestContext(), "POST", apiURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("创建 LM Studio 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LM Studio 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LM Studio 返回错误 %d: %s", resp.StatusCode, string(b))
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk ChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				full.WriteString(choice.Delta.Content)
				if onToken != nil {
					onToken(choice.Delta.Content)
				}
			}
		}
	}

	return full.String(), nil
}

// ── Agent TAOR Loop ───────────────────────────────────────────────

// AgentTool defines a tool the agent can use.
type AgentTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema string `json:"input_schema"` // JSON schema as string
}

// AgentRunResult contains the result of an agent run.
type AgentRunResult struct {
	Messages    []ChatMessage `json:"messages"`
	Iterations  int           `json:"iterations"`
	Done        bool          `json:"done"`
	Error       string        `json:"error,omitempty"`
	DurationMs  int64         `json:"durationMs,omitempty"`  // AgentV2 duration
	AgentType   string        `json:"agentType,omitempty"`   // "v1", "v2", "ralph"
	TotalTokens int           `json:"totalTokens,omitempty"` // Token count
	HasRollback bool          `json:"hasRollback,omitempty"` // V2 rollback support
}

// registryCache caches tool registries per rootPath.
var (
	registryCache   = map[string]*tools.Registry{}
	registryCacheMu sync.Mutex
)

// getRegistry returns (or creates) a tools.Registry for the given rootPath.
// Injects the LLMCaller so tools like AutoResearch can call the LLM.
func (s *Service) getRegistry(rootPath string) *tools.Registry {
	registryCacheMu.Lock()
	defer registryCacheMu.Unlock()
	if r, ok := registryCache[rootPath]; ok {
		return r
	}
	r := tools.NewRegistry(rootPath)
	r.SetLLMCaller(s) // ai.Service implements tools.LLMCaller via CallLLM
	r.SetSandboxManager(s.sandboxMgr)
	registryCache[rootPath] = r
	return r
}

// RemoveRegistry removes a cached registry for the given rootPath.
// Called when a project is closed to free memory.
func (s *Service) RemoveRegistry(rootPath string) {
	registryCacheMu.Lock()
	defer registryCacheMu.Unlock()
	delete(registryCache, rootPath)
}

// builtInTools returns the tools available to the agent from the Registry.
func (s *Service) builtInTools(rootPath string) []AgentTool {
	r := s.getRegistry(rootPath)
	all := r.All()
	result := make([]AgentTool, 0, len(all))
	for _, t := range all {
		schemaBytes, _ := json.Marshal(t.InputSchema())
		result = append(result, AgentTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: string(schemaBytes),
		})
	}
	return result
}

// toolDescriptions builds the tool descriptions string for the system prompt.
func (s *Service) toolDescriptions(rootPath string) string {
	ts := s.builtInTools(rootPath)
	var b strings.Builder
	b.WriteString("你可以使用以下工具来完成任务。每个工具调用格式为：\n")
	b.WriteString("```tool\n{\"name\": \"工具名\", \"args\": {参数}}\n```\n\n")
	for _, t := range ts {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n  参数 Schema: %s\n\n", t.Name, t.Description, t.InputSchema))
	}

	// Add use_skill virtual tool
	b.WriteString("- **use_skill**: 调用预定义技能包，按步骤自动执行\n  参数 Schema: {\"skill\": \"技能名称\"}\n\n")
	b.WriteString("- **project_memory_remember**: 保存一条重要、长期有效的项目记忆，适合架构决策、约定、踩坑、用户偏好。不要保存可直接从代码读取的普通内容\n  参数 Schema: {\"content\":\"记忆内容\",\"type\":\"architecture|workflow|preference|bug|pattern|fact\",\"files\":[\"相关文件\"],\"concepts\":[\"关键词\"]}\n\n")
	b.WriteString("- **project_memory_recall**: 读取当前项目的精简记忆，用于在少量 token 内恢复关键上下文\n  参数 Schema: {\"query\":\"可选查询\",\"limit\":8}\n\n")

	// List available skills
	sm := skills.NewManager(rootPath)
	if err := sm.LoadAll(); err == nil {
		available := sm.ListInfo()
		if len(available) > 0 {
			b.WriteString("可用技能：\n")
			for _, s := range available {
				b.WriteString(fmt.Sprintf("- **%s** (v%s): %s — 触发词: %s\n", s.Name, s.Version, s.Description, strings.Join(s.Triggers, ", ")))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("当你需要使用工具时，输出上述格式的 tool 块。当你认为任务已完成或需要用户输入时，直接回复文字。\n")
	return b.String()
}

// ExecuteTool runs a tool call via the tools.Registry and returns the result as a string.
// This is the exported version that implements hierarchy.ToolExecutor.
func (s *Service) ExecuteTool(name string, args map[string]interface{}, rootPath string) string {
	// Handle use_skill virtual tool
	if name == "use_skill" {
		return s.executeSkill(args, rootPath)
	}
	if name == "project_memory_remember" {
		content, _ := args["content"].(string)
		memType, _ := args["type"].(string)
		mem, err := memory.Remember(rootPath, content, memType, stringSliceArg(args["files"]), stringSliceArg(args["concepts"]))
		if err != nil {
			return fmt.Sprintf("保存项目记忆失败: %s", err)
		}
		return fmt.Sprintf("已保存项目记忆: %s", mem.Title)
	}
	if name == "project_memory_recall" {
		query, _ := args["query"].(string)
		limit := intArg(args["limit"], 8)
		memories, err := memory.Recall(rootPath, query, limit)
		if err != nil {
			return fmt.Sprintf("读取项目记忆失败: %s", err)
		}
		if len(memories) == 0 {
			return "当前项目暂无记忆"
		}
		var lines []string
		for _, m := range memories {
			lines = append(lines, fmt.Sprintf("- [%s] %s", m.Type, m.Content))
		}
		return strings.Join(lines, "\n")
	}

	r := s.getRegistry(rootPath)
	result, err := r.Execute(s.currentRequestContext(), name, args)
	if err != nil {
		// Tool not found in registry — try name aliases for backward compatibility
		aliases := map[string]string{
			"run_bash":   "bash",
			"read_file":  "read_file",
			"write_file": "file_write",
			"list_dir":   "list_directory",
		}
		if alias, ok := aliases[name]; ok && alias != name {
			result, err = r.Execute(s.currentRequestContext(), alias, args)
		}
		if err != nil {
			return fmt.Sprintf("未知工具: %s", name)
		}
	}
	if result.Success {
		return result.Content
	}
	return fmt.Sprintf("工具 %s 执行失败: %s", name, result.Content)
}

func stringSliceArg(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func intArg(v interface{}, fallback int) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return fallback
}

func (s *Service) projectMemoryContext(rootPath, query string) string {
	return memory.RenderContext(rootPath, query, 1200)
}

func (s *Service) withProjectMemory(rootPath, query, prompt string) string {
	ctx := s.projectMemoryContext(rootPath, query)
	if ctx == "" {
		return prompt
	}
	return prompt + "\n\n" + ctx
}

// executeSkill runs a skill by name, executing each step sequentially.
func (s *Service) executeSkill(args map[string]interface{}, rootPath string) string {
	skillName, _ := args["skill"].(string)
	if skillName == "" {
		return "错误: use_skill 缺少 skill 参数"
	}

	sm := skills.NewManager(rootPath)
	if err := sm.LoadAll(); err != nil {
		return fmt.Sprintf("加载技能失败: %s", err)
	}

	skill, ok := sm.Get(skillName)
	if !ok {
		available := sm.ListInfo()
		names := make([]string, len(available))
		for i, s := range available {
			names[i] = s.Name
		}
		return fmt.Sprintf("未知技能: %s。可用技能: %s", skillName, strings.Join(names, ", "))
	}

	var results []string
	for i, step := range skill.Steps {
		result := s.ExecuteTool(step.Tool, step.Args, rootPath) // already method
		results = append(results, fmt.Sprintf("步骤 %d (%s): %s", i+1, step.Tool, result))
	}
	return strings.Join(results, "\n")
}

// ListSkills returns available skills for the current project.
func (s *Service) ListSkills() []skills.SkillInfo {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return nil
	}
	return sm.ListInfo()
}

// InstallSkill installs a skill from YAML content string.
func (s *Service) InstallSkill(yamlContent string) error {
	sm := skills.NewManager(s.rootPath())
	return sm.InstallLocal(yamlContent)
}

// InstallSkillFromFile installs a skill from a local file path.
func (s *Service) InstallSkillFromFile(filePath string) error {
	sm := skills.NewManager(s.rootPath())
	return sm.InstallFromFile(filePath)
}

// DeleteSkill removes a skill by name.
func (s *Service) DeleteSkill(name string) error {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return fmt.Errorf("加载技能失败: %w", err)
	}
	return sm.Delete(name)
}

// ValidateSkill validates a skill by name and returns the result.
func (s *Service) ValidateSkill(name string) (bool, string) {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return false, fmt.Sprintf("加载技能失败: %s", err)
	}
	return sm.Validate(name)
}

// ValidateAllSkills validates all loaded skills.
func (s *Service) ValidateAllSkills() map[string]string {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return map[string]string{"error": err.Error()}
	}
	return sm.ValidateAll()
}

// GetSkillDetail returns full detail for a skill.
func (s *Service) GetSkillDetail(name string) *skills.Skill {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return nil
	}
	skill, ok := sm.SkillDetail(name)
	if !ok {
		return nil
	}
	return skill
}

// ExportSkill exports a skill as portable JSON for sharing.
func (s *Service) ExportSkill(name string) string {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return ""
	}
	exported, err := sm.ExportSkill(name)
	if err != nil {
		return ""
	}
	return exported
}

// ExportAllSkills exports all skills as a JSON array.
func (s *Service) ExportAllSkills() string {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return ""
	}
	exported, err := sm.ExportAll()
	if err != nil {
		return ""
	}
	return exported
}

// ImportSkillFromJSON imports a skill from market JSON string.
func (s *Service) ImportSkillFromJSON(jsonStr string) string {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return fmt.Sprintf("failed to load skills: %v", err)
	}
	if err := sm.ImportFromMarket(jsonStr); err != nil {
		return fmt.Sprintf("import failed: %v", err)
	}
	return "ok"
}

// ImportSkillsFromFile imports skills from a local market JSON file.
func (s *Service) ImportSkillsFromFile(filePath string) string {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return fmt.Sprintf("failed to load skills: %v", err)
	}
	if err := sm.ImportFromFile(filePath); err != nil {
		return fmt.Sprintf("import failed: %v", err)
	}
	return "ok"
}

// SearchMarketSkills searches the community market for skills.
func (s *Service) SearchMarketSkills(query, category string) []skills.MarketEntry {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return nil
	}
	results, err := sm.SearchMarket(query, category)
	if err != nil {
		return nil
	}
	return results
}

// RateSkill records a rating for a skill.
func (s *Service) RateSkill(name string, rating int) string {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return fmt.Sprintf("failed: %v", err)
	}
	if err := sm.RateSkill(name, rating); err != nil {
		return fmt.Sprintf("failed: %v", err)
	}
	return "ok"
}

// GetSkillMarketInfo returns market info for a skill.
func (s *Service) GetSkillMarketInfo(name string) *skills.MarketEntry {
	sm := skills.NewManager(s.rootPath())
	if err := sm.LoadAll(); err != nil {
		return nil
	}
	skill, ok := sm.SkillDetail(name)
	if !ok {
		return nil
	}

	// Build market entry from skill
	data, _ := os.ReadFile(skill.FilePath)
	entry := skills.MarketEntry{
		Name:        skill.Name,
		Description: skill.Description,
		Version:     skill.Version,
		Author:      "local",
		Category:    skills.InferCategory(skill),
		Tags:        skill.Triggers,
		YAMLContent: string(data),
		Rating:      float64(sm.GetSkillRating(name)),
	}
	return &entry
}

// ─── Sandbox Management API ─────────────────────────────────────────────

// SandboxProviderStatus returns the availability status of each sandbox provider.
func (s *Service) SandboxProviderStatus() []sandbox.ProviderStatus {
	return s.sandboxMgr.GetProviderStatuses()
}

// SandboxAutoInstall auto-downloads and installs a sandbox provider.
func (s *Service) SandboxAutoInstall(providerName string) ([]string, error) {
	return s.sandboxMgr.AutoInstallProvider(providerName)
}

// SandboxSetE2BAPIKey configures the E2B API key.
func (s *Service) SandboxSetE2BAPIKey(apiKey string) {
	s.sandboxMgr.SetE2BAPIKey(apiKey)
}

// SandboxSetProvider manually selects the sandbox provider.
func (s *Service) SandboxSetProvider(providerName string) error {
	return s.sandboxMgr.SetProvider(providerName)
}

// SandboxList returns active sandboxes.
func (s *Service) SandboxList() []*sandbox.SandboxInfo {
	return s.sandboxMgr.List()
}

// SandboxCreate creates a new sandbox.
func (s *Service) SandboxCreate(image string) (*sandbox.SandboxInfo, error) {
	cfg := sandbox.DefaultConfig()
	if image != "" {
		cfg.Image = image
	}
	return s.sandboxMgr.Create(context.Background(), cfg)
}

// SandboxStop stops a sandbox by ID.
func (s *Service) SandboxStop(id string) error {
	return s.sandboxMgr.Stop(context.Background(), id)
}

// GetRootPath returns the current project root path (exported for Wails bindings).
func (s *Service) GetRootPath() string {
	return s.rootPath()
}

// SetRootPath sets the current project root path.
func (s *Service) SetRootPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootPathVal = path
}

// rootPath returns the current project root path.
func (s *Service) rootPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rootPathVal
}

// ParseToolCalls implements hierarchy.ToolCallParser interface.
// It parses the first tool call from an LLM response.
func (s *Service) ParseToolCalls(response string) (string, map[string]interface{}, bool) {
	return parseToolCall(response)
}

// parseToolCall extracts a tool call from the assistant's response.
// Returns (toolName, args, ok). Uses the webai/parser for robust extraction.
func parseToolCall(content string) (string, map[string]interface{}, bool) {
	// First try the old ```tool ... ``` format for backward compat
	if start := strings.Index(content, "```tool"); start != -1 {
		end := strings.Index(content[start+7:], "```")
		if end != -1 {
			jsonStr := strings.TrimSpace(content[start+7 : start+7+end])
			var call struct {
				Name string                 `json:"name"`
				Args map[string]interface{} `json:"args"`
			}
			if err := json.Unmarshal([]byte(jsonStr), &call); err == nil && call.Name != "" {
				return call.Name, call.Args, true
			}
		}
	}

	// Use webai/parser for broader format support (XML tags, Anthropic blocks, etc.)
	toolCalls := parser.ParseToolCalls(content)
	if len(toolCalls) > 0 {
		tc := toolCalls[0]
		var args map[string]interface{}
		if err := json.Unmarshal(tc.Arguments, &args); err != nil {
			args = map[string]interface{}{"raw": string(tc.Arguments)}
		}
		return tc.Name, args, true
	}

	return "", nil, false
}

// extractVisibleThinking returns only reasoning text explicitly produced by the
// model, with tool protocol blocks removed before it is sent to the UI.
func extractVisibleThinking(content string) string {
	thinking, _ := splitThinkingContent(content)
	if thinking == "" {
		thinking = content
	}
	for _, tc := range parser.ParseToolCalls(thinking) {
		thinking = strings.Replace(thinking, tc.RawText, "", 1)
	}
	return stripThinkingArtifacts(thinking)
}

// removeOverlap detects if the start of answer repeats the end of thinking
// and strips the duplicated prefix from answer. DeepSeek's RESPONSE fragment
// often repeats the last few words/characters of the THINK fragment.
func removeOverlap(thinking, answer string) string {
	if thinking == "" || answer == "" {
		return answer
	}
	// Try matching suffixes of increasing length (up to 200 chars)
	maxOverlap := 200
	if len(thinking) < maxOverlap {
		maxOverlap = len(thinking)
	}
	if len(answer) < maxOverlap {
		maxOverlap = len(answer)
	}
	// Check from longest possible overlap down to 1 character
	for n := maxOverlap; n > 0; n-- {
		suffix := thinking[len(thinking)-n:]
		if strings.HasPrefix(answer, suffix) {
			return strings.TrimLeft(answer[n:], " \n")
		}
	}
	return answer
}

// splitThinkingContent splits a response from ParseSSEDeepSeek into thinking and answer parts.
// ParseSSEDeepSeek returns a combined string with thinking and answer sections.
// This function separates them so the agent loop can display thinking separately
// and only show the answer as the final assistant response.
func splitThinkingContent(resp string) (thinking string, answer string) {
	thinkStart := "<think>\n"
	thinkEnd := "\n</think>"
	startIdx := strings.Index(resp, thinkStart)
	if startIdx == -1 {
		return "", resp
	}
	afterStart := startIdx + len(thinkStart)
	endIdx := strings.Index(resp[afterStart:], thinkEnd)
	if endIdx == -1 {
		return "", resp
	}
	thinking = resp[afterStart : afterStart+endIdx]
	answerStart := afterStart + endIdx + len(thinkEnd)
	answer = strings.TrimLeft(resp[answerStart:], "\n")
	// Detect and remove overlap: DeepSeek RESPONSE fragment often repeats
	// the last few words/characters of the THINK fragment at the start of answer.
	answer = removeOverlap(thinking, answer)
	return thinking, answer
}

// stripThinkingArtifacts cleans up remaining XML/HTML tags and empty code blocks
// left after stripping tool call RawText from a model response.
func stripThinkingArtifacts(s string) string {
	// Strip <invoke ...>...</invoke> wrapper tags (DeepSeek format)
	s = stripTagPair(s, "<invoke ", "</invoke>")
	// Strip thinking markers
	s = strings.ReplaceAll(s, "\u2705", "")
	s = strings.ReplaceAll(s, "\u2706", "")
	// Strip Chinese thinking section labels (DeepSeek outputs these)
	s = stripThinkingLabels(s)
	// Strip all remaining XML-like tags using structured parser
	s = stripXMLTags(s)
	// Strip residual fragments left after XML tag removal (e.g. *tool_call>, tool_call>, invoke>)
	s = stripResidualFragments(s)
	// Strip orphaned opening angle brackets (e.g. "< " or "</ ")
	s = stripOrphanBrackets(s)
	// Strip empty code blocks left after removing tool call content
	s = stripEmptyCodeBlocks(s)
	// Strip instruction text from formatWebToolResults that the model echoes back
	s = stripInstructions(s)
	// Strip DeepSeek echo artifacts (repeated words like "我们尝试尝试", "好的，,")
	s = stripEchoArtifacts(s)
	// Collapse runs of 3+ newlines into 2 (blank line)
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// stripTagPair removes all content between openTag and closeTag (inclusive).
func stripTagPair(text, openTag, closeTag string) string {
	for {
		start := strings.Index(text, openTag)
		if start == -1 {
			break
		}
		end := strings.Index(text[start:], closeTag)
		if end == -1 {
			break
		}
		end += start + len(closeTag)
		text = text[:start] + text[end:]
	}
	return text
}

// stripXMLTags removes all XML-like tags from text using a structured character parser.
func stripXMLTags(text string) string {
	var b strings.Builder
	inTag := false
	inStr := false
	depth := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inTag {
			if inStr {
				if ch == '\\' && i+1 < len(text) {
					i++
					continue
				}
				if ch == '"' || ch == '\'' {
					inStr = false
				}
				continue
			}
			switch ch {
			case '"', '\'':
				inStr = true
			case '>':
				depth--
				if depth <= 0 {
					inTag = false
					depth = 0
				}
			case '<':
				depth++
			}
			continue
		}
		if ch == '<' {
			if i+1 < len(text) && (text[i+1] == '/' || isAlpha(text[i+1])) {
				inTag = true
				depth = 1
				continue
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// stripResidualFragments removes leftover fragments like "word>" or "*word>".
func stripResidualFragments(text string) string {
	var b strings.Builder
	i := 0
	for i < len(text) {
		start := i
		if text[i] == '*' {
			i++
		}
		wordStart := i
		for i < len(text) && isAlphaUnderscore(text[i]) {
			i++
		}
		if i > wordStart && i < len(text) && text[i] == '>' {
			i++ // skip the >
			continue
		}
		i = start
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// stripOrphanBrackets removes orphaned opening angle brackets like "< " or "</ ".
func stripOrphanBrackets(text string) string {
	text = strings.ReplaceAll(text, "</ ", "")
	text = strings.ReplaceAll(text, "< ", "")
	return text
}

// stripEmptyCodeBlocks removes ```lang\n``` blocks with no content.
func stripEmptyCodeBlocks(text string) string {
	searchFrom := 0
	for {
		start := strings.Index(text[searchFrom:], "```")
		if start == -1 {
			break
		}
		start += searchFrom
		afterOpen := start + 3
		lineEnd := strings.Index(text[afterOpen:], "\n")
		if lineEnd == -1 {
			searchFrom = afterOpen
			continue
		}
		innerStart := afterOpen + lineEnd + 1
		closeIdx := strings.Index(text[innerStart:], "```")
		if closeIdx == -1 {
			searchFrom = innerStart
			continue
		}
		closeIdx += innerStart
		inner := text[innerStart:closeIdx]
		if strings.TrimSpace(inner) == "" {
			text = text[:start] + text[closeIdx+3:]
			searchFrom = start
		} else {
			searchFrom = closeIdx + 3
		}
	}
	return text
}

// stripInstructions removes instruction text echoed back by the model.
func stripInstructions(text string) string {
	instructions := []string{
		"Based on these results, continue your reasoning. Call more tools if needed, or provide your final answer.",
		"Based on these results, continue your reasoning Call more tools if needed, or provide your final answer",
		"If you have enough information to answer the user, provide your final answer directly. If you still need more information, call another tool.",
		"If you have enough information to answer the user, provide your final answer directly If you still need more information, call another tool",
	}
	lower := strings.ToLower(text)
	for _, instr := range instructions {
		if idx := strings.Index(lower, strings.ToLower(instr)); idx != -1 {
			text = text[:idx] + text[idx+len(instr):]
			lower = strings.ToLower(text)
		}
	}
	return text
}

// stripThinkingLabels removes Chinese thinking section labels output by DeepSeek.
func stripThinkingLabels(text string) string {
	labels := []string{
		"思考过程", "思考", "分析过程", "推理过程",
		"Thought", "Thinking", "Analysis",
	}
	for _, label := range labels {
		// Match standalone label on its own line (with optional colon/suffix)
		text = strings.Replace(text, label+"\n", "\n", -1)
		text = strings.Replace(text, label+"：\n", "\n", -1)
		text = strings.Replace(text, label+":\n", "\n", -1)
		// Match label at start of text
		if strings.HasPrefix(text, label+"\n") || strings.HasPrefix(text, label+"：") || strings.HasPrefix(text, label+":") {
			idx := strings.Index(text, "\n")
			if idx >= 0 {
				text = text[idx+1:]
			}
		}
	}
	return text
}

// stripEchoArtifacts removes DeepSeek echo artifacts where the model
// repeats words (e.g. "我们尝试尝试", "好的，,") due to THINK/RESPONSE overlap.
func stripEchoArtifacts(text string) string {
	// Pattern: Chinese char repeated twice (e.g. 尝试尝试 → 尝试)
	// Match: a 2+ char CJK word immediately repeated
	runes := []rune(text)
	var result []rune
	i := 0
	for i < len(runes) {
		// Try to detect repeated CJK word of length 2-4
		matched := false
		for wordLen := 4; wordLen >= 2 && wordLen <= (len(runes)-i)/2; wordLen-- {
			end := i + wordLen
			if end > len(runes) {
				break
			}
			repeatEnd := end + wordLen
			if repeatEnd > len(runes) {
				continue
			}
			word := string(runes[i:end])
			repeat := string(runes[end:repeatEnd])
			if word == repeat && isCJKWord(word) {
				// Skip the repeated part, keep one copy
				result = append(result, runes[i:end]...)
				i = repeatEnd
				matched = true
				break
			}
		}
		if !matched {
			result = append(result, runes[i])
			i++
		}
	}
	text = string(result)

	// Fix doubled punctuation: "，," → "，", "。。" → "。"
	text = strings.ReplaceAll(text, "，,", "，")
	text = strings.ReplaceAll(text, ",，", "，")
	text = strings.ReplaceAll(text, "。。", "。")
	text = strings.ReplaceAll(text, "，，", "，")

	return text
}

// isCJKWord checks if a string consists primarily of CJK characters.
func isCJKWord(s string) bool {
	for _, r := range s {
		if !isCJK(r) {
			return false
		}
	}
	return len(s) > 0
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x3000 && r <= 0x303F) // CJK Symbols and Punctuation
}

func isAlpha(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isAlphaUnderscore(ch byte) bool {
	return isAlpha(ch) || ch == '_'
}

// emitProgress emits a subagent:progress event to the frontend via Wails.
func (s *Service) emitProgress(iteration int, agentType, toolName string, toolArgs map[string]interface{}, output string, isDone bool, err string) {
	if s.hasEmitter() {
		s.emit("subagent:progress", map[string]interface{}{
			"agentId":   "main",
			"agentType": agentType,
			"iteration": iteration,
			"toolName":  toolName,
			"toolArgs":  toolArgs,
			"output":    output,
			"isDone":    isDone,
			"error":     err,
		})
	}
}

// AgentRun executes the TAOR (Think-Act-Observe-Repeat) loop.
// It sends the user's message, lets the model think and use tools,
// and iterates until the model produces a final text response or
// reaches maxIterations.
func (s *Service) AgentRun(userMessage string, rootPath string, maxIterations int) AgentRunResult {
	ctx, requestID := s.beginRequest()
	defer s.endRequest(requestID)
	s.SetRootPath(rootPath)
	if maxIterations <= 0 {
		maxIterations = 20
	}

	systemPrompt := fmt.Sprintf(`你是天蚕 AI 编程助手，一个专业的编程 AI Agent。

你在一个 IDE 环境中工作，当前项目根目录是: %s

%s

工作流程：
1. 理解用户需求
2. 如果需要信息，先使用工具获取
3. 如果需要修改文件，使用 write_file
4. 如果需要执行命令，使用 run_bash
5. 完成后，用自然语言总结你做了什么

重要规则：
- 每次只调用一个工具
- 等待工具结果后再决定下一步
- 不要猜测文件内容，先用 read_file 查看
- 修改代码前先读取原文件
- 完成任务后直接回复用户，不要再调用工具`, rootPath, s.toolDescriptions(rootPath))
	systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	result := AgentRunResult{Messages: messages}

	for i := 0; i < maxIterations; i++ {
		if ctx.Err() != nil {
			result.Error = "已停止生成"
			result.Done = true
			return result
		}
		result.Iterations = i + 1

		// Call LM Studio
		resp, err := s.Chat(messages)
		if err != nil {
			result.Error = fmt.Sprintf("AI 请求失败: %v", err)
			result.Done = true
			s.emitProgress(i+1, "local", "", nil, "", true, result.Error)
			return result
		}

		// Add assistant response to history
		messages = append(messages, ChatMessage{Role: "assistant", Content: resp})

		// Check if the response contains a tool call
		toolName, toolArgs, hasTool := parseToolCall(resp)
		if !hasTool {
			// No tool call — the model is done, return final response
			result.Done = true
			result.Messages = messages
			s.emitProgress(i+1, "local", "", nil, resp, true, "")
			return result
		}

		// Execute the tool
		s.emitProgress(i+1, "local", toolName, toolArgs, "", false, "")
		toolResult := s.ExecuteTool(toolName, toolArgs, rootPath)
		s.emitProgress(i+1, "local", toolName, toolArgs, toolResult, false, "")

		// Add tool result as user message (observation)
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult),
		})
	}

	// Reached max iterations
	result.Done = false
	result.Messages = messages
	result.Error = fmt.Sprintf("达到最大迭代次数 %d", maxIterations)
	s.emitProgress(maxIterations, "local", "", nil, "", true, result.Error)
	return result
}

// AgentRunAPI executes the TAOR loop using a remote API provider.
func (s *Service) AgentRunAPI(userMessage string, rootPath string, maxIterations int) AgentRunResult {
	ctx, requestID := s.beginRequest()
	defer s.endRequest(requestID)
	s.SetRootPath(rootPath)
	if maxIterations <= 0 {
		maxIterations = 20
	}

	systemPrompt := fmt.Sprintf(`你是天蚕 AI 编程助手，一个专业的编程 AI Agent。

你在一个 IDE 环境中工作，当前项目根目录是: %s

%s

工作流程：
1. 理解用户需求
2. 如果需要信息，先使用工具获取
3. 如果需要修改文件，使用 write_file
4. 如果需要执行命令，使用 run_bash
5. 完成后，用自然语言总结你做了什么

重要规则：
- 每次只调用一个工具
- 等待工具结果后再决定下一步
- 不要猜测文件内容，先用 read_file 查看
- 修改代码前先读取原文件
- 完成任务后直接回复用户，不要再调用工具`, rootPath, s.toolDescriptions(rootPath))
	systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	result := AgentRunResult{Messages: messages}

	for i := 0; i < maxIterations; i++ {
		if ctx.Err() != nil {
			result.Error = "已停止生成"
			result.Done = true
			return result
		}
		result.Iterations = i + 1

		// Call remote API provider
		resp, err := s.ChatAPI(messages)
		if err != nil {
			result.Error = fmt.Sprintf("API 请求失败: %v", err)
			result.Done = true
			s.emitProgress(i+1, "api", "", nil, "", true, result.Error)
			return result
		}

		messages = append(messages, ChatMessage{Role: "assistant", Content: resp})

		toolName, toolArgs, hasTool := parseToolCall(resp)
		if !hasTool {
			result.Done = true
			result.Messages = messages
			s.emitProgress(i+1, "api", "", nil, resp, true, "")
			return result
		}

		s.emitProgress(i+1, "api", toolName, toolArgs, "", false, "")
		toolResult := s.ExecuteTool(toolName, toolArgs, rootPath)
		s.emitProgress(i+1, "api", toolName, toolArgs, toolResult, false, "")

		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult),
		})
	}

	result.Done = false
	result.Messages = messages
	result.Error = fmt.Sprintf("达到最大迭代次数 %d", maxIterations)
	s.emitProgress(maxIterations, "api", "", nil, "", true, result.Error)
	return result
}

// ── Smart Router Integration ────────────────────────────────────────

// SmartRouteResult is returned to the frontend when using auto-routing.
type SmartRouteResult struct {
	Tier       string  `json:"tier"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Fallback   bool    `json:"fallback"`
}

// SmartRoute queries the SmartRouter for the best tier for a given message.
// The frontend can call this to decide which source to use, or use SmartAgentRun
// to let the backend auto-route and execute.
func (s *Service) SmartRoute(userMessage string) SmartRouteResult {
	if s.Router == nil {
		return SmartRouteResult{Tier: "local", Reason: "router not initialized"}
	}
	rr := s.Router.Route(userMessage)
	return SmartRouteResult{
		Tier:       string(rr.Tier),
		Category:   string(rr.Category),
		Confidence: rr.Confidence,
		Reason:     rr.Reason,
		Fallback:   rr.Fallback,
	}
}

// SmartAgentRun uses the SmartRouter to auto-select the best tier,
// then executes the agent loop on that tier. If the selected tier fails,
// it records the failure and falls back to the next tier.
func (s *Service) SmartAgentRun(userMessage string, rootPath string, maxIterations int) AgentRunResult {
	s.SetRootPath(rootPath)
	if maxIterations <= 0 {
		maxIterations = 20
	}

	if s.Router == nil {
		// No router — fall back to local
		return s.AgentRun(userMessage, rootPath, maxIterations)
	}

	// Refresh hardware state before routing
	s.Router.RefreshHardwareState()

	rr := s.Router.Route(userMessage)
	tier := rr.Tier

	// Emit routing decision to frontend
	if s.hasEmitter() {
		s.emit("router:decision", SmartRouteResult{
			Tier:       string(rr.Tier),
			Category:   string(rr.Category),
			Confidence: rr.Confidence,
			Reason:     rr.Reason,
			Fallback:   rr.Fallback,
		})
	}

	// Try the routed tier
	result := s.runOnTier(tier, userMessage, rootPath, maxIterations)
	if isStoppedError(result.Error) {
		return result
	}
	if result.Error == "" {
		return result
	}

	// Record failure and try fallback tiers
	originalErr := result.Error
	s.Router.RecordFailure(tier, originalErr)
	for _, fallback := range s.Router.GetConfig().FallbackOrder {
		if fallback == tier {
			continue
		}
		if s.Router.HasRecentFailure(fallback) {
			continue
		}

		result = s.runOnTier(fallback, userMessage, rootPath, maxIterations)
		if isStoppedError(result.Error) {
			return result
		}
		if result.Error == "" {
			if s.hasEmitter() {
				s.emit("router:fallback", map[string]string{
					"from":   string(tier),
					"to":     string(fallback),
					"reason": originalErr,
				})
			}
			return result
		}
		s.Router.RecordFailure(fallback, result.Error)
	}

	// All tiers failed — return last error
	return result
}

func isStoppedError(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(message, "已停止生成") || strings.Contains(lower, "context canceled") || strings.Contains(lower, "context cancelled")
}

// SmartAgentRunV2 使用增强版 AgentV2 执行任务
// AgentV2 包含：增强记忆、高级压缩、Rollback、并行执行、MCP 集成
func (s *Service) SmartAgentRunV2(userMessage string, rootPath string, maxIterations int) AgentRunResultV2 {
	ctx, requestID := s.beginRequest()
	defer s.endRequest(requestID)
	s.SetRootPath(rootPath)
	if maxIterations <= 0 {
		maxIterations = 20
	}
	userMessageWithMemory := s.withProjectMemory(rootPath, userMessage, userMessage)

	// 创建 AgentV2 实例，注入 LLM 调用函数
	// AgentV2.NewAgentV2 接收 compact.ChatFunc: func(prompt string) (string, error)
	chatFn := func(prompt string) (string, error) {
		messages := []ChatMessage{{Role: "user", Content: prompt}}
		return s.ChatWithModel(messages)
	}

	agentChatFn := func(systemPrompt string, msgs []agent.Message) (string, error) {
		messages := agentMessagesToChatMessages(systemPrompt, msgs)
		response, err := s.ChatWithModel(messages)
		if err == nil && len(parser.ParseToolCalls(response)) > 0 && s.hasEmitter() {
			if thinking := extractVisibleThinking(response); thinking != "" {
				s.emit("ai:reasoning", map[string]string{"content": thinking})
			}
		}
		return response, err
	}

	// 创建 AgentV2
	agentV2 := agent.NewAgentV2(rootPath, chatFn)
	agentV2.SetProgressCallback(func(iteration int, toolName string, toolArgs map[string]interface{}, output string, isDone bool, err string) {
		s.emitProgress(iteration, "auto", toolName, toolArgs, output, isDone, err)
	})

	// 分析任务类型
	analysis := agentV2.GetAnalysis(userMessageWithMemory)

	// 统一走标准 V2 工具循环，确保所有 provider 都能持续收到工具执行结果。
	// Parallel/Ralph V2 目前只完成了单轮工具执行，不适合作为 Auto 默认路径。
	result := agentV2.RunV2(ctx, userMessageWithMemory, maxIterations, agentChatFn)

	// 转换结果
	return AgentRunResultV2{
		Messages:    result.Messages,
		Done:        result.Done,
		Error:       result.Error,
		DurationMs:  result.DurationMs,
		AgentType:   result.AgentType,
		TotalTokens: result.TotalTokens,
		HasRollback: true, // AgentV2 总是支持 rollback
		TaskAnalysis: TaskAnalysisV2{
			PrimaryRole:    string(analysis.PrimaryRole),
			Complexity:     string(analysis.Complexity),
			EstimatedTurns: analysis.EstimatedTurns,
			IsRoutable:     analysis.IsRoutable,
			SubtaskCount:   len(analysis.Subtasks),
		},
	}
}

func agentMessagesToChatMessages(systemPrompt string, msgs []agent.Message) []ChatMessage {
	messages := []ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case agent.RoleSystem:
			messages = append(messages, ChatMessage{Role: "system", Content: content})
		case agent.RoleAssistant:
			messages = append(messages, ChatMessage{Role: "assistant", Content: content})
		case agent.RoleTool:
			name := strings.TrimSpace(m.ToolName)
			if name == "" {
				name = "tool"
			}
			messages = append(messages, ChatMessage{
				Role:    "user",
				Content: fmt.Sprintf("工具 %s 执行结果:\n%s", name, content),
			})
		default:
			messages = append(messages, ChatMessage{Role: "user", Content: content})
		}
	}
	return messages
}

// AgentRunResultV2 是 AgentV2 执行结果的返回类型
type AgentRunResultV2 struct {
	Messages     []agent.Message `json:"messages"`
	Done         bool            `json:"done"`
	Error        string          `json:"error,omitempty"`
	DurationMs   int64           `json:"durationMs"`
	AgentType    string          `json:"agentType,omitempty"`
	TotalTokens  int             `json:"totalTokens"`
	HasRollback  bool            `json:"hasRollback"`
	TaskAnalysis TaskAnalysisV2  `json:"taskAnalysis,omitempty"`
}

// TaskAnalysisV2 包含 AgentV2 的任务分析结果
type TaskAnalysisV2 struct {
	PrimaryRole    string `json:"primaryRole"`
	Complexity     string `json:"complexity"`
	EstimatedTurns int    `json:"estimatedTurns"`
	IsRoutable     bool   `json:"isRoutable"`
	SubtaskCount   int    `json:"subtaskCount"`
}

// shouldUseRalphLoop 判断是否应该使用 Ralph 自纠错循环
func shouldUseRalphLoop(task string) bool {
	if envconfig.EnvBool("TIANCAN_DISABLE_RALPH") {
		return false
	}
	indicators := []string{
		"implement", "refactor", "migrate", "fix bug", "add feature",
		"create", "build", "port", "debug", "optimize",
		"实现", "重构", "迁移", "修复", "添加", "创建", "构建", "移植", "调试", "优化",
	}
	lower := strings.ToLower(task)
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// ChatWithModel 是内部方法，用于 AgentV2 的 LLM 调用
func (s *Service) ChatWithModel(messages []ChatMessage) (string, error) {
	var attempts []string
	deadline := time.Now().Add(240 * time.Second)
	callCandidate := func(limit time.Duration, fn func(context.Context) (string, error)) (string, error) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("Auto 模型选择总超时")
		}
		if limit > remaining {
			limit = remaining
		}
		return callLLMWithTimeout(s.currentRequestContext(), limit, fn)
	}

	s.mu.Lock()
	apiProvider := s.provider
	s.mu.Unlock()
	if apiProvider.BaseURL != "" && apiProvider.APIKey != "" && apiProvider.Model != "" {
		s.emitLLMProviderEvent("attempt", "API", "正在请求已配置的 API")
		resp, err := callCandidate(45*time.Second, func(_ context.Context) (string, error) {
			return s.ChatAPI(messages)
		})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "API", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "API", formatLLMAttemptError("API", resp, err))
		attempts = append(attempts, formatLLMAttemptError("API", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "API", "未配置可用 API")
		attempts = append(attempts, "API 未配置")
	}

	var resp string
	var err error
	s.emitLLMProviderEvent("attempt", "OpenRelay", "正在尝试 OpenRelay，并通过天蚕 Agent 工具层执行项目操作")
	resp, err = callCandidate(60*time.Second, func(ctx context.Context) (string, error) {
		return s.chatOpenRelay(ctx, messages)
	})
	if err == nil && strings.TrimSpace(resp) != "" {
		s.emitLLMProviderEvent("success", "OpenRelay", "已返回响应")
		return resp, nil
	}
	s.emitLLMProviderEvent("error", "OpenRelay", formatLLMAttemptError("OpenRelay", resp, err))
	attempts = append(attempts, formatLLMAttemptError("OpenRelay", resp, err))

	s.emitLLMProviderEvent("attempt", "Claude Code / Codex", "正在尝试本地 CLI")
	resp, err = callCandidate(200*time.Second, func(ctx context.Context) (string, error) {
		return s.chatLocalCLI(ctx, messages)
	})
	if err == nil && strings.TrimSpace(resp) != "" {
		return resp, nil
	}
	attempts = append(attempts, formatLLMAttemptError("本地 CLI Provider", resp, err))

	if s.isServerRunning() {
		s.emitLLMProviderEvent("attempt", "LM Studio", "正在连接本地模型")
		resp, err = callCandidate(45*time.Second, func(_ context.Context) (string, error) {
			return s.Chat(messages)
		})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "LM Studio", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "LM Studio", formatLLMAttemptError("LM Studio", resp, err))
		attempts = append(attempts, formatLLMAttemptError("本地 LM Studio", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "LM Studio", "本地服务未启动")
		attempts = append(attempts, "本地 LM Studio 未启动")
	}

	if s.GetWebAuth() != "" {
		s.emitLLMProviderEvent("attempt", "Web AI", "正在尝试网页登录态，并通过天蚕 Agent 工具层执行项目操作")
		resp, err = callCandidate(45*time.Second, func(_ context.Context) (string, error) {
			webResp, _, callErr := s.chatWebConv(chatMessagesToWebPrompt(messages), "", "")
			return webResp, callErr
		})
		if err == nil && strings.TrimSpace(resp) != "" {
			s.emitLLMProviderEvent("success", "Web AI", "已返回响应")
			return resp, nil
		}
		s.emitLLMProviderEvent("error", "Web AI", formatLLMAttemptError("Web AI", resp, err))
		attempts = append(attempts, formatLLMAttemptError("Web", resp, err))
	} else {
		s.emitLLMProviderEvent("skip", "Web AI", "未登录")
		attempts = append(attempts, "Web 未登录")
	}

	return "", fmt.Errorf("所有 LLM 均不可用：%s", strings.Join(attempts, "；"))
}

func callLLMWithTimeout(parent context.Context, timeout time.Duration, fn func(context.Context) (string, error)) (string, error) {
	type result struct {
		resp string
		err  error
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ch := make(chan result, 1)
	go func() {
		resp, err := fn(ctx)
		ch <- result{resp: resp, err: err}
	}()
	select {
	case res := <-ch:
		return res.resp, res.err
	case <-ctx.Done():
		if ctx.Err() == context.Canceled {
			return "", fmt.Errorf("已停止生成")
		}
		return "", fmt.Errorf("尝试超时（%s）", timeout.String())
	}
}

func formatLLMAttemptError(name string, resp string, err error) string {
	if err != nil {
		return fmt.Sprintf("%s 失败: %v", name, err)
	}
	if strings.TrimSpace(resp) == "" {
		return fmt.Sprintf("%s 失败: 空响应", name)
	}
	return fmt.Sprintf("%s 失败: 未知错误", name)
}

func chatMessagesToWebPrompt(messages []ChatMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		sb.WriteString("[")
		sb.WriteString(role)
		sb.WriteString("]: ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// SmartAgentStreamV2 使用增强版 AgentV2 的流式执行
func (s *Service) SmartAgentStreamV2(userMessage string, rootPath string, maxIterations int) (<-chan AgentStreamEvent, error) {
	// 对于流式执行，暂时回退到标准 AgentStream
	// AgentV2 的流式支持需要更多集成工作
	return s.AgentStream(userMessage, rootPath, maxIterations)
}

// runOnTier executes the agent loop on a specific tier.
// Always uses AgentV2 with intelligent routing - it automatically selects the best execution strategy.
func (s *Service) runOnTier(tier router.ModelTier, userMessage string, rootPath string, maxIterations int) AgentRunResult {
	// AgentV2 with SmartAgentRunV2 handles all routing internally:
	// - Parallel execution for routable tasks
	// - Ralph V2 self-correction for complex coding tasks
	// - Standard V2 loop for simple tasks
	resultV2 := s.SmartAgentRunV2(userMessage, rootPath, maxIterations)

	// 转换 agent.Message 到 ChatMessage
	messages := make([]ChatMessage, len(resultV2.Messages))
	for i, m := range resultV2.Messages {
		messages[i] = ChatMessage{Role: string(m.Role), Content: m.Content}
	}
	return AgentRunResult{
		Messages:    messages,
		Done:        resultV2.Done,
		Error:       resultV2.Error,
		DurationMs:  resultV2.DurationMs,
		AgentType:   resultV2.AgentType,
		TotalTokens: resultV2.TotalTokens,
	}
}

// SmartAgentStream uses the SmartRouter to auto-select the best tier for streaming.
func (s *Service) SmartAgentStream(userMessage string, rootPath string, maxIterations int) (<-chan AgentStreamEvent, error) {
	if s.Router == nil {
		return s.AgentStream(userMessage, rootPath, maxIterations)
	}

	s.Router.RefreshHardwareState()
	rr := s.Router.Route(userMessage)

	// Emit routing decision
	if s.hasEmitter() {
		s.emit("router:decision", SmartRouteResult{
			Tier:       string(rr.Tier),
			Category:   string(rr.Category),
			Confidence: rr.Confidence,
			Reason:     rr.Reason,
			Fallback:   rr.Fallback,
		})
	}

	// For streaming, we use the selected tier directly
	// (fallback on error is handled by the frontend re-calling with a different source)
	switch rr.Tier {
	case router.TierAPI:
		return s.agentStreamAPI(userMessage, rootPath, maxIterations)
	case router.TierWeb:
		return s.agentStreamWeb(userMessage, rootPath, maxIterations)
	default:
		return s.AgentStream(userMessage, rootPath, maxIterations)
	}
}

// GetRouterConfig returns the current SmartRouter config for the frontend.
func (s *Service) GetRouterConfig() router.SmartRouterConfig {
	if s.Router == nil {
		return router.DefaultConfig()
	}
	return s.Router.GetConfig()
}

// SetRouterConfig updates the SmartRouter config from the frontend.
func (s *Service) SetRouterConfig(cfg router.SmartRouterConfig) {
	if s.Router == nil {
		return
	}
	cfg.App = s.App
	s.Router.SetConfig(cfg)
}

// RefreshModelStatuses queries LM Studio for current models and updates the router.
func (s *Service) RefreshModelStatuses() {
	models, err := s.ListModels()
	if err != nil || len(models) == 0 {
		// LM Studio not running — all local models unavailable
		s.Router.UpdateModelStatuses(nil)
		return
	}

	cfg := s.Router.GetConfig()
	statuses := make([]router.LocalModelStatus, 0, len(models))
	for _, m := range models {
		ms := router.LocalModelStatus{
			ID:        m.ID,
			Available: true, // assume available unless hardware check says otherwise
		}
		// Estimate model size from ID heuristics (e.g. "qwen2.5-7b" → ~7B params → ~4GB)
		paramsGB, ramGB := estimateModelResources(m.ID)
		ms.ParamsB = paramsGB
		ms.SizeGB = ramGB
		ms.MinRAMGB = ramGB * 1.3 // 30% overhead for KV cache
		if ms.MinRAMGB > 0 && cfg.AvailableMemoryGB > 0 && ms.MinRAMGB > cfg.AvailableMemoryGB {
			ms.Available = false
			ms.DisableReason = fmt.Sprintf("需要 %.1fGB 内存，当前可用 %.1fGB", ms.MinRAMGB, cfg.AvailableMemoryGB)
		}
		statuses = append(statuses, ms)
	}
	s.Router.UpdateModelStatuses(statuses)
}

// estimateModelResources tries to estimate parameter count and RAM requirement from model ID.
func estimateModelResources(modelID string) (paramsB, ramGB float64) {
	// Common patterns: "7b", "13b", "34b", "70b", "0.5b", "1.5b", "3b"
	lower := strings.ToLower(modelID)
	// Try to find a pattern like "7b", "13b", etc.
	for _, suffix := range []string{"b", "B"} {
		for i := len(lower) - 1; i >= 0; i-- {
			if lower[i] == suffix[0] && i > 0 {
				// Scan backwards for digits and dots
				j := i - 1
				for j >= 0 && (lower[j] >= '0' && lower[j] <= '9' || lower[j] == '.') {
					j--
				}
				numStr := lower[j+1 : i]
				if v, err := strconv.ParseFloat(numStr, 64); err == nil && v > 0 {
					paramsB = v
					// Rough RAM estimate: params * 2 bytes (fp16) / 1e9 → GB
					// Quantized models use less; assume Q4 → ~0.6 bytes/param
					ramGB = v * 0.6
					return
				}
			}
		}
	}
	return 0, 0
}

// agentStreamAPI runs the streaming agent loop using the API provider.
func (s *Service) agentStreamAPI(userMessage string, rootPath string, maxIterations int) (<-chan AgentStreamEvent, error) {
	s.SetRootPath(rootPath)
	ch := make(chan AgentStreamEvent, 256)

	go func() {
		defer close(ch)
		if maxIterations <= 0 {
			maxIterations = 20
		}

		systemPrompt := fmt.Sprintf(`你是天蚕 AI 编程助手，一个专业的编程 AI Agent。

你在一个 IDE 环境中工作，当前项目根目录是: %s

%s

工作流程：
1. 理解用户需求
2. 如果需要信息，先使用工具获取
3. 如果需要修改文件，使用 write_file
4. 如果需要执行命令，使用 run_bash
5. 完成后，用自然语言总结你做了什么

重要规则：
- 每次只调用一个工具
- 等待工具结果后再决定下一步
- 不要猜测文件内容，先用 read_file 查看
- 修改代码前先读取原文件
- 完成任务后直接回复用户，不要再调用工具`, rootPath, s.toolDescriptions(rootPath))
		systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)

		messages := []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		}

		for i := 0; i < maxIterations; i++ {
			resp, err := s.ChatAPI(messages)
			if err != nil {
				ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("API 请求失败: %v", err)}
				return
			}

			messages = append(messages, ChatMessage{Role: "assistant", Content: resp})
			ch <- AgentStreamEvent{Type: "token", Content: resp}

			toolName, toolArgs, hasTool := parseToolCall(resp)
			if !hasTool {
				ch <- AgentStreamEvent{Type: "done", Content: resp}
				return
			}

			argsJSON, _ := json.Marshal(toolArgs)
			ch <- AgentStreamEvent{Type: "tool_call", Name: toolName, Content: string(argsJSON)}

			toolResult := s.ExecuteTool(toolName, toolArgs, rootPath)
			ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: toolResult}

			messages = append(messages, ChatMessage{
				Role:    "user",
				Content: fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult),
			})
		}
		ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("达到最大迭代次数 %d", maxIterations)}
	}()

	return ch, nil
}

// agentStreamWeb runs the streaming agent loop using the web provider.
func (s *Service) agentStreamWeb(userMessage string, rootPath string, maxIterations int) (<-chan AgentStreamEvent, error) {
	s.SetRootPath(rootPath)
	ch := make(chan AgentStreamEvent, 256)

	go func() {
		defer close(ch)
		if maxIterations <= 0 {
			maxIterations = 20
		}

		toolDescs := s.toolDescriptions(rootPath)
		prompt := fmt.Sprintf(`你是天蚕 AI 编程助手。当前项目根目录: %s

%s

工作流程：理解需求→使用工具→执行命令→总结结果
重要规则：每次只调用一个工具，等待结果后再决定下一步。`, rootPath, toolDescs)

		convID := s.webConvID
		lastActions := ""

		for i := 0; i < maxIterations; i++ {
			var userMsg string
			if i == 0 {
				userMsg = prompt + "\n\n用户请求: " + userMessage
			} else {
				userMsg = lastActions
			}

			resp, newConvID, err := s.chatWebConv(userMsg, convID, "")
			if err != nil {
				ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("Web 请求失败: %v", err)}
				return
			}
			if newConvID != "" {
				convID = newConvID
				s.webConvID = convID
			}

			// Strip thinking artifacts
			resp = stripThinkingArtifacts(resp)

			ch <- AgentStreamEvent{Type: "token", Content: resp}

			toolName, toolArgs, hasTool := parseToolCall(resp)
			if !hasTool {
				ch <- AgentStreamEvent{Type: "done", Content: resp}
				return
			}

			argsJSON, _ := json.Marshal(toolArgs)
			ch <- AgentStreamEvent{Type: "tool_call", Name: toolName, Content: string(argsJSON)}

			toolResult := s.ExecuteTool(toolName, toolArgs, rootPath)
			ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: toolResult}

			lastActions = fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult)
		}
		ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("达到最大迭代次数 %d", maxIterations)}
	}()

	return ch, nil
}

// ── Streaming Agent Run (for UI) ──────────────────────────────────

// AgentStreamEvent represents a streaming event from the agent loop.
type AgentStreamEvent struct {
	Type     string `json:"type"`               // "token", "tool_call", "tool_result", "permission_request", "done", "error"
	Content  string `json:"content"`            // content depends on type
	Name     string `json:"name,omitempty"`     // tool name for tool_call/tool_result
	Thinking string `json:"thinking,omitempty"` // model-provided reasoning text before a tool call
	ID       string `json:"id,omitempty"`       // unique ID for permission_request correlation
	Reason   string `json:"reason,omitempty"`   // reason for permission request
}

// pendingPermission holds a channel that blocks until the user responds.
type pendingPermission struct {
	response chan bool
}

// permissionMu guards the pendingPermissions map.
var (
	pendingPermissions   = make(map[string]*pendingPermission)
	pendingPermissionsMu sync.Mutex
)

// adaptTool adapts a tool name + readOnly flag to the agenttypes.Tool interface
// so PermissionManager.Check can be called without a full tool struct.
type adaptTool struct {
	toolName string
	readOnly bool
}

func (t adaptTool) Name() string                        { return t.toolName }
func (t adaptTool) Description() string                 { return "" }
func (t adaptTool) InputSchema() map[string]interface{} { return nil }
func (t adaptTool) Execute(_ context.Context, _ map[string]interface{}) (agenttypes.ToolResult, error) {
	return agenttypes.ToolResult{}, nil
}
func (t adaptTool) IsReadOnly() bool        { return t.readOnly }
func (t adaptTool) IsConcurrencySafe() bool { return true }

// isReadOnlyTool returns true for tools that don't modify the filesystem.
func isReadOnlyTool(name string) bool {
	switch name {
	case "read_file", "list_directory", "search_files", "grep_files",
		"get_directory_tree", "file_exists", "get_working_directory":
		return true
	default:
		return false
	}
}

// RespondPermission allows the frontend to approve or deny a pending permission request.
func (s *Service) RespondPermission(requestID string, approved bool) {
	pendingPermissionsMu.Lock()
	pp, ok := pendingPermissions[requestID]
	if ok {
		delete(pendingPermissions, requestID)
	}
	pendingPermissionsMu.Unlock()
	if ok && pp != nil {
		pp.response <- approved
	}
}

// AgentStream runs the TAOR loop and streams events via a channel.
func (s *Service) AgentStream(userMessage string, rootPath string, maxIterations int) (<-chan AgentStreamEvent, error) {
	ctx, requestID := s.beginRequest()
	s.SetRootPath(rootPath)
	ch := make(chan AgentStreamEvent, 256)

	go func() {
		defer close(ch)
		defer s.endRequest(requestID)

		if maxIterations <= 0 {
			maxIterations = 20
		}

		systemPrompt := fmt.Sprintf(`你是天蚕 AI 编程助手，一个专业的编程 AI Agent。

你在一个 IDE 环境中工作，当前项目根目录是: %s

%s

工作流程：
1. 理解用户需求
2. 如果需求不明确，先向用户提问澄清，不要猜测
3. 如果需要信息，先使用工具获取
4. 如果需要修改文件，使用 write_file
5. 如果需要执行命令，使用 run_bash
6. 完成后，用自然语言总结你做了什么

重要规则：
- 每次只调用一个工具
- 等待工具结果后再决定下一步
- 不要猜测文件内容，先用 read_file 查看
- 修改代码前先读取原文件
- 完成任务后直接回复用户，不要再调用工具

意图澄清规则：
- 如果用户的需求模糊、有歧义或缺少关键信息，你必须先向用户提问澄清，而不是猜测意图
- 需要澄清的情况包括但不限于：
  * 用户提到"那个文件"但没有指定具体文件
  * 用户要求修改但未说明修改什么
  * 用户的需求可能有多种理解方式
  * 涉及删除或覆盖操作但未确认目标
- 澄清时，给出具体的选项让用户选择，例如"你是指 A 还是 B？"
- 如果需求足够明确，直接执行，不要不必要地提问`, rootPath, s.toolDescriptions(rootPath))
		systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)

		messages := []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		}

		for i := 0; i < maxIterations; i++ {
			if ctx.Err() != nil {
				ch <- AgentStreamEvent{Type: "cancelled", Content: "已停止生成"}
				return
			}
			// Streaming chat call
			var fullResp strings.Builder
			_, err := s.ChatStream(messages, func(token string) {
				fullResp.WriteString(token)
				ch <- AgentStreamEvent{Type: "token", Content: token}
			})
			if err != nil {
				ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("AI 请求失败: %v", err)}
				return
			}

			resp := fullResp.String()
			messages = append(messages, ChatMessage{Role: "assistant", Content: resp})

			// Check for tool call
			toolName, toolArgs, hasTool := parseToolCall(resp)
			if !hasTool {
				// Done — final response already streamed
				s.emitProgress(i+1, "local", "", nil, resp, true, "")
				ch <- AgentStreamEvent{Type: "done", Content: resp}
				return
			}

			// Notify UI about tool call
			argsJSON, _ := json.Marshal(toolArgs)
			ch <- AgentStreamEvent{Type: "tool_call", Name: toolName, Content: string(argsJSON), Thinking: extractVisibleThinking(resp)}

			// Permission check: ask user if needed
			pm := safety.NewPermissionManager(rootPath, safety.ModeDefault)
			permResult := pm.Check(adaptTool{toolName: toolName, readOnly: isReadOnlyTool(toolName)}, toolArgs)
			if permResult.Decision == agenttypes.DecisionAsk {
				reqID := fmt.Sprintf("perm_%d_%d", time.Now().UnixNano(), i)
				respCh := make(chan bool, 1)
				pendingPermissionsMu.Lock()
				pendingPermissions[reqID] = &pendingPermission{response: respCh}
				pendingPermissionsMu.Unlock()

				ch <- AgentStreamEvent{
					Type:    "permission_request",
					ID:      reqID,
					Name:    toolName,
					Content: string(argsJSON),
					Reason:  permResult.Reason,
				}

				// Block until user responds (with timeout to prevent goroutine leak)
				var approved bool
				select {
				case approved = <-respCh:
				case <-ctx.Done():
					pendingPermissionsMu.Lock()
					delete(pendingPermissions, reqID)
					pendingPermissionsMu.Unlock()
					ch <- AgentStreamEvent{Type: "cancelled", Content: "已停止生成"}
					return
				case <-time.After(5 * time.Minute):
					approved = false
					pendingPermissionsMu.Lock()
					delete(pendingPermissions, reqID)
					pendingPermissionsMu.Unlock()
					ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: "⏰ 权限请求超时，已自动拒绝"}
				}
				if !approved {
					ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: "❌ 用户拒绝了此操作"}
					messages = append(messages, ChatMessage{
						Role:    "user",
						Content: fmt.Sprintf("工具 %s 被用户拒绝: %s", toolName, permResult.Reason),
					})
					continue
				}
			} else if permResult.Decision == agenttypes.DecisionDeny {
				ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: fmt.Sprintf("❌ 操作被拒绝: %s", permResult.Reason)}
				messages = append(messages, ChatMessage{
					Role:    "user",
					Content: fmt.Sprintf("工具 %s 被拒绝: %s", toolName, permResult.Reason),
				})
				continue
			}

			// Execute tool
			s.emitProgress(i+1, "local", toolName, toolArgs, "", false, "")
			toolResult := s.ExecuteTool(toolName, toolArgs, rootPath)
			s.emitProgress(i+1, "local", toolName, toolArgs, toolResult, false, "")
			ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: toolResult}

			// Add observation
			messages = append(messages, ChatMessage{
				Role:    "user",
				Content: fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult),
			})
		}

		ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("达到最大迭代次数 %d", maxIterations)}
		s.emitProgress(maxIterations, "local", "", nil, "", true, fmt.Sprintf("达到最大迭代次数 %d", maxIterations))
	}()

	return ch, nil
}

// StartAgentStream starts the streaming agent loop and pushes events to the
// frontend via Wails Events. The frontend should listen on the "agent:stream" event.
// This method returns immediately; events are delivered asynchronously.
func (s *Service) StartAgentStream(userMessage string, rootPath string, maxIterations int) error {
	ch, err := s.AgentStream(userMessage, rootPath, maxIterations)
	if err != nil {
		return err
	}
	go func() {
		for evt := range ch {
			if s.hasEmitter() {
				s.emit("agent:stream", evt)
			}
		}
		// Signal completion so the frontend knows the stream ended
		if s.hasEmitter() {
			s.emit("agent:stream", AgentStreamEvent{Type: "stream_end"})
		}
	}()
	return nil
}

// ── HTTP helpers ──────────────────────────────────────────────────

// fetchFastest tries multiple URLs and returns the first successful response.
func fetchFastest(urls []string) ([]byte, string, error) {
	type result struct {
		data []byte
		src  string
		err  error
	}
	ch := make(chan result, len(urls))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	for _, u := range urls {
		go func(url string) {
			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				ch <- result{err: err}
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				ch <- result{err: err}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				ch <- result{err: fmt.Errorf("HTTP %d", resp.StatusCode)}
				return
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				ch <- result{err: err}
				return
			}
			ch <- result{data: data, src: url}
		}(u)
	}

	var firstErr error
	for i := 0; i < len(urls); i++ {
		r := <-ch
		if r.err == nil {
			return r.data, r.src, nil
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}
	return nil, "", firstErr
}

// ── Web AI Mode (zero-token web providers) ──────────────────────────

// WebProviderInfo is a simplified provider info for the frontend.
type WebProviderInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	LoginURL    string `json:"loginUrl"`
}

// ListWebProviders returns the list of available web AI providers.
func (s *Service) ListWebProviders() []WebProviderInfo {
	all := types.AllProviders()
	result := make([]WebProviderInfo, len(all))
	for i, p := range all {
		result[i] = WebProviderInfo{
			ID:          p.ID,
			DisplayName: p.DisplayName,
			LoginURL:    p.LoginURL,
		}
	}
	return result
}

// LoginWeb launches a Playwright browser for the user to authenticate.
// It runs the login flow in a goroutine and emits events:
//   - webai:login-success → {provider, cookie, bearer, ...}
//   - webai:login-error   → {error: "..."}
//   - webai:login-fallback → opens default browser as fallback
func (s *Service) LoginWeb(providerID string) error {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return fmt.Errorf("未知的 Web 提供商: %s", providerID)
	}

	// Check if a local browser is available (rod drives Chrome/Chromium directly)
	if !browser.IsBrowserReady() {
		log.Printf("ai: no local browser available, opening default browser as fallback")
		// Fallback: open login URL in default browser
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", cfg.LoginURL)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", cfg.LoginURL)
		default:
			cmd = exec.Command("xdg-open", cfg.LoginURL)
		}
		cmd.SysProcAttr = hideWindowAttr()
		_ = cmd.Start()
		if s.hasEmitter() {
			s.emit("webai:login-fallback", map[string]string{"provider": providerID})
		}
		return nil
	}

	// Run Playwright login in a goroutine (blocks up to 5 minutes)
	go func() {
		authResult, err := browser.RunLogin(providerID)
		if err != nil {
			log.Printf("ai: Playwright login failed for %s: %v", providerID, err)
			if s.hasEmitter() {
				s.emit("webai:login-error", map[string]string{
					"provider": providerID,
					"error":    err.Error(),
				})
			}
			return
		}

		// Save auth to in-memory state
		s.mu.Lock()
		s.webAuth = authResult
		s.webID = providerID
		s.webConvID = ""
		s.mu.Unlock()

		// Persist auth to disk
		if err := tokenstore.SaveProviderToken(providerID, authResult); err != nil {
			log.Printf("ai: failed to persist token for %s: %v", providerID, err)
		}

		log.Printf("ai: login complete for %s (cookie_len=%d bearer_len=%d)",
			providerID, len(authResult.Cookie), len(authResult.Bearer))

		// Emit success event
		if s.hasEmitter() {
			s.emit("webai:login-success", map[string]interface{}{
				"provider":     providerID,
				"cookie":       authResult.Cookie,
				"bearer":       authResult.Bearer,
				"access_token": authResult.AccessToken,
				"user_agent":   authResult.UserAgent,
				"extra":        authResult.Extra,
			})
		}
	}()

	return nil
}

// SetWebAuth saves the user-provided auth credentials for a web provider.
// The user copies cookies from the browser after login.
func (s *Service) SetWebAuth(providerID string, cookie string, userAgent string) error {
	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return fmt.Errorf("未知的 Web 提供商: %s", providerID)
	}
	auth := providers.LoginWithBrowser(providerID, cookie, userAgent, map[string]string{})
	if !auth.IsAuthenticated() {
		return fmt.Errorf("认证信息不足：请确保复制了完整的 Cookie 字符串")
	}
	s.mu.Lock()
	s.webAuth = auth
	s.webID = providerID
	s.webConvID = "" // reset conversation when switching providers
	s.mu.Unlock()

	// Persist to disk
	if err := tokenstore.SaveProviderToken(providerID, auth); err != nil {
		log.Printf("ai: failed to persist token for %s: %v", providerID, err)
	}
	log.Printf("ai: web auth saved for %s", providerID)
	return nil
}

// GetWebAuth returns the current web provider ID (or empty if not set).
// Auto-loads persisted auth from disk if not already in memory.
func (s *Service) GetWebAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.webAuth != nil && s.webAuth.IsAuthenticated() {
		return s.webID
	}
	// Try loading from disk
	for _, pid := range tokenstore.ListStoredProviders() {
		if auth := tokenstore.GetProviderToken(pid); auth != nil && auth.IsAuthenticated() {
			s.webAuth = auth
			s.webID = pid
			log.Printf("ai: auto-loaded web auth for %s from disk", pid)
			return pid
		}
	}
	return ""
}

// IsWebProviderLoggedIn checks if a specific web provider has valid auth.
// Checks in-memory state first, then falls back to disk-persisted tokens.
func (s *Service) IsWebProviderLoggedIn(providerID string) bool {
	s.mu.Lock()
	// Check in-memory: if current provider matches and is authenticated
	if s.webID == providerID && s.webAuth != nil && s.webAuth.IsAuthenticated() {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	// Check disk
	auth := tokenstore.GetProviderToken(providerID)
	return auth != nil && auth.IsAuthenticated()
}

// GetLoggedInWebProviders returns all web providers that have valid authentication.
// This is used for fallback when the primary provider fails.
func (s *Service) GetLoggedInWebProviders() []WebProviderInfo {
	all := types.AllProviders()
	result := []WebProviderInfo{}
	for _, p := range all {
		// Check in-memory state first
		s.mu.Lock()
		isCurrentProvider := s.webID == p.ID && s.webAuth != nil && s.webAuth.IsAuthenticated()
		s.mu.Unlock()

		// Also check disk-persisted tokens
		auth := tokenstore.GetProviderToken(p.ID)
		hasDiskAuth := auth != nil && auth.IsAuthenticated()

		if isCurrentProvider || hasDiskAuth {
			result = append(result, WebProviderInfo{
				ID:          p.ID,
				DisplayName: p.DisplayName,
				LoginURL:    p.LoginURL,
			})
		}
	}
	return result
}

// ChatWeb sends a chat message to the active web provider.
func (s *Service) ChatWeb(message string) (string, error) {
	s.mu.Lock()
	auth := s.webAuth
	providerID := s.webID
	s.mu.Unlock()

	if auth == nil || !auth.IsAuthenticated() {
		return "", fmt.Errorf("Web 提供商未登录：请先登录并保存 Cookie")
	}

	ctx, cancel := context.WithTimeout(s.currentRequestContext(), 120*time.Second)
	defer cancel()

	req := types.ChatRequest{
		Message: message,
		Model:   s.model,
	}

	// Try direct HTTP chat first (deepseek, claude, doubao, kimi)
	result, err := providers.Chat(ctx, providerID, auth, req)
	if err == nil && result.Success {
		return result.Content, nil
	}
	if err == nil && result.Error != "" {
		// Provider requires browser-based chat
		if strings.Contains(result.Error, "requires browser") {
			return "", fmt.Errorf("Web 提供商 %s 需要浏览器交互模式，暂不支持直接对话。请使用 API 模式或本地模式。", providerID)
		}
		return "", fmt.Errorf("Web 对话失败 (%s): %s", providerID, result.Error)
	}
	if err != nil {
		return "", fmt.Errorf("Web 请求失败 (%s): %w", providerID, err)
	}

	return result.Content, nil
}

// AgentRunAdvanced executes the agent loop using the full agent subsystem
// (config, memory, compact, safety, roles, parallel, tools, mcp, ralph).
// It accepts a mode string ("local", "api", "web") to select the chat backend.
func (s *Service) AgentRunAdvanced(userMessage string, rootPath string, maxIterations int, mode string) agent.AgentRunResult {
	a := agent.NewAgent(rootPath)
	a.Registry().SetSandboxManager(s.sandboxMgr)
	a.Registry().SetRollbackManager(a.GetRollbackManager())
	userMessageWithMemory := s.withProjectMemory(rootPath, userMessage, userMessage)

	// Build chat function based on mode
	var chatFn agent.ChatFunc
	switch mode {
	case "local":
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)
			chatMsgs := agentMsgsToChatMsgs(systemPrompt, msgs)
			return s.Chat(chatMsgs)
		}
	case "api":
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)
			chatMsgs := agentMsgsToChatMsgs(systemPrompt, msgs)
			return s.ChatAPI(chatMsgs)
		}
	case "web":
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			// For web mode, concatenate all messages into a single prompt
			var sb strings.Builder
			sb.WriteString(s.withProjectMemory(rootPath, userMessage, systemPrompt))
			sb.WriteString("\n")
			for _, m := range msgs {
				sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
			}
			return s.ChatWeb(sb.String())
		}
	default:
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			systemPrompt = s.withProjectMemory(rootPath, userMessage, systemPrompt)
			chatMsgs := agentMsgsToChatMsgs(systemPrompt, msgs)
			return s.Chat(chatMsgs)
		}
	}

	// Wire agent loop progress to frontend via Wails events
	a.SetProgressCallback(func(iteration int, toolName string, toolArgs map[string]interface{}, output string, isDone bool, err string) {
		if s.hasEmitter() {
			s.emit("subagent:progress", map[string]interface{}{
				"agentId":   "main",
				"agentType": mode,
				"iteration": iteration,
				"toolName":  toolName,
				"toolArgs":  toolArgs,
				"output":    output,
				"isDone":    isDone,
				"error":     err,
			})
		}
	})

	return a.Run(context.Background(), userMessageWithMemory, maxIterations, chatFn)
}

// ConnectMCPServer connects to an MCP server via the agent subsystem.
func (s *Service) ConnectMCPServer(name string, serverType string, command string, args []string, env map[string]string) error {
	a := agent.NewAgent("") // rootPath not needed for MCP connection
	_, err := a.ConnectMCPServer(name, agent.MCPConfig{
		Type:    agent.MCPTransportType(serverType),
		Command: command,
		Args:    args,
		Env:     env,
	})
	return err
}

// GetAgentConfigFiles returns loaded TIANCAN.md config files for a project.
func (s *Service) GetAgentConfigFiles(rootPath string) []string {
	a := agent.NewAgent(rootPath)
	files := a.GetConfigFiles()
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

// agentMsgsToChatMsgs converts agent messages to ChatMessage format.
func agentMsgsToChatMsgs(systemPrompt string, msgs []agent.Message) []ChatMessage {
	var result []ChatMessage
	if systemPrompt != "" {
		result = append(result, ChatMessage{Role: "system", Content: systemPrompt})
	}
	for _, m := range msgs {
		result = append(result, ChatMessage{Role: string(m.Role), Content: m.Content})
	}
	return result
}

// chatWebConv sends a message to the web provider, supporting conversation_id for multi-turn.
// Returns (responseContent, newConversationID, error).
// chatWebConv sends a message to a specific web provider.
// If providerID is empty, uses the currently selected provider.
// This function tries the specified provider, and if it fails with a transient error,
// it attempts fallback to other available providers.
func (s *Service) chatWebConv(message string, convID string, providerID string) (string, string, error) {
	// Get all logged-in providers for fallback
	availableProviders := s.getLoggedInProviders()

	// If no specific provider requested, use current selection
	if providerID == "" {
		s.mu.Lock()
		providerID = s.webID
		s.mu.Unlock()
	}

	// Build ordered list: specified provider first, then others
	orderedProviders := []string{providerID}
	for _, p := range availableProviders {
		if p != providerID {
			orderedProviders = append(orderedProviders, p)
		}
	}

	var lastErr error
	for _, pid := range orderedProviders {
		resp, newConvID, err := s.chatWithProvider(message, convID, pid)
		if err == nil {
			thinking, answer := splitThinkingContent(resp)
			if thinking != "" && s.hasEmitter() {
				s.emit("ai:reasoning", map[string]string{
					"provider": "Web AI",
					"content":  truncateString(thinking, 4000),
				})
			}
			visible := answer
			if strings.TrimSpace(visible) == "" {
				visible = stripThinkingArtifacts(resp)
			}
			s.emitChunkedModelOutput("Web AI", visible)
			return resp, newConvID, nil
		}

		// Check if error is transient (network, rate limit, etc.) or permanent (auth)
		isTransient := s.isTransientError(err)
		log.Printf("chatWebConv: provider %s failed: %v (transient=%v)", pid, err, isTransient)

		if !isTransient {
			// Auth errors don't benefit from fallback - return immediately
			return "", "", fmt.Errorf("Web AI 请求失败 (%s): %v", pid, err)
		}
		lastErr = err

		// Clear convID for new provider (each provider has its own conversation)
		convID = ""
	}

	// All providers failed
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的 Web AI 提供商")
	}
	return "", "", fmt.Errorf("Web AI 请求失败: %v", lastErr)
}

// getLoggedInProviders returns provider IDs that have valid auth
func (s *Service) getLoggedInProviders() []string {
	var result []string
	all := types.AllProviders()

	for _, p := range all {
		// Check in-memory state
		s.mu.Lock()
		isCurrent := s.webID == p.ID && s.webAuth != nil && s.webAuth.IsAuthenticated()
		s.mu.Unlock()

		// Check disk
		auth := tokenstore.GetProviderToken(p.ID)
		hasAuth := auth != nil && auth.IsAuthenticated()

		if isCurrent || hasAuth {
			result = append(result, p.ID)
		}
	}
	return result
}

// isTransientError determines if an error is transient and warrants retry
func (s *Service) isTransientError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	// Non-transient errors (auth, config issues)
	nonTransient := []string{
		"未登录",
		"not logged in",
		"authentication",
		"unauthorized",
		"401",
		"403",
		"未授权",
		"invalid credentials",
		"cookie",
		"token",
		"需要浏览器",
		"requires browser",
		"浏览器交互",
		"not supported",
		"不支持",
	}
	for _, n := range nonTransient {
		if strings.Contains(errStr, strings.ToLower(n)) {
			return false
		}
	}

	// Transient errors that may succeed on retry
	transient := []string{
		"timeout",
		"超时",
		"connection",
		"连接",
		"network",
		"网络",
		"reset",
		"refused",
		"rate limit",
		"限流",
		"429",
		"500",
		"502",
		"503",
		"504",
		"empty response",
		"空响应",
		"context deadline",
		"deadline exceeded",
	}
	for _, t := range transient {
		if strings.Contains(errStr, strings.ToLower(t)) {
			return true
		}
	}

	// Default to transient for unknown errors (better to retry)
	return true
}

// chatWithProvider sends a message to a specific provider (no fallback)
func (s *Service) chatWithProvider(message string, convID string, providerID string) (string, string, error) {
	// Get auth for this specific provider
	s.mu.Lock()
	isCurrentProvider := s.webID == providerID
	currentAuth := s.webAuth
	s.mu.Unlock()

	// Try in-memory auth first (current provider)
	var auth *types.AuthResult
	if isCurrentProvider && currentAuth != nil && currentAuth.IsAuthenticated() {
		auth = currentAuth
	} else {
		// Try disk-persisted token
		auth = tokenstore.GetProviderToken(providerID)
	}

	if auth == nil || !auth.IsAuthenticated() {
		return "", "", fmt.Errorf("提供商 %s 未登录", providerID)
	}

	req := types.ChatRequest{
		Message:        message,
		Model:          s.model,
		ConversationID: convID,
	}

	cfg := types.GetProviderConfig(providerID)
	if cfg == nil {
		return "", "", fmt.Errorf("未知的 Web 提供商: %s", providerID)
	}

	if !cfg.UsesBrowserChat {
		ctx, cancel := context.WithTimeout(s.currentRequestContext(), 120*time.Second)
		defer cancel()
		result, err := providers.Chat(ctx, providerID, auth, req)
		if err != nil {
			return "", "", err
		}
		if !result.Success {
			return "", "", fmt.Errorf("%s", result.Error)
		}
		return result.Content, result.ConversationID, nil
	}

	result, err := browser.RunBrowserChat(providerID, auth, req)
	if err != nil {
		return "", "", err
	}
	if !result.Success {
		return "", "", fmt.Errorf("%s", result.Error)
	}
	return result.Content, result.ConversationID, nil
}

// buildWebToolPrompt builds a tool description block injected into the user message.
func (s *Service) buildWebToolPrompt(rootPath string) string {
	var b strings.Builder
	b.WriteString("You have access to the following tools. To call a tool, use <invoke> XML format:\n")
	b.WriteString("<invoke name=\"tool_name\">\n{\"arg1\": \"value1\"}\n</invoke>\n\n")
	b.WriteString("IMPORTANT RULES:\n")
	b.WriteString("- To DELETE files, use the bash tool: <invoke name=\"bash\">{\"command\": \"rm /path/to/file\"}</invoke>\n")
	b.WriteString("- To MOVE/RENAME files, use bash: <invoke name=\"bash\">{\"command\": \"mv /old /new\"}</invoke>\n")
	b.WriteString("- To COPY files, use bash: <invoke name=\"bash\">{\"command\": \"cp /src /dst\"}</invoke>\n")
	b.WriteString("- Do NOT just describe what you would do — actually call the tool to perform the action.\n")
	b.WriteString("- When the user asks you to delete/remove a file, call bash with rm immediately. Do not just list files.\n\n")
	b.WriteString("Available tools:\n\n")
	b.WriteString(s.toolDescriptions(rootPath))
	return s.withProjectMemory(rootPath, "", b.String())
}

// formatWebToolResults formats tool results for the next web prompt.
// Following Claude Code's context management principle: every message sent to the model
// must contain enough context for the model to understand what it's doing.
// Web AI platforms only accept a single message per turn (using convID for history),
// so we embed a context summary to prevent the model from losing track.
func formatWebToolResults(toolCalls []types.ToolCallBlock, results []string, originalQuestion string, assistantReasoning string) string {
	var b strings.Builder

	// Context summary: remind the model of the original question and its reasoning.
	// This is critical for Web AI providers where we can't send full message history.
	b.WriteString("<context>\n")
	b.WriteString(fmt.Sprintf("User's original request: %s\n", originalQuestion))
	if assistantReasoning != "" {
		// Truncate reasoning to prevent context bloat
		reasoning := assistantReasoning
		if len(reasoning) > 1500 {
			reasoning = reasoning[:1500] + "\n...[reasoning truncated]"
		}
		b.WriteString(fmt.Sprintf("Your previous reasoning: %s\n", reasoning))
	}
	// Remind the model what actions it already attempted (prevents intent drift)
	if len(toolCalls) > 0 {
		b.WriteString("Actions you already attempted:\n")
		for _, tc := range toolCalls {
			b.WriteString(fmt.Sprintf("- %s(%s)\n", tc.Name, string(tc.Arguments)))
		}
	}
	b.WriteString("</context>\n\n")

	b.WriteString("Tool execution results:\n\n")
	for i, tc := range toolCalls {
		res := results[i]
		if len(res) > 4000 {
			res = res[:4000] + "\n...[truncated]"
		}
		b.WriteString(fmt.Sprintf("<tool_result name=%q id=%q>\n%s\n</tool_result>\n\n", tc.Name, tc.ID, res))
	}
	b.WriteString("If you have enough information to answer the user, provide your final answer directly. If you still need more information, call another tool.")
	return b.String()
}

// buildActionsSummary extracts a concise summary of tool calls and results from message history.
// This is stored for follow-up context so the model remembers what it already did.
func buildActionsSummary(messages []ChatMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "tool_call":
			b.WriteString(fmt.Sprintf("- Called tool %s: %s\n", msg.Name, truncate(msg.Content, 200)))
		case "tool_result":
			b.WriteString(fmt.Sprintf("  Result: %s\n", truncate(msg.Content, 300)))
		}
	}
	summary := b.String()
	if len(summary) > 2000 {
		summary = summary[:2000] + "\n...[truncated]"
	}
	return summary
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// AgentRunWeb executes the agent loop using a web AI provider.
// Following Claude Code's context management principle: every message sent to the model
// must contain enough context for the model to understand what it's doing.
// Web AI platforms only accept a single message per turn (using convID for history),
// so we embed a context summary in tool result messages to prevent context loss.
func (s *Service) AgentRunWeb(userMessage string, rootPath string, maxIterations int) AgentRunResult {
	ctx, requestID := s.beginRequest()
	defer s.endRequest(requestID)
	if maxIterations <= 0 {
		maxIterations = 20
	}

	// Build tool prompt and inject into first message (web providers have no system prompt)
	toolPrompt := s.buildWebToolPrompt(rootPath)

	s.mu.Lock()
	hasExistingConv := s.webConvID != ""
	lastRequest := s.webLastRequest
	lastActions := s.webLastActions
	s.mu.Unlock()

	var firstMessage string
	if hasExistingConv {
		// Follow-up message: inject previous turn context so the model remembers
		// what was requested and what was already done.
		var contextBlock string
		projectMemory := s.projectMemoryContext(rootPath, userMessage)
		if lastRequest != "" || lastActions != "" || projectMemory != "" {
			contextBlock = "\n<previous_context>\n"
			if projectMemory != "" {
				contextBlock += projectMemory + "\n"
			}
			if lastRequest != "" {
				contextBlock += fmt.Sprintf("Previous user request: %s\n", lastRequest)
			}
			if lastActions != "" {
				contextBlock += fmt.Sprintf("Previous actions and results:\n%s\n", lastActions)
			}
			contextBlock += "</previous_context>\n"
		}
		firstMessage = fmt.Sprintf("[继续对话 — 你是天蚕 AI 编程助手，工作目录: %s。你可以继续使用工具。]%s\nUser: %s", rootPath, contextBlock, userMessage)
	} else {
		firstMessage = fmt.Sprintf("%s\n你是天蚕 AI 编程助手，工作目录: %s\n\n---\n\nUser: %s", toolPrompt, rootPath, userMessage)
	}

	messages := []ChatMessage{
		{Role: "user", Content: firstMessage},
	}
	result := AgentRunResult{Messages: messages}

	s.mu.Lock()
	convID := s.webConvID
	s.mu.Unlock()
	var sendMsg string

	// Track context across iterations (Claude Code sends full message history;
	// Web AI only sends one message per turn, so we embed context summary)
	assistantReasoning := ""

	for i := 0; i < maxIterations; i++ {
		if ctx.Err() != nil {
			result.Error = "已停止生成"
			result.Done = true
			return result
		}
		result.Iterations = i + 1

		// First iteration: send the full prompt with tool descriptions.
		// Subsequent iterations: send the tool results message with context.
		if i == 0 {
			sendMsg = firstMessage
		}

		resp, newConvID, err := s.chatWebConv(sendMsg, convID, "")
		if err != nil {
			result.Error = fmt.Sprintf("Web AI 请求失败: %v", err)
			result.Done = true
			result.Messages = messages
			s.emitProgress(i+1, "web", "", nil, "", true, result.Error)
			return result
		}
		if newConvID != "" {
			convID = newConvID
			s.mu.Lock()
			s.webConvID = newConvID
			s.mu.Unlock()
		}

		// Split thinking and answer from DeepSeek SSE output
		thinkPart, answerPart := splitThinkingContent(resp)

		// Parse tool calls (same strategies as Python ResponseParser)
		toolCalls := parser.ParseToolCalls(resp)
		log.Printf("AgentRunWeb iteration %d: %d tool call(s), resp_len=%d", i+1, len(toolCalls), len(resp))
		if len(toolCalls) == 0 {
			// No tool calls — final response: only show the answer part
			finalAnswer := stripThinkingArtifacts(answerPart)
			if finalAnswer == "" {
				// Fallback: if no answer part, use the whole response cleaned
				finalAnswer = stripThinkingArtifacts(resp)
			}
			messages = append(messages, ChatMessage{Role: "assistant", Content: finalAnswer})

			// Store context for follow-up: what was requested and what happened
			s.mu.Lock()
			s.webLastRequest = userMessage
			if len(messages) > 1 {
				s.webLastActions = buildActionsSummary(messages)
			}
			s.mu.Unlock()

			result.Done = true
			result.Messages = messages
			s.emitProgress(i+1, "web", "", nil, finalAnswer, true, "")
			return result
		}

		// Intermediate thinking: use thinkPart only (not the answer)
		thinking := resp
		for _, tc := range toolCalls {
			thinking = strings.Replace(thinking, tc.RawText, "", 1)
		}
		if thinkPart != "" {
			thinking = stripThinkingArtifacts(thinkPart)
		} else {
			thinking = stripThinkingArtifacts(thinking)
		}
		messages = append(messages, ChatMessage{Role: "assistant", Content: thinking})

		// Update assistant reasoning for context tracking
		assistantReasoning = thinking

		// Execute all tool calls (skip those with empty/invalid arguments)
		toolResults := make([]string, 0, len(toolCalls))
		for _, tc := range toolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal(tc.Arguments, &args); err != nil {
				args = map[string]interface{}{}
			}
			// Skip tool calls with no meaningful arguments (e.g. bash without command)
			if len(args) == 0 && tc.Name != "" {
				log.Printf("AgentRunWeb: skipping tool call %s with empty arguments", tc.Name)
				continue
			}
			// Add tool_call message so frontend can display it
			argsStr := string(tc.Arguments)
			messages = append(messages, ChatMessage{Role: "tool_call", Content: argsStr, Name: tc.Name})

			s.emitProgress(i+1, "web", tc.Name, args, "", false, "")
			res := s.ExecuteTool(tc.Name, args, rootPath)
			s.emitProgress(i+1, "web", tc.Name, args, res, false, "")
			if len(res) > 2000 {
				res = res[:2000]
			}
			// Add tool_result message so frontend can display it
			messages = append(messages, ChatMessage{Role: "tool_result", Content: res, Name: tc.Name})
			toolResults = append(toolResults, res)
			// Emit filetree refresh event for write operations so frontend updates directory tree
			if !isReadOnlyTool(tc.Name) && s.hasEmitter() {
				s.emit("filetree:refresh", rootPath)
			}
		}

		// Format tool results for next message, including context summary
		sendMsg = formatWebToolResults(toolCalls, toolResults, userMessage, assistantReasoning)
		messages = append(messages, ChatMessage{Role: "user", Content: sendMsg})
	}

	// Hit max iterations — still store context
	s.mu.Lock()
	s.webLastRequest = userMessage
	if len(messages) > 1 {
		s.webLastActions = buildActionsSummary(messages)
	}
	s.mu.Unlock()

	result.Done = false
	result.Messages = messages
	result.Error = fmt.Sprintf("达到最大迭代次数 %d", maxIterations)
	s.emitProgress(maxIterations, "web", "", nil, "", true, result.Error)
	return result
}
