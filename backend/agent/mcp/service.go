package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// Service exposes MCP functionality to the frontend via Wails.
type Service struct {
	client       *Client
	configMgr    *MCPConfigManager
	resourceMgr  *ResourceManager
	promptMgr    *PromptManager
	toolDisc     *ToolDiscovery
	configPath   string
	connected    bool
	mu           sync.RWMutex
}

// NewService creates a new MCP service instance.
func NewService() *Service {
	dir, _ := os.UserHomeDir()
	cfgDir := filepath.Join(dir, ".tiancan")
	_ = os.MkdirAll(cfgDir, 0o755)

	svc := &Service{
		client:     NewClient(),
		configMgr:  NewMCPConfigManager(),
		configPath: filepath.Join(cfgDir, "mcp.json"),
	}

	// Load existing configs
	_ = svc.loadConfigs()

	// Initialize managers
	svc.resourceMgr = NewResourceManager(svc.client, 5*60*1e9) // 5 min cache
	svc.promptMgr = NewPromptManager(svc.client)
	svc.toolDisc = NewToolDiscovery(svc.client)

	// Auto-connect servers on startup
	go svc.autoConnect()

	return svc
}

// autoConnect connects to all configured servers.
func (s *Service) autoConnect() {
	configs := s.configMgr.GetConfigs()
	for name, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		_, _ = s.client.ConnectToServer(name, cfg)
	}
	s.mu.Lock()
	s.connected = true
	s.mu.Unlock()
}

// loadConfigs loads MCP configs from file.
func (s *Service) loadConfigs() error {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var configs map[string]types.MCPConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return err
	}
	for name, cfg := range configs {
		s.configMgr.SetConfig(name, cfg)
	}
	return nil
}

// saveConfigs persists MCP configs to file.
func (s *Service) saveConfigs() error {
	configs := s.configMgr.GetConfigs()
	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.configPath, data, 0o644)
}

// ---- Public API (Wails bindings) ----

// GetServers returns all MCP server states.
func (s *Service) GetServers() []types.MCPServerState {
	return s.client.GetServers()
}

// ConnectServer establishes connection to an MCP server.
func (s *Service) ConnectServer(name string, cfg types.MCPConfig) (string, error) {
	state, err := s.client.ConnectToServer(name, cfg)
	if err != nil {
		return "failed", fmt.Errorf("connect to %s: %w", name, err)
	}
	// Save config
	s.configMgr.SetConfig(name, cfg)
	_ = s.saveConfigs()
	return string(state.Type), nil
}

// DisconnectServer closes connection to an MCP server.
func (s *Service) DisconnectServer(name string) error {
	err := s.client.DisconnectServer(name)
	if err != nil {
		return fmt.Errorf("disconnect %s: %w", name, err)
	}
	return nil
}

// ToggleServer enables or disables a server (updates config).
func (s *Service) ToggleServer(name string, enabled bool) error {
	cfg, ok := s.configMgr.GetConfig(name)
	if !ok {
		return fmt.Errorf("server not found: %s", name)
	}
	cfg.Disabled = !enabled
	s.configMgr.SetConfig(name, cfg)
	_ = s.saveConfigs()

	if enabled {
		_, err := s.client.ConnectToServer(name, cfg)
		return err
	}
	return s.client.DisconnectServer(name)
}

// CallTool invokes an MCP tool by its full name.
func (s *Service) CallTool(toolName string, args map[string]interface{}) (string, bool, error) {
	result, err := s.client.CallTool(context.Background(), toolName, args)
	if err != nil {
		return "", false, fmt.Errorf("call tool %s: %w", toolName, err)
	}
	return result.Content, result.Success, nil
}

// ListTools returns all available MCP tools.
func (s *Service) ListTools() []types.MCPToolInfo {
	return s.client.GetTools()
}

// ListResources returns all available MCP resources.
func (s *Service) ListResources() []types.MCPResourceInfo {
	return s.resourceMgr.ListResources(context.Background())
}

// ReadResource reads a specific resource by URI.
func (s *Service) ReadResource(uri string) (string, string, error) {
	return s.resourceMgr.ReadResource(context.Background(), uri)
}

// ListPrompts returns all available MCP prompts.
func (s *Service) ListPrompts() []types.MCPPromptInfo {
	return s.promptMgr.ListPrompts(context.Background())
}

// GetPrompt renders a prompt template with arguments.
func (s *Service) GetPrompt(name string, args map[string]string) ([]types.Message, error) {
	return s.promptMgr.GetPrompt(context.Background(), name, args)
}

// GetConfigs returns all MCP configurations (without secrets).
func (s *Service) GetConfigs() map[string]types.MCPConfig {
	return s.configMgr.GetConfigs()
}

// SaveConfig adds or updates a server configuration.
func (s *Service) SaveConfig(name string, cfg types.MCPConfig) error {
	s.configMgr.SetConfig(name, cfg)
	return s.saveConfigs()
}

// DeleteConfig removes a server configuration.
func (s *Service) DeleteConfig(name string) error {
	// Disconnect if connected
	_ = s.client.DisconnectServer(name)
	s.configMgr.RemoveConfig(name)
	return s.saveConfigs()
}

// RefreshTools re-discovers tools from all connected servers.
func (s *Service) RefreshTools() error {
	return s.toolDisc.RefreshTools(context.Background())
}

// GetToolDescriptions returns tool descriptions for system prompt.
func (s *Service) GetToolDescriptions() string {
	return s.toolDisc.GetToolDescriptions()
}

// IsToolAllowed checks if a tool can be called.
func (s *Service) IsToolAllowed(serverName, toolName string) bool {
	return s.configMgr.IsToolAllowed(serverName, toolName)
}

// SetPermission sets tool permissions for a server.
func (s *Service) SetPermission(serverName string, allowedTools []string, deniedTools []string, readOnly bool) {
	s.configMgr.SetPermission(&ChannelPermission{
		ServerName:   serverName,
		AllowedTools: allowedTools,
		DeniedTools:  deniedTools,
		ReadOnly:     readOnly,
	})
}

// GetPermissions returns all channel permissions.
func (s *Service) GetPermissions() map[string]*ChannelPermission {
	return s.configMgr.GetAllPermissions()
}
