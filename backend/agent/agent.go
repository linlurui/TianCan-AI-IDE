package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/compact"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/config"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/mcp"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/memory"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/parallel"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/ralph"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/roles"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/safety"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/tools"
	webparser "github.com/rocky233/tiancan-ai-ide/backend/webai/parser"
)

// AgentRunResult holds the result of an agent run.
type AgentRunResult struct {
	Done        bool             `json:"done"`
	Error       string           `json:"error,omitempty"`
	Messages    []Message        `json:"messages"`
	AgentType   string           `json:"agentType,omitempty"`
	TotalTokens int              `json:"totalTokens"`
	DurationMs  int64            `json:"durationMs"`
	RalphResult *RalphLoopResult `json:"ralphResult,omitempty"`
}

// StreamEvent represents a streaming event from the agent loop.
type StreamEvent struct {
	Type    string `json:"type"`           // "token", "tool_call", "tool_result", "thinking", "done", "error", "compact"
	Content string `json:"content"`        // content depends on type
	Name    string `json:"name,omitempty"` // tool name for tool_call/tool_result
	Success bool   `json:"success,omitempty"`
}

// StreamChatFunc is the streaming variant of ChatFunc.
// It calls onToken for each token and returns the full accumulated response.
type StreamChatFunc func(systemPrompt string, messages []Message, onToken func(token string)) (string, error)

// Agent is the main orchestrator that wires all subsystems together.
type Agent struct {
	rootPath     string
	homeDir      string
	registry     *tools.Registry
	safetySys    *safety.SafetySystem
	memorySys    *memory.MemorySystem
	compactSvc   *compact.CompactService
	mcpClient    *mcp.Client
	ralphLoop    *ralph.RalphLoop
	parallelExec *parallel.Executor

	mu          sync.Mutex
	messages    []Message
	configFiles []config.ConfigFile
}

// NewAgent creates a new agent with all subsystems initialized.
func NewAgent(rootPath string) *Agent {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = "/tmp"
	}

	registry := tools.NewRegistry(rootPath)
	safetySys := safety.NewSafetySystem()
	memorySys := memory.NewMemorySystem(homeDir)
	compactSvc := compact.NewCompactService()
	mcpClient := mcp.NewClient()

	a := &Agent{
		rootPath:     rootPath,
		homeDir:      homeDir,
		registry:     registry,
		safetySys:    safetySys,
		memorySys:    memorySys,
		compactSvc:   compactSvc,
		mcpClient:    mcpClient,
		parallelExec: parallel.NewExecutor(4),
	}

	// Load config files
	a.loadConfig()

	// Ensure memory directory exists
	memorySys.EnsureDirExists()

	return a
}

// loadConfig loads TIANCAN.md config files.
func (a *Agent) loadConfig() {
	configs, err := config.LoadAllConfigs(a.rootPath, a.homeDir)
	if err == nil {
		a.configFiles = configs
	}
}

// Run executes the main agent loop (Think-Act-Observe-Repeat).
// chatFn is the function that sends messages to the AI model.
func (a *Agent) Run(ctx context.Context, userMessage string, maxIterations int, chatFn ChatFunc) AgentRunResult {
	if maxIterations <= 0 {
		maxIterations = 20
	}
	start := time.Now()

	// Determine if task should be routed
	agentDef := roles.GeneralAgent
	if roles.ShouldRoute(userMessage) {
		agentDef = roles.RouterAgent
	}

	// Check if Ralph Loop should be used for non-trivial tasks
	useRalph := a.shouldUseRalphLoop(userMessage)
	if useRalph {
		return a.runWithRalph(ctx, userMessage, agentDef, chatFn)
	}

	// Build system prompt
	systemPrompt := a.buildSystemPrompt(agentDef)

	// Initialize messages
	a.mu.Lock()
	a.messages = append(a.messages, Message{
		Role:      RoleUser,
		Content:   userMessage,
		Timestamp: time.Now(),
	})
	messages := make([]Message, len(a.messages))
	copy(messages, a.messages)
	a.mu.Unlock()

	totalTokens := 0

	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return AgentRunResult{Done: true, Error: "cancelled", Messages: messages, DurationMs: time.Since(start).Milliseconds()}
		default:
		}

		// Check auto-compact
		if a.compactSvc.ShouldAutoCompact(messages) {
			result, err := a.compactSvc.CompactConversation(messages, func(prompt string) (string, error) {
				return chatFn(systemPrompt, []Message{{Role: RoleUser, Content: prompt}})
			})
			if err == nil && result != nil {
				messages = append(result.SummaryMessages, messages[len(messages)-4:]...)
			}
		}

		// Get AI response
		response, err := chatFn(systemPrompt, messages)
		if err != nil {
			return AgentRunResult{
				Done:       true,
				Error:      fmt.Sprintf("AI request failed: %v", err),
				Messages:   messages,
				DurationMs: time.Since(start).Milliseconds(),
			}
		}

		assistantMsg := Message{
			Role:      RoleAssistant,
			Content:   response,
			Timestamp: time.Now(),
		}
		messages = append(messages, assistantMsg)
		totalTokens += len(response) / 4

		// Parse tool calls
		toolCalls := parseToolCallFromResponse(response)
		if len(toolCalls) == 0 {
			// No more tool calls - done
			a.mu.Lock()
			a.messages = messages
			a.mu.Unlock()

			// Extract memories
			a.memorySys.ExtractMemories(messages)

			return AgentRunResult{
				Done:        true,
				Messages:    messages,
				TotalTokens: totalTokens,
				DurationMs:  time.Since(start).Milliseconds(),
			}
		}

		// Execute tool calls with safety checks
		for _, tc := range toolCalls {
			tool, found := a.registry.Get(tc.Name)
			if !found {
				// Check MCP tools
				mcpResult, mcpErr := a.mcpClient.CallTool(ctx, tc.Name, tc.Args)
				if mcpErr != nil {
					messages = append(messages, Message{
						Role:     RoleTool,
						Content:  fmt.Sprintf("Tool not found: %s", tc.Name),
						ToolName: tc.Name,
						Success:  false,
					})
				} else {
					messages = append(messages, Message{
						Role:     RoleTool,
						Content:  mcpResult.Content,
						ToolName: tc.Name,
						Success:  mcpResult.Success,
					})
				}
				continue
			}

			// Safety check
			permResult, level := a.safetySys.CheckPermission(tool, tc.Args, a.rootPath)
			switch permResult.Decision {
			case DecisionDeny:
				messages = append(messages, Message{
					Role:     RoleTool,
					Content:  fmt.Sprintf("Permission denied: %s", permResult.Reason),
					ToolName: tc.Name,
					Success:  false,
				})
			case DecisionAsk:
				// In non-interactive mode, deny
				messages = append(messages, Message{
					Role:     RoleTool,
					Content:  fmt.Sprintf("Tool %s requires user confirmation (safety level %d). Denied in non-interactive mode.", tc.Name, level),
					ToolName: tc.Name,
					Success:  false,
				})
			case DecisionAllow:
				result, err := tool.Execute(ctx, tc.Args)
				if err != nil {
					result = ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
				}
				messages = append(messages, Message{
					Role:     RoleTool,
					Content:  result.Content,
					ToolName: tc.Name,
					Success:  result.Success,
				})
				totalTokens += len(result.Content) / 4
			}
		}
	}

	a.mu.Lock()
	a.messages = messages
	a.mu.Unlock()

	return AgentRunResult{
		Done:        true,
		Messages:    messages,
		TotalTokens: totalTokens,
		DurationMs:  time.Since(start).Milliseconds(),
	}
}

// RunStream executes the agent loop with streaming events sent to the returned channel.
// The caller must drain the channel to avoid blocking. The channel is closed when done.
func (a *Agent) RunStream(ctx context.Context, userMessage string, maxIterations int, streamChatFn StreamChatFunc) (<-chan StreamEvent, error) {
	if maxIterations <= 0 {
		maxIterations = 20
	}
	ch := make(chan StreamEvent, 256)

	go func() {
		defer close(ch)
		start := time.Now()

		agentDef := roles.GeneralAgent
		if roles.ShouldRoute(userMessage) {
			agentDef = roles.RouterAgent
		}

		systemPrompt := a.buildSystemPrompt(agentDef)

		a.mu.Lock()
		a.messages = append(a.messages, Message{
			Role:      RoleUser,
			Content:   userMessage,
			Timestamp: time.Now(),
		})
		messages := make([]Message, len(a.messages))
		copy(messages, a.messages)
		a.mu.Unlock()

		totalTokens := 0

		for i := 0; i < maxIterations; i++ {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: "error", Content: "cancelled"}
				return
			default:
			}

			// Auto-compact check
			if a.compactSvc.ShouldAutoCompact(messages) {
				ch <- StreamEvent{Type: "compact", Content: "Compacting conversation..."}
				result, err := a.compactSvc.CompactConversation(messages, func(prompt string) (string, error) {
					return streamChatFn(systemPrompt, []Message{{Role: RoleUser, Content: prompt}}, nil)
				})
				if err == nil && result != nil {
					messages = append(result.SummaryMessages, messages[len(messages)-4:]...)
				}
			}

			// Streaming chat call
			var fullResp strings.Builder
			response, err := streamChatFn(systemPrompt, messages, func(token string) {
				fullResp.WriteString(token)
				ch <- StreamEvent{Type: "token", Content: token}
			})
			if err != nil {
				ch <- StreamEvent{Type: "error", Content: fmt.Sprintf("AI request failed: %v", err)}
				return
			}

			resp := fullResp.String()
			if resp == "" {
				resp = response
			}

			messages = append(messages, Message{
				Role:      RoleAssistant,
				Content:   resp,
				Timestamp: time.Now(),
			})
			totalTokens += len(resp) / 4

			// Parse tool calls
			toolCalls := parseToolCallFromResponse(resp)
			if len(toolCalls) == 0 {
				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				a.memorySys.ExtractMemories(messages)
				ch <- StreamEvent{Type: "done", Content: resp}
				_ = start
				return
			}

			// Execute tool calls with safety checks
			for _, tc := range toolCalls {
				argsJSON, _ := json.Marshal(tc.Args)
				ch <- StreamEvent{Type: "tool_call", Name: tc.Name, Content: string(argsJSON)}

				tool, found := a.registry.Get(tc.Name)
				if !found {
					ch <- StreamEvent{Type: "tool_result", Name: tc.Name, Content: fmt.Sprintf("Tool not found: %s", tc.Name), Success: false}
					messages = append(messages, Message{Role: RoleTool, Content: fmt.Sprintf("Tool not found: %s", tc.Name), ToolName: tc.Name, Success: false})
					continue
				}

				permResult, level := a.safetySys.CheckPermission(tool, tc.Args, a.rootPath)
				switch permResult.Decision {
				case DecisionDeny:
					content := fmt.Sprintf("Permission denied: %s", permResult.Reason)
					ch <- StreamEvent{Type: "tool_result", Name: tc.Name, Content: content, Success: false}
					messages = append(messages, Message{Role: RoleTool, Content: content, ToolName: tc.Name, Success: false})
				case DecisionAsk:
					content := fmt.Sprintf("Tool %s requires user confirmation (safety level %d). Denied in non-interactive mode.", tc.Name, level)
					ch <- StreamEvent{Type: "tool_result", Name: tc.Name, Content: content, Success: false}
					messages = append(messages, Message{Role: RoleTool, Content: content, ToolName: tc.Name, Success: false})
				case DecisionAllow:
					result, err := tool.Execute(ctx, tc.Args)
					if err != nil {
						result = ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
					}
					ch <- StreamEvent{Type: "tool_result", Name: tc.Name, Content: result.Content, Success: result.Success}
					messages = append(messages, Message{Role: RoleTool, Content: result.Content, ToolName: tc.Name, Success: result.Success})
					totalTokens += len(result.Content) / 4
				}
			}
		}

		ch <- StreamEvent{Type: "error", Content: fmt.Sprintf("达到最大迭代次数 %d", maxIterations)}
		a.mu.Lock()
		a.messages = messages
		a.mu.Unlock()
	}()

	return ch, nil
}

// shouldUseRalphLoop determines if a task should use the Ralph Loop.
func (a *Agent) shouldUseRalphLoop(task string) bool {
	if os.Getenv("TIANCAN_DISABLE_RALPH") == "1" {
		return false
	}
	// Use Ralph Loop for tasks that involve multiple file edits or API changes
	indicators := []string{
		"implement", "refactor", "migrate", "fix bug", "add feature",
		"create", "build", "port",
		"实现", "重构", "迁移", "修复", "添加", "创建", "构建", "移植",
	}
	lower := strings.ToLower(task)
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// runWithRalph runs the agent with the Ralph Loop for self-correction.
func (a *Agent) runWithRalph(ctx context.Context, task string, agentDef AgentDefinition, chatFn ChatFunc) AgentRunResult {
	start := time.Now()

	rl := ralph.NewRalphLoop(
		func(systemPrompt string, msgs []Message) (string, error) {
			return chatFn(systemPrompt, msgs)
		},
		func(ctx context.Context, toolName string, args map[string]interface{}) (ToolResult, error) {
			return a.registry.Execute(ctx, toolName, args)
		},
	)

	result, err := rl.Run(ctx, task, agentDef, a.rootPath)
	if err != nil {
		return AgentRunResult{
			Done:       true,
			Error:      fmt.Sprintf("Ralph Loop error: %v", err),
			Messages:   result.Messages,
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	return AgentRunResult{
		Done:        true,
		Messages:    result.Messages,
		AgentType:   string(agentDef.AgentType),
		TotalTokens: compact.EstimateTokens(result.Messages),
		DurationMs:  time.Since(start).Milliseconds(),
		RalphResult: result,
	}
}

// buildSystemPrompt assembles the full system prompt from all subsystems.
func (a *Agent) buildSystemPrompt(agentDef AgentDefinition) string {
	var sb strings.Builder

	// Base agent prompt
	sb.WriteString(agentDef.SystemPrompt)
	sb.WriteString("\n\n")

	// Config instructions (TIANCAN.md)
	if len(a.configFiles) > 0 {
		sb.WriteString(config.BuildSystemPrompt(a.configFiles))
		sb.WriteString("\n\n")
	}

	// Memory prompt
	if memPrompt := a.memorySys.LoadMemoryPrompt(); memPrompt != "" {
		sb.WriteString(memPrompt)
		sb.WriteString("\n\n")
	}

	// Available tools listing
	sb.WriteString("# Available Tools\n\n")
	for _, t := range a.registry.All() {
		ro := ""
		if t.IsReadOnly() {
			ro = " (read-only)"
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s%s\n", t.Name(), t.Description(), ro))
	}

	// MCP tools
	for _, t := range a.mcpClient.GetTools() {
		ro := ""
		if t.ReadOnly {
			ro = " (read-only)"
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s%s [MCP:%s]\n", t.Name, t.Description, ro, t.ServerName))
	}

	sb.WriteString("\n")

	// Safety mode indicator
	if a.safetySys.IsAutoMode() {
		sb.WriteString("Running in **auto mode** — tool calls are automatically classified for safety.\n\n")
	}

	// Project root
	sb.WriteString(fmt.Sprintf("Project root: %s\n", a.rootPath))

	return sb.String()
}

// ConnectMCPServer connects to an MCP server and registers its tools.
func (a *Agent) ConnectMCPServer(name string, cfg MCPConfig) (*MCPServerState, error) {
	state, err := a.mcpClient.ConnectToServer(name, cfg)
	if err != nil {
		return state, err
	}
	// Register MCP tools in the tool registry
	if state.Type == MCPConnConnected {
		for _, t := range state.Tools {
			a.registry.RegisterMCP(&mcpToolWrapper{info: t, client: a.mcpClient})
		}
	}
	return state, nil
}

// DisconnectMCPServer disconnects from an MCP server.
func (a *Agent) DisconnectMCPServer(name string) error {
	a.registry.UnregisterMCP(name)
	return a.mcpClient.DisconnectServer(name)
}

// GetMCPServers returns all MCP server states.
func (a *Agent) GetMCPServers() []MCPServerState {
	return a.mcpClient.GetServers()
}

// GetConfigFiles returns loaded config files.
func (a *Agent) GetConfigFiles() []config.ConfigFile {
	return a.configFiles
}

// GetTokenWarningState returns token usage warnings.
func (a *Agent) GetTokenWarningState() (int, bool, bool) {
	tokenCount := compact.EstimateTokens(a.messages)
	percentLeft, isAboveWarning, isAboveAutoCompact := a.compactSvc.CalculateTokenWarningState(tokenCount)
	return percentLeft, isAboveWarning, isAboveAutoCompact
}

// --- Tool call parsing ---

// parseToolCallFromResponse delegates to webai/parser which handles all formats
// (XML, JSON, emoji, Anthropic, DeepSeek, function-call, etc) without hardcoded tool lists.
func parseToolCallFromResponse(response string) []ToolCall {
	webCalls := webparser.ParseToolCalls(response)
	var calls []ToolCall
	for _, wc := range webCalls {
		args := make(map[string]interface{})
		if wc.Arguments != nil {
			json.Unmarshal(wc.Arguments, &args)
		}
		calls = append(calls, ToolCall{Name: wc.Name, Args: args})
	}
	return calls
}

// extractJSON tries to find a JSON array in the response.
func extractJSON(s string) string {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return "[]"
}

// allKnownTools is dynamically populated from the tool registry.
// No hardcoded tool names — tools are registered at runtime.
var allKnownTools = map[string]bool{}

// RegisterToolName adds a tool name to the known tools set.
func RegisterToolName(name string) {
	allKnownTools[name] = true
}

// IsKnownTool checks if a tool name is registered.
func IsKnownTool(name string) bool {
	return allKnownTools[name]
}

// parseXMLToolCalls and parseFunctionCallToolCalls are replaced by
// webai/parser.ParseToolCalls which handles all formats without hardcoded tool lists.
// The agent-level parseToolCallFromResponse delegates to it.

// --- MCP tool wrapper ---

// mcpToolWrapper wraps an MCP tool info as a Tool interface.
type mcpToolWrapper struct {
	info   MCPToolInfo
	client *mcp.Client
}

func (t *mcpToolWrapper) Name() string                        { return t.info.Name }
func (t *mcpToolWrapper) Description() string                 { return t.info.Description }
func (t *mcpToolWrapper) InputSchema() map[string]interface{} { return t.info.InputSchema }
func (t *mcpToolWrapper) IsReadOnly() bool                    { return t.info.ReadOnly }
func (t *mcpToolWrapper) IsConcurrencySafe() bool             { return t.info.ReadOnly }
func (t *mcpToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error) {
	return t.client.CallTool(ctx, t.info.Name, args)
}

// ResolvePath resolves a path relative to root, handling zero-dependency resolution.
func ResolvePath(path, rootPath string) string {
	if filepath.IsAbs(path) {
		return path
	}
	// Handle ~/ paths
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return filepath.Join(rootPath, path)
}
