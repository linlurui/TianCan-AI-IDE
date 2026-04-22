package agent

// Re-export types from the types sub-package to avoid import cycles.
// Sub-packages should import agent/types directly.
import "github.com/rocky233/tiancan-ai-ide/backend/agent/types"

type MessageRole = types.MessageRole
type Message = types.Message
type ToolCall = types.ToolCall
type ToolResult = types.ToolResult
type Tool = types.Tool
type PermissionDecision = types.PermissionDecision
type PermissionResult = types.PermissionResult
type DecisionReason = types.DecisionReason
type AgentRole = types.AgentRole
type AgentDefinition = types.AgentDefinition
type CompactionResult = types.CompactionResult
type MemoryType = types.MemoryType
type MemoryFileInfo = types.MemoryFileInfo
type YOLOClassifierResult = types.YOLOClassifierResult
type RalphLoopResult = types.RalphLoopResult
type RalphVerdict = types.RalphVerdict
type MCPConfig = types.MCPConfig
type MCPTransportType = types.MCPTransportType
type MCPServerState = types.MCPServerState
type MCPServerConnType = types.MCPServerConnType
type MCPToolInfo = types.MCPToolInfo
type ParallelTask = types.ParallelTask
type ParallelTaskStatus = types.ParallelTaskStatus
type ParallelTaskResult = types.ParallelTaskResult
type ChatFunc = types.ChatFunc

const (
	RoleUser      = types.RoleUser
	RoleAssistant = types.RoleAssistant
	RoleSystem    = types.RoleSystem
	RoleTool      = types.RoleTool

	DecisionAllow = types.DecisionAllow
	DecisionDeny  = types.DecisionDeny
	DecisionAsk   = types.DecisionAsk

	RoleRouter   = types.RoleRouter
	RoleCoder    = types.RoleCoder
	RoleReviewer = types.RoleReviewer
	RoleVerifier = types.RoleVerifier
	RoleGeneral  = types.RoleGeneral

	MemoryManaged = types.MemoryManaged
	MemoryUser    = types.MemoryUser
	MemoryProject = types.MemoryProject
	MemoryLocal   = types.MemoryLocal
	MemoryAutoMem = types.MemoryAutoMem

	VerdictPass    = types.VerdictPass
	VerdictFail    = types.VerdictFail
	VerdictPartial = types.VerdictPartial

	MCPTransportStdio = types.MCPTransportStdio
	MCPTransportHTTP  = types.MCPTransportHTTP
	MCPTransportSSE   = types.MCPTransportSSE
	MCPTransportWS    = types.MCPTransportWS

	MCPConnConnected = types.MCPConnConnected
	MCPConnFailed    = types.MCPConnFailed
	MCPConnPending   = types.MCPConnPending
	MCPConnDisabled  = types.MCPConnDisabled
	MCPConnNeedsAuth = types.MCPConnNeedsAuth

	ParallelPending   = types.ParallelPending
	ParallelRunning   = types.ParallelRunning
	ParallelCompleted = types.ParallelCompleted
	ParallelFailed    = types.ParallelFailed
)
