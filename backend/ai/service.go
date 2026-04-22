package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"log"

	"github.com/rocky233/tiancan-ai-ide/backend/agent"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/safety"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/tools"
	agenttypes "github.com/rocky233/tiancan-ai-ide/backend/agent/types"
	"github.com/rocky233/tiancan-ai-ide/backend/skills"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/browser"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/parser"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/providers"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/tokenstore"
	"github.com/rocky233/tiancan-ai-ide/backend/webai/types"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// APIProviderConfig stores credentials for a remote API provider.
type APIProviderConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// Service manages LM Studio integration and AI chat/agent capabilities.
// TODO: Integrate with router for hardware-aware model selection on first launch
type Service struct {
	mu             sync.Mutex
	apiURL         string // e.g. http://127.0.0.1:1234/v1
	model          string
	running        bool
	provider       APIProviderConfig // active remote API provider
	webAuth        *types.AuthResult // active web provider auth
	webID          string            // active web provider ID
	webConvID      string            // persisted conversation_id for web providers
	webLastRequest string            // last user request for follow-up context
	webLastActions string            // last turn actions+results summary for follow-up context
	App            *application.App  // set by main.go; used for Events.Emit
}

// NewService creates a new AI service.
func NewService() *Service {
	return &Service{
		apiURL: "http://127.0.0.1:1234/v1",
		model:  "default",
	}
}

// ── LM Studio lifecycle ──────────────────────────────────────────

// managedLMStudioPath returns the path where we cache LM Studio binary.
func managedLMStudioPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ide", "bin", "lm-studio")
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
	ID     string `json:"id"`
	Object string `json:"object"`
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
	return result.Data, nil
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
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// Chat sends a non-streaming chat request and returns the full response.
func (s *Service) Chat(messages []ChatMessage) (string, error) {
	s.mu.Lock()
	apiURL := s.apiURL
	model := s.model
	s.mu.Unlock()

	reqBody := ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   4096,
		Stream:      false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	resp, err := http.Post(apiURL+"/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("LM Studio 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LM Studio 返回错误 %d: %s", resp.StatusCode, string(b))
	}

	var result ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LM Studio 返回空响应")
	}

	return result.Choices[0].Message.Content, nil
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
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API 请求失败 (%s): %w", p.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API 返回错误 %d (%s): %s", resp.StatusCode, p.Name, string(b))
	}

	var result ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("API 返回空响应 (%s)", p.Name)
	}

	return result.Choices[0].Message.Content, nil
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

	resp, err := http.Post(apiURL+"/chat/completions", "application/json", bytes.NewReader(body))
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
	Messages   []ChatMessage `json:"messages"`
	Iterations int           `json:"iterations"`
	Done       bool          `json:"done"`
	Error      string        `json:"error,omitempty"`
}

// registryCache caches tool registries per rootPath.
var (
	registryCache   = map[string]*tools.Registry{}
	registryCacheMu sync.Mutex
)

// getRegistry returns (or creates) a tools.Registry for the given rootPath.
func getRegistry(rootPath string) *tools.Registry {
	registryCacheMu.Lock()
	defer registryCacheMu.Unlock()
	if r, ok := registryCache[rootPath]; ok {
		return r
	}
	r := tools.NewRegistry(rootPath)
	registryCache[rootPath] = r
	return r
}

// builtInTools returns the tools available to the agent from the Registry.
func builtInTools(rootPath string) []AgentTool {
	r := getRegistry(rootPath)
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
func toolDescriptions(rootPath string) string {
	ts := builtInTools(rootPath)
	var b strings.Builder
	b.WriteString("你可以使用以下工具来完成任务。每个工具调用格式为：\n")
	b.WriteString("```tool\n{\"name\": \"工具名\", \"args\": {参数}}\n```\n\n")
	for _, t := range ts {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n  参数 Schema: %s\n\n", t.Name, t.Description, t.InputSchema))
	}

	// Add use_skill virtual tool
	b.WriteString("- **use_skill**: 调用预定义技能包，按步骤自动执行\n  参数 Schema: {\"skill\": \"技能名称\"}\n\n")

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

// executeTool runs a tool call via the tools.Registry and returns the result as a string.
func executeTool(name string, args map[string]interface{}, rootPath string) string {
	// Handle use_skill virtual tool
	if name == "use_skill" {
		return executeSkill(args, rootPath)
	}

	r := getRegistry(rootPath)
	result, err := r.Execute(context.Background(), name, args)
	if err != nil {
		// Tool not found in registry — try name aliases for backward compatibility
		aliases := map[string]string{
			"run_bash":   "bash",
			"read_file":  "read_file",
			"write_file": "file_write",
			"list_dir":   "list_directory",
		}
		if alias, ok := aliases[name]; ok && alias != name {
			result, err = r.Execute(context.Background(), alias, args)
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

// executeSkill runs a skill by name, executing each step sequentially.
func executeSkill(args map[string]interface{}, rootPath string) string {
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
		result := executeTool(step.Tool, step.Args, rootPath)
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

// rootPath returns the current project root path.
func (s *Service) rootPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiURL // reuse for now; TODO: add dedicated rootPath field
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

// AgentRun executes the TAOR (Think-Act-Observe-Repeat) loop.
// It sends the user's message, lets the model think and use tools,
// and iterates until the model produces a final text response or
// reaches maxIterations.
func (s *Service) AgentRun(userMessage string, rootPath string, maxIterations int) AgentRunResult {
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
- 完成任务后直接回复用户，不要再调用工具`, rootPath, toolDescriptions(rootPath))

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	result := AgentRunResult{Messages: messages}

	for i := 0; i < maxIterations; i++ {
		result.Iterations = i + 1

		// Call LM Studio
		resp, err := s.Chat(messages)
		if err != nil {
			result.Error = fmt.Sprintf("AI 请求失败: %v", err)
			result.Done = true
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
			return result
		}

		// Execute the tool
		toolResult := executeTool(toolName, toolArgs, rootPath)

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
	return result
}

// AgentRunAPI executes the TAOR loop using a remote API provider.
func (s *Service) AgentRunAPI(userMessage string, rootPath string, maxIterations int) AgentRunResult {
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
- 完成任务后直接回复用户，不要再调用工具`, rootPath, toolDescriptions(rootPath))

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	result := AgentRunResult{Messages: messages}

	for i := 0; i < maxIterations; i++ {
		result.Iterations = i + 1

		// Call remote API provider
		resp, err := s.ChatAPI(messages)
		if err != nil {
			result.Error = fmt.Sprintf("API 请求失败: %v", err)
			result.Done = true
			return result
		}

		messages = append(messages, ChatMessage{Role: "assistant", Content: resp})

		toolName, toolArgs, hasTool := parseToolCall(resp)
		if !hasTool {
			result.Done = true
			result.Messages = messages
			return result
		}

		toolResult := executeTool(toolName, toolArgs, rootPath)

		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult),
		})
	}

	result.Done = false
	result.Messages = messages
	result.Error = fmt.Sprintf("达到最大迭代次数 %d", maxIterations)
	return result
}

// ── Streaming Agent Run (for UI) ──────────────────────────────────

// AgentStreamEvent represents a streaming event from the agent loop.
type AgentStreamEvent struct {
	Type    string `json:"type"`             // "token", "tool_call", "tool_result", "permission_request", "done", "error"
	Content string `json:"content"`          // content depends on type
	Name    string `json:"name,omitempty"`   // tool name for tool_call/tool_result
	ID      string `json:"id,omitempty"`     // unique ID for permission_request correlation
	Reason  string `json:"reason,omitempty"` // reason for permission request
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
- 如果需求足够明确，直接执行，不要不必要地提问`, rootPath, toolDescriptions(rootPath))

		messages := []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		}

		for i := 0; i < maxIterations; i++ {
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
				ch <- AgentStreamEvent{Type: "done", Content: resp}
				return
			}

			// Notify UI about tool call
			argsJSON, _ := json.Marshal(toolArgs)
			ch <- AgentStreamEvent{Type: "tool_call", Name: toolName, Content: string(argsJSON)}

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

				// Block until user responds
				approved := <-respCh
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
			toolResult := executeTool(toolName, toolArgs, rootPath)
			ch <- AgentStreamEvent{Type: "tool_result", Name: toolName, Content: toolResult}

			// Add observation
			messages = append(messages, ChatMessage{
				Role:    "user",
				Content: fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult),
			})
		}

		ch <- AgentStreamEvent{Type: "error", Content: fmt.Sprintf("达到最大迭代次数 %d", maxIterations)}
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
			if s.App != nil {
				s.App.Event.Emit("agent:stream", evt)
			}
		}
		// Signal completion so the frontend knows the stream ended
		if s.App != nil {
			s.App.Event.Emit("agent:stream", AgentStreamEvent{Type: "stream_end"})
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

	// Check if Playwright is available
	if !browser.IsPlaywrightReady() {
		log.Printf("ai: Playwright not available, opening default browser as fallback")
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
		if s.App != nil {
			s.App.Event.Emit("webai:login-fallback", map[string]string{"provider": providerID})
		}
		return nil
	}

	// Run Playwright login in a goroutine (blocks up to 5 minutes)
	go func() {
		authResult, err := browser.RunLogin(providerID)
		if err != nil {
			log.Printf("ai: Playwright login failed for %s: %v", providerID, err)
			if s.App != nil {
				s.App.Event.Emit("webai:login-error", map[string]string{
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
		if s.App != nil {
			s.App.Event.Emit("webai:login-success", map[string]interface{}{
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

// ChatWeb sends a chat message to the active web provider.
func (s *Service) ChatWeb(message string) (string, error) {
	s.mu.Lock()
	auth := s.webAuth
	providerID := s.webID
	s.mu.Unlock()

	if auth == nil || !auth.IsAuthenticated() {
		return "", fmt.Errorf("Web 提供商未登录：请先登录并保存 Cookie")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	// Build chat function based on mode
	var chatFn agent.ChatFunc
	switch mode {
	case "local":
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			chatMsgs := agentMsgsToChatMsgs(systemPrompt, msgs)
			return s.Chat(chatMsgs)
		}
	case "api":
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			chatMsgs := agentMsgsToChatMsgs(systemPrompt, msgs)
			return s.ChatAPI(chatMsgs)
		}
	case "web":
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			// For web mode, concatenate all messages into a single prompt
			var sb strings.Builder
			for _, m := range msgs {
				sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
			}
			return s.ChatWeb(sb.String())
		}
	default:
		chatFn = func(systemPrompt string, msgs []agent.Message) (string, error) {
			chatMsgs := agentMsgsToChatMsgs(systemPrompt, msgs)
			return s.Chat(chatMsgs)
		}
	}

	return a.Run(context.Background(), userMessage, maxIterations, chatFn)
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
func (s *Service) chatWebConv(message string, convID string) (string, string, error) {
	s.mu.Lock()
	auth := s.webAuth
	providerID := s.webID
	s.mu.Unlock()

	if auth == nil || !auth.IsAuthenticated() {
		return "", "", fmt.Errorf("Web 提供商未登录")
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
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
func buildWebToolPrompt(rootPath string) string {
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
	b.WriteString(toolDescriptions(rootPath))
	return b.String()
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
	if maxIterations <= 0 {
		maxIterations = 20
	}

	// Build tool prompt and inject into first message (web providers have no system prompt)
	toolPrompt := buildWebToolPrompt(rootPath)

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
		if lastRequest != "" || lastActions != "" {
			contextBlock = "\n<previous_context>\n"
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
		result.Iterations = i + 1

		// First iteration: send the full prompt with tool descriptions.
		// Subsequent iterations: send the tool results message with context.
		if i == 0 {
			sendMsg = firstMessage
		}

		resp, newConvID, err := s.chatWebConv(sendMsg, convID)
		if err != nil {
			result.Error = fmt.Sprintf("Web AI 请求失败: %v", err)
			result.Done = true
			result.Messages = messages
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

		// Debug: log raw response and answer part for diagnosing parser failures
		log.Printf("AgentRunWeb DEBUG iteration %d: resp_len=%d answer_len=%d think_len=%d",
			i+1, len(resp), len(answerPart), len(thinkPart))
		if len(resp) > 200 {
			log.Printf("AgentRunWeb DEBUG resp_preview: %q", resp[:200])
		} else {
			log.Printf("AgentRunWeb DEBUG resp_full: %q", resp)
		}

		// Parse tool calls (same strategies as Python ResponseParser)
		toolCalls := parser.ParseToolCalls(resp)
		log.Printf("AgentRunWeb DEBUG: parser found %d tool calls", len(toolCalls))
		for ti, tc := range toolCalls {
			log.Printf("AgentRunWeb DEBUG tool[%d]: name=%s args=%s rawLen=%d", ti, tc.Name, string(tc.Arguments)[:min(200, len(string(tc.Arguments)))], len(tc.RawText))
		}
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
			return result
		}

		names := make([]string, 0, len(toolCalls))
		for _, tc := range toolCalls {
			names = append(names, tc.Name)
		}
		log.Printf("AgentRunWeb iteration %d: %d tool call(s): %s",
			i+1, len(toolCalls), strings.Join(names, ", "))

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

			res := executeTool(tc.Name, args, rootPath)
			if len(res) > 2000 {
				res = res[:2000]
			}
			// Add tool_result message so frontend can display it
			messages = append(messages, ChatMessage{Role: "tool_result", Content: res, Name: tc.Name})
			toolResults = append(toolResults, res)
			// Emit filetree refresh event for write operations so frontend updates directory tree
			if !isReadOnlyTool(tc.Name) && s.App != nil {
				s.App.Event.Emit("filetree:refresh", rootPath)
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
	return result
}
