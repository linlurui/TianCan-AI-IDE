package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
	"github.com/rocky233/tiancan-ai-ide/backend/sandbox"
)

// Registry manages dynamic tool registration and execution.
type Registry struct {
	builtin  map[string]types.Tool
	mcp      map[string]types.Tool
	mu       sync.RWMutex
	rootPath string
}

// NewRegistry creates a new tool registry with built-in tools.
func NewRegistry(rootPath string) *Registry {
	r := &Registry{
		builtin:  make(map[string]types.Tool),
		mcp:      make(map[string]types.Tool),
		rootPath: rootPath,
	}
	r.registerBuiltinTools()
	return r
}

// Register adds a tool to the registry.
func (r *Registry) Register(t types.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builtin[t.Name()] = t
}

// RegisterMCP adds an MCP tool to the registry.
func (r *Registry) RegisterMCP(t types.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mcp[t.Name()] = t
}

// UnregisterMCP removes all tools from a specific MCP server.
func (r *Registry) UnregisterMCP(serverName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.mcp {
		if strings.HasPrefix(name, "mcp__"+serverName+"__") {
			delete(r.mcp, name)
		}
	}
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (types.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.builtin[name]; ok {
		return t, true
	}
	if t, ok := r.mcp[name]; ok {
		return t, true
	}
	return nil, false
}

// All returns all registered tools.
func (r *Registry) All() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []types.Tool
	for _, t := range r.builtin {
		result = append(result, t)
	}
	for _, t := range r.mcp {
		result = append(result, t)
	}
	return result
}

// BuiltinTools returns only built-in tools.
func (r *Registry) BuiltinTools() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []types.Tool
	for _, t := range r.builtin {
		result = append(result, t)
	}
	return result
}

// MCPTools returns only MCP tools.
func (r *Registry) MCPTools() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []types.Tool
	for _, t := range r.mcp {
		result = append(result, t)
	}
	return result
}

// Execute runs a tool by name with the given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]interface{}) (types.ToolResult, error) {
	t, ok := r.Get(name)
	if !ok {
		return types.ToolResult{}, fmt.Errorf("tool not found: %s", name)
	}
	return t.Execute(ctx, args)
}

// registerBuiltinTools registers all built-in tools.
func (r *Registry) registerBuiltinTools() {
	// File tools (file_tools.go)
	r.Register(&ReadFileTool{rootPath: r.rootPath})
	r.Register(&WriteFileTool{rootPath: r.rootPath})
	r.Register(&EditFileTool{rootPath: r.rootPath})
	r.Register(&MultiEditFileTool{rootPath: r.rootPath})
	r.Register(&ListDirTool{rootPath: r.rootPath})
	// Search/shell tools (search_tools.go)
	r.Register(&BashTool{rootPath: r.rootPath})
	r.Register(&GrepTool{rootPath: r.rootPath})
	r.Register(&GlobTool{rootPath: r.rootPath})
	r.Register(&WebFetchTool{})
	// Git tools (git_tools.go)
	r.Register(&GitDiffTool{rootPath: r.rootPath})
	r.Register(&GitLogTool{rootPath: r.rootPath})
	r.Register(&GitShowTool{rootPath: r.rootPath})
	r.Register(&GitBlameTool{rootPath: r.rootPath})
	r.Register(&GitCommitTool{rootPath: r.rootPath})
	r.Register(&GitStatusTool{rootPath: r.rootPath})
	r.Register(&GitBranchTool{rootPath: r.rootPath})
	r.Register(&GitStashTool{rootPath: r.rootPath})
	// LSP tools (lsp_tools.go) — port set dynamically via SetLSPPort
	r.Register(&LSPDefinitionTool{rootPath: r.rootPath})
	r.Register(&LSPReferencesTool{rootPath: r.rootPath})
	r.Register(&LSPHoverTool{rootPath: r.rootPath})
	r.Register(&LSPSymbolsTool{rootPath: r.rootPath})
	r.Register(&LSPDiagnosticsTool{rootPath: r.rootPath})
	// Test tools (test_tools.go)
	r.Register(&TestRunTool{rootPath: r.rootPath})
	// Todo tools (todo_tools.go)
	r.Register(&TodoWriteTool{})
	r.Register(&TodoReadTool{})
	// Database tools (db_tools.go + db_manager.go)
	r.Register(&DBConnectTool{})
	r.Register(&DBDisconnectTool{})
	r.Register(&DBListConnectionsTool{})
	r.Register(&DBQueryTool{})
	r.Register(&DBExecuteTool{})
	r.Register(&DBListTablesTool{})
	r.Register(&DBDescribeTableTool{})
	// API testing tools (api_tools.go)
	r.Register(&APIRequestTool{})
	r.Register(&APIAssertTool{})
	r.Register(&APIRunCollectionTool{})
	r.Register(&APIGetVarsTool{})
	r.Register(&APISetVarTool{})
	// Advanced test tools (test_adv_tools.go)
	r.Register(&TestDiscoverTool{rootPath: r.rootPath})
	r.Register(&TestCoverageTool{rootPath: r.rootPath})
	r.Register(&TestReportTool{rootPath: r.rootPath})
	// Deploy tools (deploy_tools.go)
	r.Register(&DeployBuildTool{rootPath: r.rootPath})
	r.Register(&DeployRunTool{rootPath: r.rootPath})
	r.Register(&DeployStatusTool{})
	r.Register(&DeployRollbackTool{})
	r.Register(&DeployHistoryTool{})
	// AutoResearch tools (autoresearch_tools.go)
	r.Register(&AutoResearchTool{rootPath: r.rootPath})
	r.Register(&WebSearchTool{})
	// Sandbox tools (sandbox/tool.go)
	sbMgr := sandbox.NewManager()
	r.Register(&sandbox.SandboxCreateTool{Manager: sbMgr})
	r.Register(&sandbox.SandboxExecTool{Manager: sbMgr})
	r.Register(&sandbox.SandboxStopTool{Manager: sbMgr})
	// Self-verification tools (verify_tools.go)
	r.Register(&GenerateTestsTool{rootPath: r.rootPath})
	r.Register(&AssessCompletionTool{rootPath: r.rootPath})
}

// SetLSPPort updates the LSP port on all LSP tools in the registry.
func (r *Registry) SetLSPPort(port int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.builtin {
		switch v := t.(type) {
		case *LSPDefinitionTool:
			v.lspPort = port
		case *LSPReferencesTool:
			v.lspPort = port
		case *LSPHoverTool:
			v.lspPort = port
		case *LSPSymbolsTool:
			v.lspPort = port
		case *LSPDiagnosticsTool:
			v.lspPort = port
		}
	}
}

// resolvePath resolves a relative path against rootPath.
func resolvePath(path, rootPath string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(rootPath, path)
}
