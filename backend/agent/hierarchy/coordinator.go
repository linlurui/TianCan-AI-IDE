package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// AgentDefinition defines a sub-agent's configuration.
// Mirrors Claude Code's AgentDefinition from loadAgentsDir.ts.
type AgentDefinition struct {
	AgentType       string   `json:"agentType"`
	Description     string   `json:"description"`
	Tools           []string `json:"tools"` // tool names, ["*"] for all
	DisallowedTools []string `json:"disallowedTools,omitempty"`
	Prompt          string   `json:"prompt"`
	Model           string   `json:"model,omitempty"`          // "inherit" = parent's model
	PermissionMode  string   `json:"permissionMode,omitempty"` // "bubble", "acceptEdits", etc.
	MaxTurns        int      `json:"maxTurns,omitempty"`
	IsBackground    bool     `json:"background,omitempty"`
	Isolation       string   `json:"isolation,omitempty"` // "worktree", ""
	Memory          string   `json:"memory,omitempty"`    // "user", "project", "local"
	Color           string   `json:"color,omitempty"`
}

// AgentSpawnConfig holds parameters for spawning a sub-agent.
// Mirrors Claude Code's AgentTool input parameters.
type AgentSpawnConfig struct {
	SubagentType string          `json:"subagent_type,omitempty"`
	Prompt       string          `json:"prompt"`
	Description  string          `json:"description,omitempty"`
	Model        string          `json:"model,omitempty"`
	Cwd          string          `json:"cwd,omitempty"`
	Isolation    string          `json:"isolation,omitempty"`
	IsAsync      bool            `json:"run_in_background,omitempty"`
	TeamName     string          `json:"team_name,omitempty"` // Team collaboration: shared scratchpad, role assignment
	Name         string          `json:"name,omitempty"`
	ParentCtx    context.Context `json:"-"`
}

// AgentResult holds the result of a sub-agent execution.
type AgentResult struct {
	AgentID     string          `json:"agentId"`
	AgentType   string          `json:"agentType"`
	Output      string          `json:"output"`
	Messages    []types.Message `json:"messages"`
	Error       string          `json:"error,omitempty"`
	DurationMs  int64           `json:"durationMs"`
	TotalTokens int             `json:"totalTokens"`
	IsComplete  bool            `json:"isComplete"`
}

// ScratchpadCoordination manages shared state between coordinator and workers.
// Mirrors Claude Code's scratchpad directory for cross-worker knowledge.
type ScratchpadCoordination struct {
	dir     string
	mu      sync.RWMutex
	entries map[string]string
}

// NewScratchpadCoordination creates a new scratchpad coordinator.
func NewScratchpadCoordination(baseDir string) *ScratchpadCoordination {
	scratchDir := filepath.Join(baseDir, ".tiancan", "scratchpad")
	os.MkdirAll(scratchDir, 0755)
	return &ScratchpadCoordination{
		dir:     scratchDir,
		entries: make(map[string]string),
	}
}

// Write writes a key-value pair to the scratchpad.
func (s *ScratchpadCoordination) Write(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = value
	// Also persist to disk
	path := filepath.Join(s.dir, key+".md")
	return os.WriteFile(path, []byte(value), 0644)
}

// Read reads a value from the scratchpad.
func (s *ScratchpadCoordination) Read(key string) (string, error) {
	s.mu.RLock()
	if v, ok := s.entries[key]; ok {
		s.mu.RUnlock()
		return v, nil
	}
	s.mu.RUnlock()

	// Try disk
	path := filepath.Join(s.dir, key+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("scratchpad key %s not found", key)
	}
	s.mu.Lock()
	s.entries[key] = string(data)
	s.mu.Unlock()
	return string(data), nil
}

// List returns all keys in the scratchpad.
func (s *ScratchpadCoordination) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var keys []string
	for k := range s.entries {
		keys = append(keys, k)
	}

	// Also check disk
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			key := strings.TrimSuffix(e.Name(), ".md")
			found := false
			for _, k := range keys {
				if k == key {
					found = true
					break
				}
			}
			if !found {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// GetDir returns the scratchpad directory path.
func (s *ScratchpadCoordination) GetDir() string {
	return s.dir
}

// ToolExecutor executes a tool by name with args, returning the result string.
// Injected from ai.Service to allow sub-agents to use the same tool registry.
type ToolExecutor interface {
	ExecuteTool(name string, args map[string]interface{}, rootPath string) string
}

// ToolCallParser parses tool calls from LLM responses.
// Injected from the main agent's parser logic.
type ToolCallParser interface {
	ParseToolCalls(response string) (toolName string, toolArgs map[string]interface{}, hasTool bool)
}

// SubAgentProgress is emitted during sub-agent execution for streaming feedback.
type SubAgentProgress struct {
	AgentID   string `json:"agentId"`
	AgentType string `json:"agentType"`
	Iteration int    `json:"iteration"`
	ToolName  string `json:"toolName,omitempty"`
	ToolArgs  string `json:"toolArgs,omitempty"`
	Output    string `json:"output,omitempty"`
	IsDone    bool   `json:"isDone"`
	Error     string `json:"error,omitempty"`
}

// Coordinator manages the hierarchical agent system.
type Coordinator struct {
	agentDefs       map[string]*AgentDefinition
	scratchpad      *ScratchpadCoordination
	chatFn          types.ChatFunc
	toolExec        ToolExecutor       // injected from ai.Service
	toolParser      ToolCallParser     // injected for parsing tool calls from LLM responses
	activeWorkers   map[string]*AgentResult
	activeWorkersMu sync.Mutex
	isCoordinator   bool
	rootPath        string
	maxDepth        int                    // max sub-agent nesting depth
	progressCh      chan SubAgentProgress   // optional: streaming progress channel
	worktreeManager *WorktreeManager        // worktree 生命周期管理器
}

// NewCoordinator creates a new coordinator.
func NewCoordinator(rootPath string, chatFn types.ChatFunc) *Coordinator {
	c := &Coordinator{
		agentDefs:       make(map[string]*AgentDefinition),
		scratchpad:      NewScratchpadCoordination(rootPath),
		chatFn:          chatFn,
		activeWorkers:   make(map[string]*AgentResult),
		rootPath:        rootPath,
		isCoordinator:   os.Getenv("TIANCAN_COORDINATOR_MODE") == "1",
		maxDepth:        3, // default max nesting depth
		worktreeManager: NewWorktreeManager(rootPath),
	}

	// Load agent definitions: config-driven first, then built-in as fallback
	c.loadAgentDefsFromConfig()
	c.registerBuiltInAgents() // built-in only fills gaps not covered by config
	return c
}

// IsCoordinatorMode returns whether coordinator mode is active.
func (c *Coordinator) IsCoordinatorMode() bool {
	return c.isCoordinator
}

// SetToolExecutor injects the tool executor (from ai.Service).
func (c *Coordinator) SetToolExecutor(exec ToolExecutor) {
	c.toolExec = exec
}

// SetToolCallParser injects the tool call parser.
func (c *Coordinator) SetToolCallParser(parser ToolCallParser) {
	c.toolParser = parser
}

// SetProgressChannel sets the channel for streaming sub-agent progress.
func (c *Coordinator) SetProgressChannel(ch chan SubAgentProgress) {
	c.progressCh = ch
}

// GetWorktreeManager 返回 worktree 管理器实例
func (c *Coordinator) GetWorktreeManager() *WorktreeManager {
	return c.worktreeManager
}

// SetWorktreeManager 设置自定义的 worktree 管理器
func (c *Coordinator) SetWorktreeManager(wtm *WorktreeManager) {
	c.worktreeManager = wtm
}

// emitProgress sends a progress update if a channel is configured.
func (c *Coordinator) emitProgress(p SubAgentProgress) {
	if c.progressCh != nil {
		select {
		case c.progressCh <- p:
		default:
			// channel full, drop
		}
	}
}

// loadAgentDefsFromConfig loads agent definitions from TIANCAN_AGENT_DEFS_DIR env.
// Each file is a JSON agent definition matching the AgentDefinition struct.
func (c *Coordinator) loadAgentDefsFromConfig() {
	defsDir := os.Getenv("TIANCAN_AGENT_DEFS_DIR")
	if defsDir == "" {
		return // no config dir, use built-in only
	}

	entries, err := os.ReadDir(defsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fp := filepath.Join(defsDir, e.Name())
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		var def AgentDefinition
		if err := json.Unmarshal(data, &def); err != nil {
			continue
		}
		if def.AgentType != "" {
			c.agentDefs[def.AgentType] = &def
		}
	}
}

// registerBuiltInAgents registers the default agent types (only if not already defined by config).
// Mirrors Claude Code's builtInAgents.ts.
func (c *Coordinator) registerBuiltInAgents() {
	if _, exists := c.agentDefs["general-purpose"]; !exists {
		c.agentDefs["general-purpose"] = &AgentDefinition{
			AgentType:      "general-purpose",
			Description:    "General-purpose coding assistant",
			Tools:          []string{"*"},
			Prompt:         "You are a general-purpose coding assistant. Complete the task assigned to you.",
			PermissionMode: "acceptEdits",
			MaxTurns:       200,
		}
	}

	if _, exists := c.agentDefs["verify"]; !exists {
		c.agentDefs["verify"] = &AgentDefinition{
			AgentType:      "verify",
			Description:    "Verification agent that checks work for correctness",
			Tools:          []string{"read_file", "grep", "glob", "list_directory", "bash"},
			Prompt:         "You are a verification agent. Your job is to verify that the work done is correct and complete. Check for errors, missing steps, and inconsistencies. Report your findings clearly.",
			PermissionMode: "acceptEdits",
			MaxTurns:       50,
		}
	}

	if _, exists := c.agentDefs["research"]; !exists {
		c.agentDefs["research"] = &AgentDefinition{
			AgentType:      "research",
			Description:    "Research agent that explores codebases and gathers information",
			Tools:          []string{"read_file", "grep", "glob", "list_directory"},
			Prompt:         "You are a research agent. Your job is to explore the codebase, gather information, and report findings. Do not make any changes — only read and search.",
			PermissionMode: "acceptEdits",
			MaxTurns:       50,
		}
	}

	if _, exists := c.agentDefs["fix"]; !exists {
		c.agentDefs["fix"] = &AgentDefinition{
			AgentType:      "fix",
			Description:    "Bug-fixing agent that diagnoses and resolves issues",
			Tools:          []string{"*"},
			Prompt:         "You are a bug-fixing agent. Diagnose the issue described, identify the root cause, and implement a fix. Verify the fix works correctly.",
			PermissionMode: "acceptEdits",
			MaxTurns:       100,
		}
	}
}

// GetAgentDefinition returns the agent definition for the given type.
func (c *Coordinator) GetAgentDefinition(agentType string) *AgentDefinition {
	if def, ok := c.agentDefs[agentType]; ok {
		return def
	}
	return nil
}

// RegisterAgentDefinition registers a custom agent definition.
func (c *Coordinator) RegisterAgentDefinition(def *AgentDefinition) {
	c.agentDefs[def.AgentType] = def
}

// ListAgentDefinitions returns all registered agent definitions.
func (c *Coordinator) ListAgentDefinitions() []*AgentDefinition {
	var defs []*AgentDefinition
	for _, d := range c.agentDefs {
		defs = append(defs, d)
	}
	return defs
}

// SpawnAgent spawns a sub-agent to execute a task with an iterative tool loop.
// Mirrors Claude Code's AgentTool execution flow: the sub-agent calls the LLM,
// parses tool calls, executes them, and feeds results back until the task is done
// or max iterations are reached.
func (c *Coordinator) SpawnAgent(ctx context.Context, config AgentSpawnConfig) (*AgentResult, error) {
	agentType := config.SubagentType
	if agentType == "" {
		agentType = "general-purpose"
	}

	agentDef := c.GetAgentDefinition(agentType)
	if agentDef == nil {
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}

	agentID := fmt.Sprintf("agent-%d", time.Now().UnixNano())
	start := time.Now()

	// Worktree isolation: create worktree if requested
	worktreeDir := ""
	worktreeBranch := ""
	if config.Isolation == "worktree" && c.worktreeManager != nil {
		worktreeBranch = fmt.Sprintf("agent-%d", time.Now().UnixNano())
		state, err := c.worktreeManager.CreateWorktree(agentID, worktreeBranch)
		if err != nil {
			// Worktree creation failed — proceed without isolation
			worktreeBranch = ""
		} else {
			worktreeDir = state.Path
			defer func() {
				// Auto-cleanup worktree after agent finishes
				if worktreeDir != "" && c.worktreeManager != nil {
					c.worktreeManager.RemoveWorktree(worktreeBranch, true)
				}
			}()
		}
	}

	// Determine working directory for tool execution
	workingDir := c.rootPath
	if worktreeDir != "" {
		workingDir = worktreeDir
	} else if config.Cwd != "" {
		workingDir = config.Cwd
	}

	// Build the agent's prompt
	systemPrompt := c.buildAgentSystemPrompt(agentDef, config)
	maxTurns := agentDef.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50
	}

	// Iterative tool loop
	messages := []types.Message{
		{Role: types.RoleUser, Content: config.Prompt, Timestamp: time.Now()},
	}

	var allMessages []types.Message
	var finalOutput string

	for i := 0; i < maxTurns; i++ {
		// Call LLM
		response, err := c.chatFn(systemPrompt, messages)
		if err != nil {
			c.emitProgress(SubAgentProgress{
				AgentID: agentID, AgentType: agentType, Iteration: i + 1,
				IsDone: true, Error: err.Error(),
			})
			result := &AgentResult{
				AgentID: agentID, AgentType: agentType,
				DurationMs: time.Since(start).Milliseconds(),
				IsComplete: false, Error: err.Error(),
				Messages: allMessages,
			}
			c.activeWorkersMu.Lock()
			c.activeWorkers[agentID] = result
			c.activeWorkersMu.Unlock()
			return result, err
		}

		// Add assistant response to conversation
		messages = append(messages, types.Message{
			Role: types.RoleAssistant, Content: response, Timestamp: time.Now(),
		})
		allMessages = append(allMessages, messages[len(messages)-1])

		// Try to parse tool call from response
		toolName, toolArgs, hasTool := "", map[string]interface{}{}, false
		if c.toolParser != nil {
			toolName, toolArgs, hasTool = c.toolParser.ParseToolCalls(response)
		}

		if !hasTool {
			// No tool call — agent is done
			finalOutput = response
			c.emitProgress(SubAgentProgress{
				AgentID: agentID, AgentType: agentType, Iteration: i + 1,
				IsDone: true, Output: finalOutput,
			})
			break
		}

		// Execute the tool
		c.emitProgress(SubAgentProgress{
			AgentID: agentID, AgentType: agentType, Iteration: i + 1,
			ToolName: toolName, ToolArgs: fmt.Sprintf("%v", toolArgs),
		})

		var toolResult string
		if c.toolExec != nil {
			toolResult = c.toolExec.ExecuteTool(toolName, toolArgs, workingDir)
		} else {
			toolResult = fmt.Sprintf("tool %s not executed (no tool executor configured)", toolName)
		}

		// Feed tool result back to the agent
		obsMsg := fmt.Sprintf("工具 %s 执行结果:\n%s", toolName, toolResult)
		if len(toolResult) > 2000 {
			obsMsg = fmt.Sprintf("工具 %s 执行结果:\n%s... (截断)", toolName, toolResult[:2000])
		}
		messages = append(messages, types.Message{
			Role: types.RoleUser, Content: obsMsg, Timestamp: time.Now(),
		})
		allMessages = append(allMessages, messages[len(messages)-1])

		c.emitProgress(SubAgentProgress{
			AgentID: agentID, AgentType: agentType, Iteration: i + 1,
			ToolName: toolName, Output: toolResult[:min(len(toolResult), 500)],
		})
	}

	if finalOutput == "" && len(messages) > 0 {
		finalOutput = messages[len(messages)-1].Content
	}

	// If worktree was used, merge results back
	if worktreeBranch != "" && c.worktreeManager != nil {
		if err := c.worktreeManager.MergeWorktree(worktreeBranch); err != nil {
			// Check if it's a conflict error
			if conflictErr, ok := err.(*WorktreeConflictError); ok {
				// 自动解决简单冲突：使用 worktree 版本
				finalOutput += fmt.Sprintf("\n\n⚠ 检测到合并冲突: %v", conflictErr.Message)
				finalOutput += "\n尝试自动解决冲突（使用 worktree 版本）..."

				// 强制使用 worktree 版本
				cmd := exec.Command("git", "checkout", "--theirs", ".")
				cmd.Dir = c.rootPath
				cmd.Run()

				// 标记冲突文件为已解决
				cmd = exec.Command("git", "add", "-A")
				cmd.Dir = c.rootPath
				cmd.Run()

				finalOutput += "\n✓ 冲突已自动解决"
			} else {
				finalOutput += fmt.Sprintf("\n\n⚠ Worktree merge failed: %v", err)
			}
		}
	}

	durationMs := time.Since(start).Milliseconds()
	result := &AgentResult{
		AgentID:     agentID,
		AgentType:   agentType,
		Output:      finalOutput,
		Messages:    allMessages,
		DurationMs:  durationMs,
		TotalTokens: len(finalOutput) / 4,
		IsComplete:  true,
	}

	c.activeWorkersMu.Lock()
	c.activeWorkers[agentID] = result
	c.activeWorkersMu.Unlock()

	return result, nil
}

// SpawnAgentAsync spawns a sub-agent to run in the background.
// Mirrors Claude Code's async agent execution.
func (c *Coordinator) SpawnAgentAsync(ctx context.Context, config AgentSpawnConfig) string {
	agentID := fmt.Sprintf("agent-%d", time.Now().UnixNano())

	// Register as pending
	c.activeWorkersMu.Lock()
	c.activeWorkers[agentID] = &AgentResult{
		AgentID:    agentID,
		AgentType:  config.SubagentType,
		IsComplete: false,
	}
	c.activeWorkersMu.Unlock()

	// Run in goroutine
	go func() {
		result, err := c.SpawnAgent(ctx, config)
		if err != nil && result == nil {
			result = &AgentResult{
				AgentID:    agentID,
				AgentType:  config.SubagentType,
				Error:      err.Error(),
				IsComplete: true,
			}
		}
		result.AgentID = agentID
		c.activeWorkersMu.Lock()
		c.activeWorkers[agentID] = result
		c.activeWorkersMu.Unlock()
	}()

	return agentID
}

// GetAgentResult returns the result of a completed agent.
func (c *Coordinator) GetAgentResult(agentID string) (*AgentResult, bool) {
	c.activeWorkersMu.Lock()
	defer c.activeWorkersMu.Unlock()
	result, ok := c.activeWorkers[agentID]
	if !ok {
		return nil, false
	}
	return result, result.IsComplete
}

// GetActiveWorkers returns all active worker IDs.
func (c *Coordinator) GetActiveWorkers() []string {
	c.activeWorkersMu.Lock()
	defer c.activeWorkersMu.Unlock()
	var ids []string
	for id, r := range c.activeWorkers {
		if !r.IsComplete {
			ids = append(ids, id)
		}
	}
	return ids
}

// GetWorkerContext builds the user context for coordinator mode.
// Mirrors Claude Code's getCoordinatorUserContext().
func (c *Coordinator) GetWorkerContext(mcpServerNames []string) map[string]string {
	if !c.isCoordinator {
		return nil
	}

	workerTools := []string{"read_file", "write_file", "edit_file", "bash", "grep", "glob", "list_directory"}
	content := fmt.Sprintf("Workers spawned via the Agent tool have access to these tools: %s",
		strings.Join(workerTools, ", "))

	if len(mcpServerNames) > 0 {
		content += fmt.Sprintf("\n\nWorkers also have access to MCP tools from connected MCP servers: %s",
			strings.Join(mcpServerNames, ", "))
	}

	scratchDir := c.scratchpad.GetDir()
	if scratchDir != "" {
		content += fmt.Sprintf("\n\nScratchpad directory: %s\nWorkers can read and write here without permission prompts. Use this for durable cross-worker knowledge — structure files however fits the work.", scratchDir)
	}

	return map[string]string{"workerToolsContext": content}
}

// buildAgentSystemPrompt builds the system prompt for a sub-agent.
// Mirrors Claude Code's buildAgentSystemPrompt().
func (c *Coordinator) buildAgentSystemPrompt(def *AgentDefinition, config AgentSpawnConfig) string {
	var sb strings.Builder

	sb.WriteString(def.Prompt)
	sb.WriteString("\n\n")

	// Add tool context
	if len(def.Tools) > 0 && def.Tools[0] != "*" {
		sb.WriteString(fmt.Sprintf("You have access to these tools: %s\n", strings.Join(def.Tools, ", ")))
	} else {
		sb.WriteString("You have access to all available tools.\n")
	}

	// Add scratchpad context if coordinator mode
	if c.isCoordinator {
		sb.WriteString(fmt.Sprintf("\nScratchpad directory: %s\nYou can read and write files here to share information with other workers.\n", c.scratchpad.GetDir()))
	}

	// Add task description
	if config.Description != "" {
		sb.WriteString(fmt.Sprintf("\nTask description: %s\n", config.Description))
	}

	// Add worktree isolation notice
	if config.Isolation == "worktree" {
		sb.WriteString("\nYou are running in an isolated worktree. All file operations should be relative to your working directory.\n")
	}

	return sb.String()
}

// ForkSubagent creates a forked sub-agent that inherits parent context.
// Mirrors Claude Code's forkSubagent.ts.
func (c *Coordinator) ForkSubagent(ctx context.Context, directive string, parentMessages []types.Message) (*AgentResult, error) {
	// Build forked messages: parent context + directive
	forkMessages := make([]types.Message, len(parentMessages))
	copy(forkMessages, parentMessages)

	// Append the directive as a new user message
	forkMessages = append(forkMessages, types.Message{
		Role:      types.RoleUser,
		Content:   fmt.Sprintf("<fork_directive>\n%s\n</fork_directive>", directive),
		Timestamp: time.Now(),
	})

	config := AgentSpawnConfig{
		Prompt:    directive,
		IsAsync:   false,
		ParentCtx: ctx,
	}

	return c.SpawnAgent(ctx, config)
}

// ─── Worktree Isolation ──────────────────────────────────────────────

// WorktreeState 描述一个 worktree 的状态
type WorktreeState struct {
	ID        string    `json:"id"`
	Branch    string    `json:"branch"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
	Status    string    `json:"status"` // "active", "merging", "abandoned"
	ParentID  string    `json:"parentAgentId,omitempty"`
}

// WorktreeManager 管理所有 worktree 的生命周期
type WorktreeManager struct {
	mu               sync.RWMutex
	worktrees        map[string]*WorktreeState
	gitRoot          string              // Git 仓库根目录
	worktreeRoot     string              // Worktree 存储目录
	maxParallel      int                 // 最大并行 worktree 数量
	activeCount      int                 // 当前活跃的 worktree 数量
	conflictResolver ConflictResolver    // 冲突解决策略
}

// ConflictResolver 定义冲突解决策略接口
type ConflictResolver interface {
	ResolveConflict(localContent, remoteContent, baseContent string) (string, error)
}

// DefaultConflictResolver 默认的冲突解决策略
type DefaultConflictResolver struct{}

// ResolveConflict 简单的三路合并
func (d *DefaultConflictResolver) ResolveConflict(local, remote, base string) (string, error) {
	// 如果 local 或 remote 与 base 相同，选择变化的那个
	if local == base {
		return remote, nil
	}
	if remote == base {
		return local, nil
	}
	// 两者都有变化，返回 local（优先子 agent 的修改）
	return local, nil
}

// AutoMergeConflictResolver 自动合并冲突解决策略
type AutoMergeConflictResolver struct {
	preference string // "local" 或 "remote"
}

// ResolveConflict 根据偏好解决冲突
func (a *AutoMergeConflictResolver) ResolveConflict(local, remote, base string) (string, error) {
	if a.preference == "remote" {
		if remote != base {
			return remote, nil
		}
		return local, nil
	}
	// 默认 local
	if local != base {
		return local, nil
	}
	return remote, nil
}

// SetConflictResolver 设置冲突解决策略
func (wtm *WorktreeManager) SetConflictResolver(resolver ConflictResolver) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()
	wtm.conflictResolver = resolver
}

// NewWorktreeManager 创建 worktree 管理器
func NewWorktreeManager(basePath string) *WorktreeManager {
	gitRoot := basePath // 假设 basePath 就是 git 根目录
	worktreeRoot := filepath.Join(basePath, ".tiancan", "worktrees")
	os.MkdirAll(worktreeRoot, 0755)
	return &WorktreeManager{
		worktrees:    make(map[string]*WorktreeState),
		gitRoot:      gitRoot,
		worktreeRoot: worktreeRoot,
		maxParallel: 4, // 默认最多 4 个并行 worktree
	}
}

// SetMaxParallel 设置最大并行 worktree 数量
func (wtm *WorktreeManager) SetMaxParallel(max int) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()
	if max > 0 && max <= 16 {
		wtm.maxParallel = max
	}
}

// CanCreateWorktree 检查是否可以创建新的 worktree
func (wtm *WorktreeManager) CanCreateWorktree() bool {
	wtm.mu.RLock()
	defer wtm.mu.RUnlock()
	return wtm.activeCount < wtm.maxParallel
}

// GetActiveCount 获取当前活跃的 worktree 数量
func (wtm *WorktreeManager) GetActiveCount() int {
	wtm.mu.RLock()
	defer wtm.mu.RUnlock()
	return wtm.activeCount
}

// SetGitRoot 设置 git 仓库根目录
func (wtm *WorktreeManager) SetGitRoot(path string) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()
	wtm.gitRoot = path
	// 更新 worktree 根目录
	wtm.worktreeRoot = filepath.Join(path, ".tiancan", "worktrees")
	os.MkdirAll(wtm.worktreeRoot, 0755)
}

// WorktreeLifecycleManager 管理 worktree 生命周期
type WorktreeLifecycleManager struct {
	manager          *WorktreeManager
	mu               sync.Mutex
	lifecycles       map[string]*WorktreeLifecycle
	cleanupInterval  time.Duration
	maxAge           time.Duration
	stopCh           chan struct{}
	emulatorRunning  bool
}

// WorktreeLifecycle 跟踪单个 worktree 的生命周期
type WorktreeLifecycle struct {
	WorktreeID     string
	AgentID        string
	CreatedAt      time.Time
	LastActivityAt time.Time
	Status         string // "active", "idle", "cleanup_pending", "cleaned"
	ResourceUsage  ResourceUsage
}

// ResourceUsage 资源使用情况
type ResourceUsage struct {
	FilesChanged int
	LinesAdded   int
	LinesRemoved int
}

// NewWorktreeLifecycleManager 创建生命周期管理器
func NewWorktreeLifecycleManager(mgr *WorktreeManager) *WorktreeLifecycleManager {
	return &WorktreeLifecycleManager{
		manager:         mgr,
		lifecycles:      make(map[string]*WorktreeLifecycle),
		cleanupInterval: 30 * time.Second,
		maxAge:          10 * time.Minute, // 10分钟无活动则清理
		stopCh:          make(chan struct{}),
	}
}

// Start 启动生命周期管理
func (lcm *WorktreeLifecycleManager) Start() {
	lcm.mu.Lock()
	if lcm.emulatorRunning {
		lcm.mu.Unlock()
		return
	}
	lcm.emulatorRunning = true
	lcm.mu.Unlock()

	go func() {
		ticker := time.NewTicker(lcm.cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				lcm.cleanupIdleWorktrees()
			case <-lcm.stopCh:
				return
			}
		}
	}()
}

// Stop 停止生命周期管理
func (lcm *WorktreeLifecycleManager) Stop() {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()
	if lcm.emulatorRunning {
		close(lcm.stopCh)
		lcm.emulatorRunning = false
		lcm.stopCh = make(chan struct{})
	}
}

// Register 注册一个新的 worktree 生命周期
func (lcm *WorktreeLifecycleManager) Register(worktreeID, agentID string) {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()
	lcm.lifecycles[worktreeID] = &WorktreeLifecycle{
		WorktreeID:     worktreeID,
		AgentID:        agentID,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
		Status:         "active",
	}
}

// UpdateActivity 更新 worktree 活动状态
func (lcm *WorktreeLifecycleManager) UpdateActivity(worktreeID string) {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()
	if lc, ok := lcm.lifecycles[worktreeID]; ok {
		lc.LastActivityAt = time.Now()
		if lc.Status == "idle" {
			lc.Status = "active"
		}
	}
}

// MarkIdle 标记 worktree 为空闲状态
func (lcm *WorktreeLifecycleManager) MarkIdle(worktreeID string) {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()
	if lc, ok := lcm.lifecycles[worktreeID]; ok {
		lc.Status = "idle"
	}
}

// cleanupIdleWorktrees 清理空闲的 worktrees
func (lcm *WorktreeLifecycleManager) cleanupIdleWorktrees() {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()

	now := time.Now()
	var toClean []string

	for id, lc := range lcm.lifecycles {
		if lc.Status == "cleanup_pending" || (lc.Status == "idle" && now.Sub(lc.LastActivityAt) > lcm.maxAge) {
			toClean = append(toClean, id)
		}
	}

	for _, id := range toClean {
		if err := lcm.manager.RemoveWorktree(id, true); err == nil {
			delete(lcm.lifecycles, id)
		}
	}
}

// GetStats 返回生命周期统计信息
func (lcm *WorktreeLifecycleManager) GetStats() map[string]interface{} {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()

	stats := map[string]interface{}{
		"total":       len(lcm.lifecycles),
		"active":      0,
		"idle":        0,
		"cleanup":     0,
		"maxAge":      lcm.maxAge.String(),
		"activeCount": lcm.manager.GetActiveCount(),
	}

	for _, lc := range lcm.lifecycles {
		switch lc.Status {
		case "active":
			stats["active"] = stats["active"].(int) + 1
		case "idle":
			stats["idle"] = stats["idle"].(int) + 1
		case "cleanup_pending":
			stats["cleanup"] = stats["cleanup"].(int) + 1
		}
	}
	return stats
}

// CreateWorktree 创建一个新的隔离 worktree
func (wtm *WorktreeManager) CreateWorktree(agentID, branchName string) (*WorktreeState, error) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	// 检查并行限制
	if wtm.activeCount >= wtm.maxParallel {
		return nil, fmt.Errorf("maximum parallel worktrees (%d) reached", wtm.maxParallel)
	}

	worktreeDir := filepath.Join(wtm.worktreeRoot, branchName)
	state := &WorktreeState{
		ID:        branchName,
		Branch:    branchName,
		Path:      worktreeDir,
		CreatedAt: time.Now(),
		Status:    "active",
		ParentID:  agentID,
	}

	// Try git worktree add - 从 git 根目录执行
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", branchName)
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If branch already exists, try without -b
		cmd = exec.Command("git", "worktree", "add", worktreeDir, branchName)
		cmd.Dir = wtm.gitRoot
		output, err = cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git worktree add failed: %s", string(output[:min(len(output), 300)]))
		}
	}
	wtm.worktrees[branchName] = state
	wtm.activeCount++
	return state, nil
}

// RemoveWorktree 移除一个 worktree
func (wtm *WorktreeManager) RemoveWorktree(branchName string, force bool) error {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	state, ok := wtm.worktrees[branchName]
	if !ok {
		// Worktree not managed, try direct removal
		worktreeDir := filepath.Join(wtm.worktreeRoot, branchName)
		return wtm.removeWorktreeDir(worktreeDir, force)
	}

	if state.Status == "merging" {
		return fmt.Errorf("worktree %s is in merging state, cannot remove", branchName)
	}

	if err := wtm.removeWorktreeDir(state.Path, force); err != nil {
		return err
	}
	delete(wtm.worktrees, branchName)
	if wtm.activeCount > 0 {
		wtm.activeCount--
	}
	return nil
}

// PreMergeCheck 在合并前检查是否会产生冲突
func (wtm *WorktreeManager) PreMergeCheck(branchName string) (bool, []string, error) {
	wtm.mu.RLock()
	_, ok := wtm.worktrees[branchName]
	wtm.mu.RUnlock()

	if !ok {
		return false, nil, fmt.Errorf("worktree %s not found", branchName)
	}

	// 获取分支与 HEAD 的差异文件列表
	cmd := exec.Command("git", "diff", "--name-only", "HEAD", branchName)
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil, fmt.Errorf("failed to get diff: %s", string(output))
	}

	changedFiles := strings.Split(strings.TrimSpace(string(output)), "\n")

	// 检查这些文件在当前 HEAD 中是否有未提交的更改
	cmd = exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = wtm.gitRoot
	localOutput, err := cmd.CombinedOutput()
	if err == nil {
		localChanges := strings.Split(strings.TrimSpace(string(localOutput)), "\n")
		localChangeSet := make(map[string]bool)
		for _, f := range localChanges {
			localChangeSet[f] = true
		}

		// 找出可能冲突的文件
		var potentialConflicts []string
		for _, f := range changedFiles {
			if f != "" && localChangeSet[f] {
				potentialConflicts = append(potentialConflicts, f)
			}
		}

		if len(potentialConflicts) > 0 {
			return true, potentialConflicts, nil
		}
	}

	return false, nil, nil
}

// removeWorktreeDir 执行实际的 worktree 移除
func (wtm *WorktreeManager) removeWorktreeDir(worktreeDir string, force bool) error {
	args := []string{"worktree", "remove", worktreeDir}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove failed: %s", string(output[:min(len(output), 300)]))
	}
	return nil
}

// MergeWorktree 将 worktree 的更改合并回主分支
func (wtm *WorktreeManager) MergeWorktree(branchName string) error {
	wtm.mu.Lock()
	state, ok := wtm.worktrees[branchName]
	if ok {
		state.Status = "merging"
	}
	wtm.mu.Unlock()

	// 检查是否有冲突
	cmd := exec.Command("git", "merge", "--no-commit", "--no-ff", branchName)
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()

	if err != nil {
		// 有冲突，尝试分析
		if strings.Contains(string(output), "conflict") || strings.Contains(string(output), "CONFLICT") {
			wtm.mu.Lock()
			if state != nil {
				state.Status = "conflicted"
			}
			wtm.mu.Unlock()
			return &WorktreeConflictError{
				BranchName: branchName,
				Output:     string(output),
				Message:    "Merge conflict detected, manual resolution required",
			}
		}
		return fmt.Errorf("git merge failed: %s", string(output[:min(len(output), 300)]))
	}

	// 完成合并
	cmd = exec.Command("git", "commit", "-m", fmt.Sprintf("Merge worktree %s", branchName))
	cmd.Dir = wtm.gitRoot
	if _, err := cmd.CombinedOutput(); err != nil {
		// 如果没有实际更改，abort 合并
		exec.Command("git", "merge", "--abort").Run()
		return fmt.Errorf("git commit failed (no changes to merge): %s", branchName)
	}

	// 更新状态并清理
	wtm.mu.Lock()
	if state != nil {
		state.Status = "merged"
	}
	wtm.mu.Unlock()

	// 移除 worktree
	wtm.RemoveWorktree(branchName, false)
	return nil
}

// AutoMergeWithConflictResolution 尝试自动合并并解决冲突
func (wtm *WorktreeManager) AutoMergeWithConflictResolution(branchName string) (bool, []string, error) {
	wtm.mu.Lock()
	state, ok := wtm.worktrees[branchName]
	if ok {
		state.Status = "merging"
	}
	wtm.mu.Unlock()

	// 尝试合并
	cmd := exec.Command("git", "merge", "--no-commit", "--no-ff", branchName)
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()

	if err != nil {
		// 检查是否是冲突
		if strings.Contains(string(output), "conflict") || strings.Contains(string(output), "CONFLICT") {
			// 获取冲突文件列表
			conflictFiles, _ := wtm.GetCurrentConflictedFiles()

			// 尝试自动解决冲突
			resolvedFiles, resolveErr := wtm.AutoResolveConflicts(conflictFiles)
			if resolveErr != nil {
				// 自动解决失败，abort
				exec.Command("git", "merge", "--abort").Run()
				return false, conflictFiles, resolveErr
			}

			// 尝试完成合并
			cmd = exec.Command("git", "commit", "-m", fmt.Sprintf("Merge worktree %s (auto-resolved conflicts)", branchName))
			cmd.Dir = wtm.gitRoot
			if _, commitErr := cmd.CombinedOutput(); commitErr != nil {
				exec.Command("git", "merge", "--abort").Run()
				return false, conflictFiles, fmt.Errorf("auto-resolve failed: %s", commitErr)
			}

			return true, resolvedFiles, nil
		}
		return false, nil, fmt.Errorf("merge failed: %s", string(output[:min(len(output), 300)]))
	}

	// 无冲突完成合并
	cmd = exec.Command("git", "commit", "-m", fmt.Sprintf("Merge worktree %s", branchName))
	cmd.Dir = wtm.gitRoot
	if _, err := cmd.CombinedOutput(); err != nil {
		exec.Command("git", "merge", "--abort").Run()
		return false, nil, fmt.Errorf("commit failed: %s", err)
	}

	// 清理 worktree
	wtm.RemoveWorktree(branchName, false)
	return true, nil, nil
}

// GetCurrentConflictedFiles 获取当前工作目录中有冲突的文件列表
func (wtm *WorktreeManager) GetCurrentConflictedFiles() ([]string, error) {
	cmd := exec.Command("git", "diff", "--nameOnly", "--diff-filter=U")
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get conflicted files: %s", string(output))
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	var result []string
	for _, f := range files {
		if f != "" {
			result = append(result, f)
		}
	}
	return result, nil
}

// AutoResolveConflicts 尝试自动解决冲突文件
func (wtm *WorktreeManager) AutoResolveConflicts(conflictFiles []string) ([]string, error) {
	var resolved []string
	for _, file := range conflictFiles {
		if err := wtm.resolveConflictFile(file); err == nil {
			resolved = append(resolved, file)
		}
	}
	if len(resolved) == 0 && len(conflictFiles) > 0 {
		return nil, fmt.Errorf("failed to auto-resolve any conflicts")
	}
	return resolved, nil
}

// resolveConflictFile 解决单个冲突文件
func (wtm *WorktreeManager) resolveConflictFile(filePath string) error {
	filePath = filepath.Join(wtm.gitRoot, filePath)

	// 读取冲突文件
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// 简单的冲突标记解析
	resolved := wtm.extractResolvedContent(string(content))

	// 写入解决后的内容
	return os.WriteFile(filePath, []byte(resolved), 0644)
}

// extractResolvedContent 从冲突文件中提取解决后的内容
func (wtm *WorktreeManager) extractResolvedContent(content string) string {
	// 使用默认冲突解决策略
	resolver := wtm.conflictResolver
	if resolver == nil {
		resolver = &DefaultConflictResolver{}
	}

	var result strings.Builder
	lines := strings.Split(content, "\n")
	inConflict := false
	var localLines, remoteLines, baseLines []string
	var currentSection string

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "<<<<<<<"):
			inConflict = true
			currentSection = "local"
			localLines = nil
		case strings.HasPrefix(line, "|||||||"):
			currentSection = "base"
			baseLines = nil
		case strings.HasPrefix(line, "======="):
			currentSection = "remote"
		case strings.HasPrefix(line, ">>>>>>>"):
			// 解决冲突
			resolved, err := resolver.ResolveConflict(
				strings.Join(localLines, "\n"),
				strings.Join(remoteLines, "\n"),
				strings.Join(baseLines, "\n"),
			)
			if err == nil {
				result.WriteString(resolved)
			} else {
				// 回退：使用 ours (local)
				result.WriteString(strings.Join(localLines, "\n"))
			}
			inConflict = false
		default:
			if inConflict {
				switch currentSection {
				case "local":
					localLines = append(localLines, line)
				case "base":
					baseLines = append(baseLines, line)
				case "remote":
					remoteLines = append(remoteLines, line)
				}
			} else {
				result.WriteString(line)
				result.WriteString("\n")
			}
		}
	}

	return result.String()
}

// ResolveConflicts 尝试解决 worktree 合并时的冲突
// 返回解决的冲突数和是否成功
func (wtm *WorktreeManager) ResolveConflicts(branchName string) (int, bool, error) {
	conflictFiles, err := wtm.GetCurrentConflictedFiles()
	if err != nil {
		return 0, false, err
	}

	if len(conflictFiles) == 0 {
		return 0, true, nil // 没有冲突
	}

	resolved, err := wtm.AutoResolveConflicts(conflictFiles)
	if err != nil {
		return 0, false, err
	}

	// 添加已解决的冲突文件到 git
	for _, f := range resolved {
		cmd := exec.Command("git", "add", f)
		cmd.Dir = wtm.gitRoot
		cmd.Run()
	}

	return len(resolved), len(resolved) == len(conflictFiles), nil
}

// GetWorktreeState 获取 worktree 状态
func (wtm *WorktreeManager) GetWorktreeState(branchName string) (*WorktreeState, bool) {
	wtm.mu.RLock()
	defer wtm.mu.RUnlock()
	state, ok := wtm.worktrees[branchName]
	return state, ok
}

// ListWorktrees 列出所有 worktrees
func (wtm *WorktreeManager) ListWorktrees() []*WorktreeState {
	wtm.mu.RLock()
	defer wtm.mu.RUnlock()

	states := make([]*WorktreeState, 0, len(wtm.worktrees))
	for _, state := range wtm.worktrees {
		states = append(states, state)
	}
	return states
}

// WorktreeConflictError 描述 worktree 合并冲突
type WorktreeConflictError struct {
	BranchName string
	Output     string
	Message    string
}

func (e *WorktreeConflictError) Error() string {
	return fmt.Sprintf("worktree conflict in %s: %s", e.BranchName, e.Message)
}

// GetConflicts 获取冲突文件列表
func (e *WorktreeConflictError) GetConflicts() []string {
	var conflicts []string
	for _, line := range strings.Split(e.Output, "\n") {
		if strings.Contains(line, "CONFLICT") || strings.Contains(line, "<<<<<<<") {
			// 提取冲突文件路径
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				conflicts = append(conflicts, strings.TrimSpace(parts[len(parts)-1]))
			}
		}
	}
	return conflicts
}

// GetConflictedFiles 获取冲突文件列表
func (wtm *WorktreeManager) AbortMerge(branchName string) error {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	// 中止 git 合并
	cmd := exec.Command("git", "merge", "--abort")
	cmd.Dir = wtm.gitRoot
	if _, err := cmd.CombinedOutput(); err != nil {
		// 合并可能已经中止，这通常是正常的
	}

	state, ok := wtm.worktrees[branchName]
	if ok {
		state.Status = "active"
	}
	return nil
}

// GetWorktreeChanges 获取 worktree 相对于主分支的更改统计
func (wtm *WorktreeManager) GetWorktreeChanges(branchName string) (int, int, error) {
	wtm.mu.RLock()
	_, ok := wtm.worktrees[branchName]
	wtm.mu.RUnlock()

	if !ok {
		return 0, 0, fmt.Errorf("worktree %s not found", branchName)
	}

	// 获取文件变更统计
	cmd := exec.Command("git", "diff", "--stat", "HEAD", branchName)
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get diff stat: %s", string(output))
	}

	// 解析输出获取文件数量和行数
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var filesChanged, insertions, deletions int

	for _, line := range lines {
		// 解析 " path | N insertions(+), M deletions(-)" 格式
		if strings.Contains(line, " insertions(+),") || strings.Contains(line, " deletions(-)") {
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				stats := strings.TrimSpace(parts[1])
				// 提取数字
				re := regexp.MustCompile(`(\d+)`)
				nums := re.FindAllString(stats, -1)
				if len(nums) >= 2 {
					filesChanged++
					if v, _ := strconv.Atoi(nums[0]); v > 0 {
						insertions += v
					}
					if len(nums) >= 2 {
						if v, _ := strconv.Atoi(nums[1]); v > 0 {
							deletions += v
						}
					}
				}
			}
		}
	}

	return filesChanged, insertions + deletions, nil
}

// CleanupStaleWorktrees 清理孤立的 worktree 目录
func (wtm *WorktreeManager) CleanupStaleWorktrees() ([]string, error) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	var cleaned []string

	// 列出所有 worktree 目录
	entries, err := os.ReadDir(wtm.worktreeRoot)
	if err != nil {
		return cleaned, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		branchName := entry.Name()
		// 检查是否在管理列表中
		if _, managed := wtm.worktrees[branchName]; managed {
			continue
		}

		// 检查目录是否孤立（不在 git worktree list 中）
		worktreeDir := filepath.Join(wtm.worktreeRoot, branchName)
		cmd := exec.Command("git", "worktree", "list")
		cmd.Dir = wtm.gitRoot
		output, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}

		if !strings.Contains(string(output), worktreeDir) {
			// 孤立的 worktree，尝试清理
			if err := os.RemoveAll(worktreeDir); err == nil {
				cleaned = append(cleaned, branchName)
			}
		}
	}

	return cleaned, nil
}

// ListConflicted 返回所有有冲突的 worktrees
func (wtm *WorktreeManager) ListConflicted() []*WorktreeState {
	wtm.mu.RLock()
	defer wtm.mu.RUnlock()

	var conflicted []*WorktreeState
	for _, state := range wtm.worktrees {
		if state.Status == "conflicted" {
			conflicted = append(conflicted, state)
		}
	}
	return conflicted
}

// ResolveConflictsManually 手动解决 worktree 合并冲突（接受 ours/theirs/abort）
func (wtm *WorktreeManager) ResolveConflictsManually(branchName string, resolution string) error {
	wtm.mu.Lock()
	state, ok := wtm.worktrees[branchName]
	if !ok {
		wtm.mu.Unlock()
		return fmt.Errorf("worktree %s not found", branchName)
	}
	if state.Status != "conflicted" {
		wtm.mu.Unlock()
		return fmt.Errorf("worktree %s is not in conflicted state", branchName)
	}
	wtm.mu.Unlock()

	switch resolution {
	case "accept-ours":
		cmd := exec.Command("git", "checkout", "--ours", ".")
		cmd.Dir = wtm.gitRoot
		if _, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to accept ours: %s", err)
		}
	case "accept-theirs":
		cmd := exec.Command("git", "checkout", "--theirs", ".")
		cmd.Dir = wtm.gitRoot
		if _, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to accept theirs: %s", err)
		}
	case "abort":
		cmd := exec.Command("git", "merge", "--abort")
		cmd.Dir = wtm.gitRoot
		if _, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to abort merge: %s", err)
		}
		wtm.RemoveWorktree(branchName, true)
		return nil
	default:
		return fmt.Errorf("unknown resolution: %s (use 'accept-ours', 'accept-theirs', or 'abort')", resolution)
	}

	// 标记冲突解决
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = wtm.gitRoot
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stage resolved files: %s", err)
	}

	cmd = exec.Command("git", "commit", "-m", fmt.Sprintf("Merge worktree %s (resolved)", branchName))
	cmd.Dir = wtm.gitRoot
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to commit resolution: %s", err)
	}

	wtm.mu.Lock()
	if state != nil {
		state.Status = "merged"
	}
	wtm.mu.Unlock()

	wtm.RemoveWorktree(branchName, true)
	return nil
}

// GetConflictedFilesForBranch 获取指定分支的冲突文件列表
func (wtm *WorktreeManager) GetConflictedFilesForBranch(branchName string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = wtm.gitRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get conflicted files: %s", err)
	}

	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// CleanupAbandoned 清理所有被遗弃的 worktrees
func (wtm *WorktreeManager) CleanupAbandoned() int {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	var cleaned int
	for name, state := range wtm.worktrees {
		if state.Status == "abandoned" {
			if err := wtm.removeWorktreeDir(state.Path, true); err == nil {
				delete(wtm.worktrees, name)
				cleaned++
			}
		}
	}
	return cleaned
}

// MarkAbandoned 标记 worktree 为遗弃状态
func (wtm *WorktreeManager) MarkAbandoned(branchName string) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()
	if state, ok := wtm.worktrees[branchName]; ok {
		state.Status = "abandoned"
	}
}

// Legacy worktree functions for backward compatibility
// CreateWorktree creates a git worktree for isolated sub-agent file operations.
func (c *Coordinator) CreateWorktree(branchName string) (string, error) {
	if c.worktreeManager != nil {
		state, err := c.worktreeManager.CreateWorktree("", branchName)
		if err != nil {
			return "", err
		}
		return state.Path, nil
	}
	// Legacy implementation
	worktreeDir := filepath.Join(c.rootPath, ".tiancan", "worktrees", branchName)
	os.MkdirAll(filepath.Dir(worktreeDir), 0755)

	// Try git worktree add
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", branchName)
	cmd.Dir = c.rootPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If branch already exists, try without -b
		cmd = exec.Command("git", "worktree", "add", worktreeDir, branchName)
		cmd.Dir = c.rootPath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git worktree add failed: %s", string(output[:min(len(output), 300)]))
		}
	}
	return worktreeDir, nil
}

// RemoveWorktree removes a git worktree after sub-agent completion.
func (c *Coordinator) RemoveWorktree(worktreeDir string) error {
	if c.worktreeManager != nil {
		branchName := filepath.Base(worktreeDir)
		return c.worktreeManager.RemoveWorktree(branchName, true)
	}
	// Legacy implementation
	cmd := exec.Command("git", "worktree", "remove", worktreeDir, "--force")
	cmd.Dir = c.rootPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove failed: %s", string(output[:min(len(output), 300)]))
	}
	return nil
}

// MergeWorktree merges a worktree branch back into the main branch.
func (c *Coordinator) MergeWorktree(branchName string) error {
	if c.worktreeManager != nil {
		return c.worktreeManager.MergeWorktree(branchName)
	}
	// Legacy implementation
	cmd := exec.Command("git", "merge", branchName, "--no-edit")
	cmd.Dir = c.rootPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git merge failed: %s", string(output[:min(len(output), 300)]))
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Result Aggregation ─────────────────────────────────────────────

// AggregatedResult holds merged results from parallel sub-agents.
type AggregatedResult struct {
	TotalAgents  int               `json:"totalAgents"`
	SuccessCount int               `json:"successCount"`
	FailCount    int               `json:"failCount"`
	Results      []AgentResult     `json:"results"`
	Summary      string            `json:"summary"`
	SharedState  map[string]string `json:"sharedState"` // merged scratchpad state
}

// AggregateResults merges results from multiple parallel sub-agents.
func (c *Coordinator) AggregateResults(agentIDs []string) *AggregatedResult {
	agg := &AggregatedResult{
		TotalAgents: len(agentIDs),
		Results:     make([]AgentResult, 0, len(agentIDs)),
		SharedState: make(map[string]string),
	}

	var successes, failures []string
	for _, id := range agentIDs {
		result, ok := c.GetAgentResult(id)
		if !ok {
			failures = append(failures, id)
			agg.FailCount++
			continue
		}
		agg.Results = append(agg.Results, *result)
		if result.Error != "" {
			failures = append(failures, fmt.Sprintf("%s(%s)", id, result.Error[:min(len(result.Error), 50)]))
			agg.FailCount++
		} else {
			successes = append(successes, id)
			agg.SuccessCount++
		}
	}

	// Merge scratchpad state
	keys := c.scratchpad.List()
	for _, k := range keys {
		if v, err := c.scratchpad.Read(k); err == nil {
			agg.SharedState[k] = v
		}
	}

	// Build summary
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Aggregated %d agents: %d succeeded, %d failed\n", agg.TotalAgents, agg.SuccessCount, agg.FailCount))
	if len(successes) > 0 {
		sb.WriteString(fmt.Sprintf("Success: %s\n", strings.Join(successes, ", ")))
	}
	if len(failures) > 0 {
		sb.WriteString(fmt.Sprintf("Failed: %s\n", strings.Join(failures, ", ")))
	}
	agg.Summary = sb.String()

	return agg
}

// ─── Multi-level Sub-agent Orchestration ──────────────────────────────

// SpawnNestedAgent spawns a sub-agent with depth tracking to limit nesting.
func (c *Coordinator) SpawnNestedAgent(ctx context.Context, config AgentSpawnConfig, currentDepth int) (*AgentResult, error) {
	if currentDepth >= c.maxDepth {
		return nil, fmt.Errorf("max sub-agent nesting depth (%d) reached", c.maxDepth)
	}

	// Create child coordinator sharing the same scratchpad and chatFn
	child := &Coordinator{
		agentDefs:     c.agentDefs,
		scratchpad:    c.scratchpad,
		chatFn:        c.chatFn,
		activeWorkers: make(map[string]*AgentResult),
		rootPath:      config.Cwd,
		isCoordinator: true,
		maxDepth:      c.maxDepth,
	}

	// If worktree isolation requested, create a worktree
	if config.Isolation == "worktree" && config.Cwd == "" {
		branchName := fmt.Sprintf("agent-%d", time.Now().UnixNano())
		worktreeDir, err := c.CreateWorktree(branchName)
		if err == nil {
			config.Cwd = worktreeDir
			defer c.RemoveWorktree(worktreeDir)
		}
	}

	result, err := child.SpawnAgent(ctx, config)
	if err != nil {
		return result, err
	}

	// If the sub-agent wants to spawn its own sub-agents, it can use
	// the coordinator reference embedded in its context.
	return result, nil
}
