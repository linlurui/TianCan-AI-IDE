package hierarchy

import (
	"context"
	"fmt"
	"os"
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
	TeamName     string          `json:"team_name,omitempty"` // TODO: Implement team collaboration (shared scratchpad, real-time sync, role assignment)
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

// Coordinator manages the hierarchical agent system.
// TODO: Implement multi-level sub-agent orchestration (sub-agent can spawn its own sub-agents)
// TODO: Implement result aggregation from parallel sub-agents with structured merge
// TODO: Add worktree-based isolation for concurrent sub-agent file operations
// TODO: Replace hardcoded built-in agent definitions with config-driven loading (TIANCAN_AGENT_DEFS_DIR)
type Coordinator struct {
	agentDefs       map[string]*AgentDefinition
	scratchpad      *ScratchpadCoordination
	chatFn          types.ChatFunc
	activeWorkers   map[string]*AgentResult
	activeWorkersMu sync.Mutex
	isCoordinator   bool
	rootPath        string
}

// NewCoordinator creates a new coordinator.
func NewCoordinator(rootPath string, chatFn types.ChatFunc) *Coordinator {
	c := &Coordinator{
		agentDefs:     make(map[string]*AgentDefinition),
		scratchpad:    NewScratchpadCoordination(rootPath),
		chatFn:        chatFn,
		activeWorkers: make(map[string]*AgentResult),
		rootPath:      rootPath,
		isCoordinator: os.Getenv("TIANCAN_COORDINATOR_MODE") == "1",
	}

	// Register built-in agent definitions
	c.registerBuiltInAgents()
	return c
}

// IsCoordinatorMode returns whether coordinator mode is active.
func (c *Coordinator) IsCoordinatorMode() bool {
	return c.isCoordinator
}

// registerBuiltInAgents registers the default agent types.
// Mirrors Claude Code's builtInAgents.ts.
func (c *Coordinator) registerBuiltInAgents() {
	c.agentDefs["general-purpose"] = &AgentDefinition{
		AgentType:      "general-purpose",
		Description:    "General-purpose coding assistant",
		Tools:          []string{"*"},
		Prompt:         "You are a general-purpose coding assistant. Complete the task assigned to you.",
		PermissionMode: "acceptEdits",
		MaxTurns:       200,
	}

	c.agentDefs["verify"] = &AgentDefinition{
		AgentType:      "verify",
		Description:    "Verification agent that checks work for correctness",
		Tools:          []string{"read_file", "grep", "glob", "list_directory", "bash"},
		Prompt:         "You are a verification agent. Your job is to verify that the work done is correct and complete. Check for errors, missing steps, and inconsistencies. Report your findings clearly.",
		PermissionMode: "acceptEdits",
		MaxTurns:       50,
	}

	c.agentDefs["research"] = &AgentDefinition{
		AgentType:      "research",
		Description:    "Research agent that explores codebases and gathers information",
		Tools:          []string{"read_file", "grep", "glob", "list_directory"},
		Prompt:         "You are a research agent. Your job is to explore the codebase, gather information, and report findings. Do not make any changes — only read and search.",
		PermissionMode: "acceptEdits",
		MaxTurns:       50,
	}

	c.agentDefs["fix"] = &AgentDefinition{
		AgentType:      "fix",
		Description:    "Bug-fixing agent that diagnoses and resolves issues",
		Tools:          []string{"*"},
		Prompt:         "You are a bug-fixing agent. Diagnose the issue described, identify the root cause, and implement a fix. Verify the fix works correctly.",
		PermissionMode: "acceptEdits",
		MaxTurns:       100,
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

// SpawnAgent spawns a sub-agent to execute a task.
// Mirrors Claude Code's AgentTool execution flow.
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

	// Build the agent's prompt
	systemPrompt := c.buildAgentSystemPrompt(agentDef, config)
	userPrompt := config.Prompt

	// Execute the agent via chatFn
	response, err := c.chatFn(systemPrompt, []types.Message{
		{Role: types.RoleUser, Content: userPrompt, Timestamp: time.Now()},
	})
	durationMs := time.Since(start).Milliseconds()

	result := &AgentResult{
		AgentID:    agentID,
		AgentType:  agentType,
		DurationMs: durationMs,
		IsComplete: true,
	}

	if err != nil {
		result.Error = err.Error()
		return result, err
	}

	result.Output = response
	result.TotalTokens = len(response) / 4 // rough estimate

	// Track the worker
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
