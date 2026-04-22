package types

import (
	"context"
	"time"
)

// MessageRole represents the role of a message sender.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
	RoleTool      MessageRole = "tool"
)

// Message represents a single message in the conversation.
type Message struct {
	Role      MessageRole `json:"role"`
	Content   string      `json:"content"`
	Timestamp time.Time   `json:"timestamp"`
	UUID      string      `json:"uuid"`
	// ToolUseID is set when this message is a tool result.
	ToolUseID string `json:"toolUseId,omitempty"`
	// ToolName is set when the assistant is invoking a tool.
	ToolName string `json:"toolName,omitempty"`
	// ToolArgs is set when the assistant is invoking a tool.
	ToolArgs map[string]interface{} `json:"toolArgs,omitempty"`
	// IsCompactSummary marks this as a compaction summary.
	IsCompactSummary bool `json:"isCompactSummary,omitempty"`
	// Success indicates tool execution success.
	Success bool `json:"success,omitempty"`
}

// ToolCall represents a parsed tool call from AI response.
type ToolCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// ToolResult represents the result of executing a tool.
type ToolResult struct {
	Content string `json:"content"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// Tool is the interface that all agent tools must implement.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]interface{}
	Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error)
	IsReadOnly() bool
	IsConcurrencySafe() bool
}

// PermissionDecision represents the outcome of a permission check.
type PermissionDecision string

const (
	DecisionAllow PermissionDecision = "allow"
	DecisionDeny  PermissionDecision = "deny"
	DecisionAsk   PermissionDecision = "ask"
)

// PermissionResult holds the result of a permission check.
type PermissionResult struct {
	Decision       PermissionDecision     `json:"decision"`
	UpdatedInput   map[string]interface{} `json:"updatedInput,omitempty"`
	Reason         string                 `json:"reason,omitempty"`
	DecisionReason *DecisionReason        `json:"decisionReason,omitempty"`
}

// DecisionReason provides context for a permission decision.
type DecisionReason struct {
	Type         string `json:"type"`
	RuleBehavior string `json:"ruleBehavior,omitempty"`
	Description  string `json:"description,omitempty"`
}

// AgentRole defines the role of an agent in the hierarchical system.
type AgentRole string

const (
	RoleRouter     AgentRole = "router"
	RoleCoder      AgentRole = "coder"
	RoleReviewer   AgentRole = "reviewer"
	RoleVerifier   AgentRole = "verifier"
	RoleGeneral    AgentRole = "general"
	RolePlanner    AgentRole = "planner"
	RoleDebugger   AgentRole = "debugger"
	RoleDoc        AgentRole = "doc"
	RoleSecurity   AgentRole = "security"
	RoleResearcher AgentRole = "researcher"
)

// AgentDefinition describes a specialized agent.
type AgentDefinition struct {
	AgentType       AgentRole `json:"agentType"`
	SystemPrompt    string    `json:"systemPrompt"`
	MaxTurns        int       `json:"maxTurns"`
	DisallowedTools []string  `json:"disallowedTools,omitempty"`
	AllowedTools    []string  `json:"allowedTools,omitempty"`
	PermissionMode  string    `json:"permissionMode"`
	Background      bool      `json:"background"`
	Model           string    `json:"model,omitempty"`
	Description     string    `json:"description"`
	WhenToUse       string    `json:"whenToUse,omitempty"`
}

// CompactionResult holds the result of context compression.
type CompactionResult struct {
	BoundaryMarker        string    `json:"boundaryMarker"`
	SummaryMessages       []Message `json:"summaryMessages"`
	PreCompactTokenCount  int       `json:"preCompactTokenCount"`
	PostCompactTokenCount int       `json:"postCompactTokenCount"`
}

// MemoryType classifies the source layer of a memory file.
type MemoryType string

const (
	MemoryManaged MemoryType = "Managed"
	MemoryUser    MemoryType = "User"
	MemoryProject MemoryType = "Project"
	MemoryLocal   MemoryType = "Local"
	MemoryAutoMem MemoryType = "AutoMem"
)

// MemoryFileInfo holds metadata about a loaded memory file.
type MemoryFileInfo struct {
	Path        string     `json:"path"`
	Type        MemoryType `json:"type"`
	Content     string     `json:"content"`
	Globs       []string   `json:"globs,omitempty"`
	Parent      string     `json:"parent,omitempty"`
	DisplayName string     `json:"displayName,omitempty"`
}

// YOLOClassifierResult holds the result of the auto-mode classifier.
type YOLOClassifierResult struct {
	ShouldBlock bool   `json:"shouldBlock"`
	Reason      string `json:"reason"`
	Unavailable bool   `json:"unavailable,omitempty"`
	Model       string `json:"model,omitempty"`
}

// RalphLoopResult holds the result of a Ralph Loop execution.
type RalphLoopResult struct {
	Verdict      RalphVerdict `json:"verdict"`
	Iterations   int          `json:"iterations"`
	FixesApplied int          `json:"fixesApplied"`
	Report       string       `json:"report"`
	Messages     []Message    `json:"messages"`
}

// RalphVerdict is the outcome of a Ralph Loop.
type RalphVerdict string

const (
	VerdictPass    RalphVerdict = "PASS"
	VerdictFail    RalphVerdict = "FAIL"
	VerdictPartial RalphVerdict = "PARTIAL"
)

// MCPConfig describes an MCP server configuration.
type MCPConfig struct {
	Type     MCPTransportType  `json:"type"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	URL      string            `json:"url,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// MCPTransportType defines the transport protocol for MCP.
type MCPTransportType string

const (
	MCPTransportStdio MCPTransportType = "stdio"
	MCPTransportHTTP  MCPTransportType = "http"
	MCPTransportSSE   MCPTransportType = "sse"
	MCPTransportWS    MCPTransportType = "ws"
)

// MCPServerState tracks the state of an MCP server connection.
type MCPServerState struct {
	Name   string            `json:"name"`
	Type   MCPServerConnType `json:"type"`
	Config MCPConfig         `json:"config"`
	Tools  []MCPToolInfo     `json:"tools,omitempty"`
	Error  string            `json:"error,omitempty"`
}

// MCPServerConnType represents the connection state.
type MCPServerConnType string

const (
	MCPConnConnected MCPServerConnType = "connected"
	MCPConnFailed    MCPServerConnType = "failed"
	MCPConnPending   MCPServerConnType = "pending"
	MCPConnDisabled  MCPServerConnType = "disabled"
	MCPConnNeedsAuth MCPServerConnType = "needs-auth"
)

// MCPResourceInfo describes a resource exposed by an MCP server.
type MCPResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	ServerName  string `json:"serverName"`
}

// MCPPromptInfo describes a prompt template exposed by an MCP server.
type MCPPromptInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ServerName  string `json:"serverName"`
}

// MCPToolInfo describes a tool exposed by an MCP server.
type MCPToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ServerName  string                 `json:"serverName"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
	ReadOnly    bool                   `json:"readOnly,omitempty"`
}

// ParallelTask represents a task for parallel execution.
type ParallelTask struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	Prompt      string              `json:"prompt"`
	AgentType   AgentRole           `json:"agentType"`
	Status      ParallelTaskStatus  `json:"status"`
	Result      *ParallelTaskResult `json:"result,omitempty"`
}

// ParallelTaskStatus tracks the status of a parallel task.
type ParallelTaskStatus string

const (
	ParallelPending   ParallelTaskStatus = "pending"
	ParallelRunning   ParallelTaskStatus = "running"
	ParallelCompleted ParallelTaskStatus = "completed"
	ParallelFailed    ParallelTaskStatus = "failed"
)

// ParallelTaskResult holds the result of a parallel task.
type ParallelTaskResult struct {
	Content    string `json:"content"`
	Tokens     int    `json:"tokens"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// ChatFunc is the function signature for sending messages to the AI.
type ChatFunc func(systemPrompt string, messages []Message) (string, error)
