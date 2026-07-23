package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if config.Isolation == "worktree" {
		worktreeBranch = fmt.Sprintf("agent-%d", time.Now().UnixNano())
		dir, err := c.CreateWorktree(worktreeBranch)
		if err != nil {
			// Worktree creation failed — proceed without isolation
			worktreeBranch = ""
		} else {
			worktreeDir = dir
			defer func() {
				// Auto-cleanup worktree after agent finishes
				if worktreeDir != "" {
					c.RemoveWorktree(worktreeDir)
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
	if worktreeBranch != "" {
		if err := c.MergeWorktree(worktreeBranch); err != nil {
			// Merge failed — report but don't fail the agent
			finalOutput += fmt.Sprintf("\n\n⚠ Worktree merge failed: %v", err)
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
	mu        sync.RWMutex
	worktrees map[string]*WorktreeState
	basePath  string
}

// NewWorktreeManager 创建 worktree 管理器
func NewWorktreeManager(basePath string) *WorktreeManager {
	worktreeRoot := filepath.Join(basePath, ".tiancan", "worktrees")
	os.MkdirAll(worktreeRoot, 0755)
	return &WorktreeManager{
		worktrees: make(map[string]*WorktreeState),
		basePath:  worktreeRoot,
	}
}

// CreateWorktree 创建一个新的隔离 worktree
func (wtm *WorktreeManager) CreateWorktree(agentID, branchName string) (*WorktreeState, error) {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	worktreeDir := filepath.Join(wtm.basePath, branchName)
	state := &WorktreeState{
		ID:        branchName,
		Branch:    branchName,
		Path:      worktreeDir,
		CreatedAt: time.Now(),
		Status:    "active",
		ParentID:  agentID,
	}

	// Try git worktree add
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", branchName)
	cmd.Dir = wtm.basePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If branch already exists, try without -b
		cmd = exec.Command("git", "worktree", "add", worktreeDir, branchName)
		cmd.Dir = wtm.basePath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git worktree add failed: %s", string(output[:min(len(output), 300)]))
		}
	}
	wtm.worktrees[branchName] = state
	return state, nil
}

// RemoveWorktree 移除一个 worktree
func (wtm *WorktreeManager) RemoveWorktree(branchName string, force bool) error {
	wtm.mu.Lock()
	defer wtm.mu.Unlock()

	state, ok := wtm.worktrees[branchName]
	if !ok {
		// Worktree not managed, try direct removal
		worktreeDir := filepath.Join(wtm.basePath, branchName)
		return wtm.removeWorktreeDir(worktreeDir, force)
	}

	if state.Status == "merging" {
		return fmt.Errorf("worktree %s is in merging state, cannot remove", branchName)
	}

	if err := wtm.removeWorktreeDir(state.Path, force); err != nil {
		return err
	}
	delete(wtm.worktrees, branchName)
	return nil
}

// removeWorktreeDir 执行实际的 worktree 移除
func (wtm *WorktreeManager) removeWorktreeDir(worktreeDir string, force bool) error {
	args := []string{"worktree", "remove", worktreeDir}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = wtm.basePath
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
	cmd.Dir = wtm.basePath
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
	cmd.Dir = wtm.basePath
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
	wtm.RemoveWorktree(branchName, true)
	return nil
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

// ResolveConflicts 解决 worktree 合并冲突
func (wtm *WorktreeManager) ResolveConflicts(branchName string, resolution string) error {
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
		cmd.Dir = wtm.basePath
		if _, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to accept ours: %s", err)
		}
	case "accept-theirs":
		cmd := exec.Command("git", "checkout", "--theirs", ".")
		cmd.Dir = wtm.basePath
		if _, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to accept theirs: %s", err)
		}
	case "abort":
		cmd := exec.Command("git", "merge", "--abort")
		cmd.Dir = wtm.basePath
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
	cmd.Dir = wtm.basePath
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stage resolved files: %s", err)
	}

	cmd = exec.Command("git", "commit", "-m", fmt.Sprintf("Merge worktree %s (resolved)", branchName))
	cmd.Dir = wtm.basePath
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

// WorktreeConflictError 表示合并冲突错误
type WorktreeConflictError struct {
	BranchName string
	Output     string
	Message    string
}

func (e *WorktreeConflictError) Error() string {
	return e.Message
}

// GetConflictedFiles 获取冲突文件列表
func (wtm *WorktreeManager) GetConflictedFiles(branchName string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = wtm.basePath
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

// GetWorktreeManager 获取 worktree 管理器
func (c *Coordinator) GetWorktreeManager() *WorktreeManager {
	return c.worktreeManager
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
