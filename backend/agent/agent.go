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

	"github.com/linlurui/tiancan-ai-ide/backend/agent/compact"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/config"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/mcp"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/memory"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/parallel"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/ralph"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/roles"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/safety"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/tools"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/toolguard"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
	"github.com/linlurui/tiancan-ai-ide/backend/skills"
	webparser "github.com/linlurui/tiancan-ai-ide/backend/webai/parser"
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

// SkillMatcher is the minimal skill-subsystem surface the agent needs.
// The production implementation is *skills.Manager (in-memory registry loaded
// from the filesystem — the "FSProvider"). It is an interface so tests can
// inject a fake without touching disk (G5).
type SkillMatcher interface {
	MatchTrigger(input string) []*skills.Skill
}

// ProgressCallback is called by the agent loop to report iteration progress.
type ProgressCallback func(iteration int, toolName string, toolArgs map[string]interface{}, output string, isDone bool, err string)

// Agent is the main orchestrator that wires all subsystems together.
type Agent struct {
	rootPath      string
	homeDir       string
	registry      *tools.Registry
	safetySys     *safety.SafetySystem
	memorySys     *memory.MemorySystem
	compactSvc    *compact.CompactService
	mcpClient     *mcp.Client
	ralphLoop     *ralph.RalphLoop
	parallelExec  *parallel.Executor
	rollbackMgr   *ralph.RollbackManager
	skillMatcher  SkillMatcher
	toolGuard     *toolguard.Guard
	dupDetector   *toolguard.Detector

	mu          sync.Mutex
	messages    []Message
	configFiles []config.ConfigFile
	progressCB  ProgressCallback
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
	rollbackMgr := ralph.NewRollbackManager(rootPath)

	// G5: skill registry + FSProvider — load skills from disk so the main loop
	// can inject matched skill guidance as a pre-step user message.
	skillMgr := skills.NewManager(rootPath)
	_ = skillMgr.LoadAll()

	a := &Agent{
		rootPath:     rootPath,
		homeDir:      homeDir,
		registry:     registry,
		safetySys:    safetySys,
		memorySys:    memorySys,
		compactSvc:   compactSvc,
		mcpClient:    mcpClient,
		parallelExec: parallel.NewExecutor(4),
		rollbackMgr:  rollbackMgr,
		skillMatcher: skillMgr,
		toolGuard:    toolguard.New(toolguard.DefaultConfig()),
		dupDetector:  toolguard.NewDetector(0, 0),
	}

	// Load config files
	a.loadConfig()

	// Ensure memory directory exists
	memorySys.EnsureDirExists()

	return a
}

// SetProgressCallback sets the callback for agent loop progress reporting.
func (a *Agent) SetProgressCallback(cb ProgressCallback) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progressCB = cb
}

// GetRollbackManager returns the rollback manager for file versioning.
func (a *Agent) GetRollbackManager() *ralph.RollbackManager {
	return a.rollbackMgr
}

// Registry returns the tool registry so callers can inject shared dependencies.
func (a *Agent) Registry() *tools.Registry {
	return a.registry
}

// SetSkillMatcher overrides the skill matcher (registry + FSProvider).
// Primarily for tests; the production default is wired in NewAgent.
func (a *Agent) SetSkillMatcher(sm SkillMatcher) {
	a.skillMatcher = sm
}

// SetToolGuard overrides the per-tool execution guard (deadline + the
// parent-cancel vs own-deadline distinction). Primarily for tests.
func (a *Agent) SetToolGuard(g *toolguard.Guard) {
	a.toolGuard = g
}

// SetDupDetector overrides the duplicate/rejected-call detector. Primarily for
// tests — a low threshold makes the redirect observable within a few iterations.
func (a *Agent) SetDupDetector(d *toolguard.Detector) {
	a.dupDetector = d
}

// executeRegisteredTool runs the safety check + deadline-guarded execution for a
// registered tool, and tracks duplicate/rejected calls so a model hammering the
// same denied call is redirected with a reminder (G6).
//
// Returns the tool-result content, whether it succeeded, whether the tool
// actually executed (the allow path, so callers can emit progress callbacks),
// and the error string to surface on failure.
func (a *Agent) executeRegisteredTool(ctx context.Context, tool Tool, tc ToolCall) (content string, success bool, executed bool, errStr string) {
	// G6: duplicate / rejected-call reminder — short-circuit before re-running.
	if a.dupDetector != nil {
		if remind, reason := a.dupDetector.ShouldRemind(tc.Name, tc.Args); remind {
			a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeReminded)
			return reason, false, false, ""
		}
	}

	permResult, level := a.safetySys.CheckPermission(tool, tc.Args, a.rootPath)
	switch permResult.Decision {
	case DecisionDeny:
		if a.dupDetector != nil {
			a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeDenied)
		}
		return fmt.Sprintf("Permission denied: %s", permResult.Reason), false, false, ""
	case DecisionAsk:
		if a.dupDetector != nil {
			a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeDenied)
		}
		return fmt.Sprintf("Tool %s requires user confirmation (safety level %d). Denied in non-interactive mode.", tc.Name, level), false, false, ""
	case DecisionAllow:
		res := a.runToolGuarded(ctx, tool, tc)
		if a.dupDetector != nil {
			if res.Success {
				a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeOK)
			} else {
				a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeFailed)
			}
		}
		es := ""
		if !res.Success {
			es = res.Error
		}
		return res.Content, res.Success, true, es
	}
	// Unreachable: CheckPermission only returns Deny / Ask / Allow.
	return fmt.Sprintf("Tool %s requires permission", tc.Name), false, false, ""
}

// runToolGuarded runs the tool under a per-tool deadline. The guard distinguishes
// a parent-context cancellation from the tool's own deadline (G6).
func (a *Agent) runToolGuarded(ctx context.Context, tool Tool, tc ToolCall) ToolResult {
	if a.toolGuard == nil {
		res, err := tool.Execute(ctx, tc.Args)
		if err != nil {
			return ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}
		return res
	}
	res, err := a.toolGuard.Execute(ctx, func(c context.Context) (ToolResult, error) {
		return tool.Execute(c, tc.Args)
	})
	if err != nil {
		switch e := err.(type) {
		case *toolguard.ErrParentCancelled:
			return ToolResult{
				Content: fmt.Sprintf("Tool %s cancelled: parent context done (%v)", tc.Name, e.Err),
				Success: false, Error: err.Error(),
			}
		case *toolguard.ErrDeadline:
			return ToolResult{
				Content: fmt.Sprintf("Tool %s timed out after %s", tc.Name, e.Deadline),
				Success: false, Error: err.Error(),
			}
		default:
			return ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}
	}
	return res
}

// injectSkillPreSteps returns user-role messages for skills whose triggers match
// the incoming user request. These are prepended before the actual user message
// so the model sees the skill guidance as part of the first turn.
//
// KV-cache contract (G5): the guidance is rendered into user messages here and
// must NEVER enter buildSystemPrompt — keeping the system prompt static lets the
// provider reuse the cached prefix across skill matches.
func (a *Agent) injectSkillPreSteps(userMessage string) []Message {
	if a.skillMatcher == nil {
		return nil
	}
	matched := a.skillMatcher.MatchTrigger(userMessage)
	if len(matched) == 0 {
		return nil
	}
	pre := make([]Message, 0, len(matched))
	for _, s := range matched {
		pre = append(pre, Message{
			Role:      RoleUser,
			Content:   s.RenderPreStep(),
			Timestamp: time.Now(),
		})
	}
	return pre
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
	// G5: inject matched skill guidance as pre-step user messages, BEFORE the
	// real user request (keeps it out of the system prompt for KV-cache safety).
	for _, ps := range a.injectSkillPreSteps(userMessage) {
		a.messages = append(a.messages, ps)
	}
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
			cb := a.progressCB
			a.mu.Unlock()

			// Extract memories
			a.memorySys.ExtractMemories(messages)

			if cb != nil {
				cb(i+1, "", nil, response, true, "")
			}

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

			// G6: safety check + deadline-guarded execution + duplicate/denied
			// call redirect, all in one place shared with RunStream.
			content, success, executed, errStr := a.executeRegisteredTool(ctx, tool, tc)
			messages = append(messages, Message{
				Role:     RoleTool,
				Content:  content,
				ToolName: tc.Name,
				Success:  success,
			})
			if executed {
				totalTokens += len(content) / 4
				a.mu.Lock()
				cb := a.progressCB
				a.mu.Unlock()
				if cb != nil {
					cb(i+1, tc.Name, tc.Args, content, false, errStr)
				}
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
		// G5: inject matched skill guidance as pre-step user messages (see Run).
		for _, ps := range a.injectSkillPreSteps(userMessage) {
			a.messages = append(a.messages, ps)
		}
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

			// G6: safety check + deadline-guarded execution + duplicate/denied
			// call redirect — same shared path as Run.
			content, success, executed, _ := a.executeRegisteredTool(ctx, tool, tc)
			ch <- StreamEvent{Type: "tool_result", Name: tc.Name, Content: content, Success: success}
			messages = append(messages, Message{Role: RoleTool, Content: content, ToolName: tc.Name, Success: success})
			if executed {
				totalTokens += len(content) / 4
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
	if envconfig.EnvBool("TIANCAN_DISABLE_RALPH") {
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

	// Search guidance
	sb.WriteString("## Important: When to Search\n\n")
	sb.WriteString("Use **web_search** when you need:\n")
	sb.WriteString("- Current documentation or tutorials\n")
	sb.WriteString("- API usage examples from the web\n")
	sb.WriteString("- Solutions to unknown errors\n")
	sb.WriteString("- Latest version features or changelogs\n")
	sb.WriteString("- Technical blog posts or articles\n\n")

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
