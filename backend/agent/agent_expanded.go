package agent

import (
	"context"
	"fmt"
	"os"
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
	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Enhanced Agent V2 ───────────────────────────────────────────

// AgentV2 is an enhanced orchestrator that integrates all expanded subsystems.
type AgentV2 struct {
	rootPath     string
	homeDir      string
	registry     *tools.Registry
	roleReg      *roles.Registry
	classifier   *roles.TaskClassifier
	permMgr      *safety.PermissionManager
	memoryStore  *memory.MemoryStore
	ctxWindow    *memory.ContextWindow
	convMem      *memory.ConversationMemory
	advCompactor *compact.AdvancedCompactor
	rollback     *ralph.RollbackManager
	mcpResMgr    *mcp.ResourceManager
	mcpPromptMgr *mcp.PromptManager
	mcpToolDisc  *mcp.ToolDiscovery
	dagExec      *parallel.DAGExecutor
	resultAgg    *parallel.ResultAggregator
	configRes    *config.ConfigResolver
	mcpClient    *mcp.Client

	mu        sync.Mutex
	messages  []Message
	sessionID string
}

// NewAgentV2 creates an enhanced agent with all expanded subsystems.
func NewAgentV2(rootPath string, chatFn compact.ChatFunc) *AgentV2 {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = "/tmp"
	}

	registry := tools.NewRegistry(rootPath)
	roleReg := roles.NewRegistry()
	classifier := roles.NewTaskClassifier(roleReg)
	permMgr := safety.NewPermissionManager(rootPath, safety.ModeDefault)
	memoryStore := memory.NewMemoryStore(rootPath, 10000)
	ctxWindow := memory.NewContextWindow(128000)
	convMem := memory.NewConversationMemory(fmt.Sprintf("sess_%d", time.Now().UnixNano()))
	advCompactor := compact.NewAdvancedCompactor(200000, chatFn)
	rollbackMgr := ralph.NewRollbackManager(rootPath)
	mcpClient := mcp.NewClient()
	mcpResMgr := mcp.NewResourceManager(mcpClient, 5*time.Minute)
	mcpPromptMgr := mcp.NewPromptManager(mcpClient)
	mcpToolDisc := mcp.NewToolDiscovery(mcpClient)
	configRes := config.NewConfigResolver()
	resultAgg := parallel.NewResultAggregator()

	a := &AgentV2{
		rootPath:     rootPath,
		homeDir:      homeDir,
		registry:     registry,
		roleReg:      roleReg,
		classifier:   classifier,
		permMgr:      permMgr,
		memoryStore:  memoryStore,
		ctxWindow:    ctxWindow,
		convMem:      convMem,
		advCompactor: advCompactor,
		rollback:     rollbackMgr,
		mcpClient:    mcpClient,
		mcpResMgr:    mcpResMgr,
		mcpPromptMgr: mcpPromptMgr,
		mcpToolDisc:  mcpToolDisc,
		configRes:    configRes,
		resultAgg:    resultAgg,
		sessionID:    fmt.Sprintf("sess_%d", time.Now().UnixNano()),
	}

	// Load configs
	a.configRes.LoadConfigs(rootPath, homeDir)

	return a
}

// RunV2 executes the enhanced agent loop with intelligent routing.
func (a *AgentV2) RunV2(ctx context.Context, userMessage string, maxIter int, chatFn ChatFunc) AgentRunResult {
	if maxIter <= 0 {
		maxIter = 20
	}
	start := time.Now()

	// Analyze task for routing
	analysis := a.classifier.Analyze(userMessage)

	// Select agent definition based on analysis
	agentDef, ok := a.roleReg.Get(analysis.PrimaryRole)
	if !ok {
		agentDef = roles.GeneralAgent
	}

	// Record in conversation memory
	a.convMem.AddMessage(Message{
		Role:    RoleUser,
		Content: userMessage,
	})

	// Inject memory context
	memContext := a.memoryStore.GetContextForQuery(userMessage, 2000)

	// Build enhanced system prompt
	systemPrompt := a.buildV2SystemPrompt(agentDef, analysis, memContext)

	// Initialize messages
	a.mu.Lock()
	a.messages = append(a.messages, Message{
		Role:      RoleUser,
		Content:   userMessage,
		Timestamp: time.Now(),
	})
	msgs := make([]Message, len(a.messages))
	copy(msgs, a.messages)
	a.mu.Unlock()

	totalTokens := 0

	for i := 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			return AgentRunResult{Done: true, Error: "cancelled", Messages: msgs, DurationMs: time.Since(start).Milliseconds()}
		default:
		}

		// Advanced compaction check
		if a.advCompactor.Plan(msgs) != nil {
			result, _, err := a.advCompactor.Execute(msgs, a.advCompactor.Plan(msgs))
			if err == nil && result != nil {
				msgs = append(result.SummaryMessages, msgs[len(msgs)-4:]...)
			}
		}

		// Get AI response
		response, err := chatFn(systemPrompt, msgs)
		if err != nil {
			return AgentRunResult{
				Done: true, Error: fmt.Sprintf("AI failed: %v", err),
				Messages: msgs, DurationMs: time.Since(start).Milliseconds(),
			}
		}

		assistantMsg := Message{Role: RoleAssistant, Content: response, Timestamp: time.Now()}
		msgs = append(msgs, assistantMsg)
		a.convMem.AddMessage(Message{Role: RoleAssistant, Content: response})
		totalTokens += len(response) / 4

		// Parse tool calls
		toolCalls := parseToolCallFromResponse(response)
		if len(toolCalls) == 0 {
			a.mu.Lock()
			a.messages = msgs
			a.mu.Unlock()

			// Extract and store memories
			a.extractAndStoreMemories(msgs)

			return AgentRunResult{
				Done: true, Messages: msgs, TotalTokens: totalTokens,
				AgentType:  string(analysis.PrimaryRole),
				DurationMs: time.Since(start).Milliseconds(),
			}
		}

		// Execute tool calls with role-based permission checks
		for _, tc := range toolCalls {
			// Check if tool is allowed for this role
			if !a.roleReg.IsToolAllowed(analysis.PrimaryRole, tc.Name) {
				msgs = append(msgs, Message{
					Role: RoleTool, Content: fmt.Sprintf("Tool %s not allowed for role %s", tc.Name, analysis.PrimaryRole),
					ToolName: tc.Name, Success: false,
				})
				continue
			}

			// Check permissions
			tool, found := a.registry.Get(tc.Name)
			if !found {
				// Try MCP
				mcpResult, mcpErr := a.mcpClient.CallTool(ctx, tc.Name, tc.Args)
				if mcpErr != nil {
					msgs = append(msgs, Message{Role: RoleTool, Content: fmt.Sprintf("Tool not found: %s", tc.Name), ToolName: tc.Name, Success: false})
				} else {
					msgs = append(msgs, Message{Role: RoleTool, Content: mcpResult.Content, ToolName: tc.Name, Success: mcpResult.Success})
				}
				continue
			}

			permResult := a.permMgr.Check(tool, tc.Args)
			switch permResult.Decision {
			case DecisionDeny:
				msgs = append(msgs, Message{
					Role: RoleTool, Content: fmt.Sprintf("Denied: %s", permResult.Reason),
					ToolName: tc.Name, Success: false,
				})
			case DecisionAsk:
				msgs = append(msgs, Message{
					Role: RoleTool, Content: fmt.Sprintf("Needs confirmation: %s", permResult.Reason),
					ToolName: tc.Name, Success: false,
				})
			case DecisionAllow:
				// Snapshot for rollback if write operation
				if !tool.IsReadOnly() {
					if path, ok := tc.Args["path"].(string); ok {
						a.rollback.Snapshot(path)
					}
				}
				result, err := tool.Execute(ctx, tc.Args)
				if err != nil {
					result = ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
				}
				msgs = append(msgs, Message{
					Role: RoleTool, Content: result.Content, ToolName: tc.Name, Success: result.Success,
				})
				totalTokens += len(result.Content) / 4
				a.convMem.AddMessage(Message{Role: RoleTool, Content: result.Content, ToolName: tc.Name, Success: result.Success})
			}
		}
	}

	a.mu.Lock()
	a.messages = msgs
	a.mu.Unlock()

	return AgentRunResult{
		Done: true, Messages: msgs, TotalTokens: totalTokens,
		AgentType: string(analysis.PrimaryRole), DurationMs: time.Since(start).Milliseconds(),
	}
}

// RunRalphV2 executes with the enhanced Ralph Loop V2 (with rollback).
func (a *AgentV2) RunRalphV2(ctx context.Context, task string, chatFn ChatFunc) AgentRunResult {
	start := time.Now()
	analysis := a.classifier.Analyze(task)
	agentDef, ok := a.roleReg.Get(analysis.PrimaryRole)
	if !ok {
		agentDef = roles.GeneralAgent
	}

	cfg := ralph.DefaultRalphConfig()
	cfg.EnableRollback = true
	cfg.AutoRollbackOnFail = false

	rl := ralph.NewRalphLoopV2(
		func(systemPrompt string, msgs []Message) (string, error) {
			return chatFn(systemPrompt, msgs)
		},
		func(ctx context.Context, toolName string, args map[string]interface{}) (ToolResult, error) {
			return a.registry.Execute(ctx, toolName, args)
		},
		a.rootPath, cfg,
	)

	result, err := rl.Run(ctx, task, agentDef)
	if err != nil {
		return AgentRunResult{
			Done: true, Error: fmt.Sprintf("Ralph V2 error: %v", err),
			Messages: result.Messages, DurationMs: time.Since(start).Milliseconds(),
		}
	}

	return AgentRunResult{
		Done: true, Messages: result.Messages,
		AgentType:   string(analysis.PrimaryRole),
		TotalTokens: compact.EstimateTokens(result.Messages),
		DurationMs:  time.Since(start).Milliseconds(),
		RalphResult: result,
	}
}

// RunParallelV2 executes subtasks in parallel using the DAG executor.
func (a *AgentV2) RunParallelV2(ctx context.Context, task string, chatFn ChatFunc) AgentRunResult {
	start := time.Now()
	analysis := a.classifier.Analyze(task)

	if !analysis.IsRoutable || len(analysis.Subtasks) == 0 {
		return a.RunV2(ctx, task, 20, chatFn)
	}

	// Build dependency graph
	graph := parallel.NewDependencyGraph()
	for i, st := range analysis.Subtasks {
		task := types.ParallelTask{
			ID:          fmt.Sprintf("subtask_%d", i),
			Description: st.Prompt,
			Prompt:      st.Prompt,
			AgentType:   types.AgentRole(st.AgentType),
			Status:      types.ParallelPending,
		}
		var deps []string
		// Non-parallel subtasks depend on the previous one
		if !st.Parallel && i > 0 {
			deps = []string{fmt.Sprintf("subtask_%d", i-1)}
		}
		graph.AddTask(task, deps)
	}

	execFn := func(ctx context.Context, pt types.ParallelTask) (*types.ParallelTaskResult, error) {
		agentDef, ok := a.roleReg.Get(pt.AgentType)
		if !ok {
			agentDef = roles.GeneralAgent
		}
		systemPrompt := a.buildV2SystemPrompt(agentDef, analysis, "")
		resp, err := chatFn(systemPrompt, []Message{{Role: RoleUser, Content: pt.Prompt}})
		if err != nil {
			return nil, err
		}
		return &types.ParallelTaskResult{Content: resp, Tokens: len(resp) / 4}, nil
	}

	dagExec := parallel.NewDAGExecutor(graph, execFn, 4)
	results := dagExec.Execute(ctx)

	// Aggregate results
	var allMessages []Message
	for _, r := range results {
		if r.Result != nil {
			allMessages = append(allMessages, Message{
				Role: RoleAssistant, Content: r.Result.Content,
			})
		}
	}

	return AgentRunResult{
		Done: true, Messages: allMessages,
		AgentType:  string(analysis.PrimaryRole),
		DurationMs: time.Since(start).Milliseconds(),
	}
}

// buildV2SystemPrompt builds an enhanced system prompt with all context.
func (a *AgentV2) buildV2SystemPrompt(agentDef types.AgentDefinition, analysis roles.TaskAnalysis, memContext string) string {
	var sb strings.Builder

	// Agent role prompt
	sb.WriteString(agentDef.SystemPrompt)
	sb.WriteString("\n\n")

	// Config instructions
	configPrompt := a.configRes.Resolve(a.rootPath)
	if configPrompt != "" {
		sb.WriteString(configPrompt)
		sb.WriteString("\n\n")
	}

	// Memory context
	if memContext != "" {
		sb.WriteString("# Relevant Memories\n\n")
		sb.WriteString(memContext)
		sb.WriteString("\n\n")
	}

	// Conversation topic
	if topic := a.convMem.CurrentTopic(); topic != "" {
		sb.WriteString(fmt.Sprintf("Current topic: %s\n\n", topic))
	}

	// Available tools (filtered by role)
	sb.WriteString("# Available Tools\n\n")
	for _, t := range a.registry.All() {
		if a.roleReg.IsToolAllowed(analysis.PrimaryRole, t.Name()) {
			ro := ""
			if t.IsReadOnly() {
				ro = " (read-only)"
			}
			sb.WriteString(fmt.Sprintf("- **%s**: %s%s\n", t.Name(), t.Description(), ro))
		}
	}

	// MCP tools
	for _, t := range a.mcpClient.GetTools() {
		if a.roleReg.IsToolAllowed(analysis.PrimaryRole, t.Name) {
			sb.WriteString(fmt.Sprintf("- **%s**: %s [MCP:%s]\n", t.Name, t.Description, t.ServerName))
		}
	}

	sb.WriteString("\n")

	// Permission mode
	sb.WriteString(fmt.Sprintf("Permission mode: %s\n", a.permMgr.GetMode()))
	sb.WriteString(fmt.Sprintf("Project root: %s\n", a.rootPath))

	// Task analysis context
	if analysis.IsRoutable {
		sb.WriteString(fmt.Sprintf("\nTask complexity: %s (estimated %d turns)\n", analysis.Complexity, analysis.EstimatedTurns))
	}

	return sb.String()
}

// extractAndStoreMemories extracts memories from conversation and stores them.
func (a *AgentV2) extractAndStoreMemories(messages []Message) {
	for _, msg := range messages {
		if msg.Role != RoleUser {
			continue
		}
		lower := strings.ToLower(msg.Content)
		shouldStore := strings.Contains(lower, "remember") ||
			strings.Contains(lower, "记住") ||
			strings.Contains(lower, "don't forget") ||
			strings.Contains(lower, "别忘了") ||
			strings.Contains(lower, "prefer") ||
			strings.Contains(lower, "喜欢")

		if shouldStore {
			a.memoryStore.Store(memory.MemoryEntry{
				Content:   msg.Content,
				Source:    "user",
				Type:      types.MemoryUser,
				Relevance: 0.8,
				Tags:      extractTags(msg.Content),
			})
		}
	}
}

// GetAnalysis returns task analysis for a given prompt.
func (a *AgentV2) GetAnalysis(prompt string) roles.TaskAnalysis {
	return a.classifier.Analyze(prompt)
}

// GetRoleRegistry returns the role registry.
func (a *AgentV2) GetRoleRegistry() *roles.Registry {
	return a.roleReg
}

// SetPermissionMode changes the permission mode.
func (a *AgentV2) SetPermissionMode(mode safety.PermissionMode) {
	a.permMgr.SetMode(mode)
}

// GetMemoryStore returns the memory store.
func (a *AgentV2) GetMemoryStore() *memory.MemoryStore {
	return a.memoryStore
}

// GetConversationMemory returns the conversation memory.
func (a *AgentV2) GetConversationMemory() *memory.ConversationMemory {
	return a.convMem
}

// Rollback reverts the last set of changes.
func (a *AgentV2) Rollback() error {
	return a.rollback.Rollback()
}

// extractTags extracts simple tags from content.
// Tag set is loaded from TIANCAN_TAG_SET env var — no hardcoded lists.
func extractTags(content string) []string {
	var tags []string
	words := strings.Fields(strings.ToLower(content))
	tagSet := loadTagSet()
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:，。！？；：")
		if tagSet[w] {
			tags = append(tags, w)
		}
	}
	return tags
}

// tagSetCache caches the env-loaded tag set.
var tagSetCache map[string]bool
var tagSetOnce sync.Once

// loadTagSet loads tag keywords from TIANCAN_TAG_SET env var.
// TIANCAN_TAG_SET=bug,feature,refactor,test,security,docs,config,...
func loadTagSet() map[string]bool {
	tagSetOnce.Do(func() {
		tagSetCache = make(map[string]bool)
		if v := os.Getenv("TIANCAN_TAG_SET"); v != "" {
			for _, t := range strings.Split(v, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tagSetCache[t] = true
				}
			}
		}
		if len(tagSetCache) == 0 {
			tagSetCache = map[string]bool{
				"bug": true, "feature": true, "refactor": true, "test": true,
				"security": true, "docs": true, "config": true, "ui": true,
				"api": true, "database": true, "performance": true,
				"修复": true, "功能": true, "重构": true, "测试": true,
				"安全": true, "文档": true, "配置": true,
			}
		}
	})
	return tagSetCache
}
