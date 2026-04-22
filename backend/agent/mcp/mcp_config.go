package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// MCPConfigFile represents the MCP server configuration file format.
// Mirrors Claude Code's MCP config file loading from settings.json and .mcp.json.
type MCPConfigFile struct {
	MCPServers map[string]types.MCPConfig `json:"mcpServers"`
}

// ChannelPermission defines what operations are allowed on an MCP channel.
// Mirrors Claude Code's channel allowlist/permissions.
type ChannelPermission struct {
	ServerName   string   `json:"serverName"`
	AllowedTools []string `json:"allowedTools,omitempty"` // nil/empty = all allowed
	DeniedTools  []string `json:"deniedTools,omitempty"`
	ReadOnly     bool     `json:"readOnly,omitempty"`
}

// OAuthConfig holds OAuth authentication configuration for MCP servers.
// Mirrors Claude Code's MCP OAuth support.
type OAuthConfig struct {
	Enabled      bool   `json:"enabled"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	Scopes       string `json:"scopes,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`  // runtime, not persisted
	RefreshToken string `json:"refreshToken,omitempty"` // runtime, not persisted
	Expiry       int64  `json:"expiry,omitempty"`       // unix timestamp
}

// MCPConfigManager manages MCP server configurations from multiple sources.
// Mirrors Claude Code's config file loading hierarchy.
type MCPConfigManager struct {
	configs      map[string]types.MCPConfig
	permissions  map[string]*ChannelPermission
	oauthConfigs map[string]*OAuthConfig
	mu           sync.RWMutex
	configPaths  []string // ordered by priority (highest first)
}

// NewMCPConfigManager creates a new config manager.
func NewMCPConfigManager() *MCPConfigManager {
	return &MCPConfigManager{
		configs:      make(map[string]types.MCPConfig),
		permissions:  make(map[string]*ChannelPermission),
		oauthConfigs: make(map[string]*OAuthConfig),
		configPaths:  []string{},
	}
}

// LoadFromDirectory loads MCP configs from all .mcp.json files in a directory tree.
// Mirrors Claude Code's hierarchical config loading.
func (m *MCPConfigManager) LoadFromDirectory(dir string) error {
	// Search for .mcp.json files
	patterns := []string{".mcp.json", "mcp.json"}
	for _, pattern := range patterns {
		path := filepath.Join(dir, pattern)
		if _, err := os.Stat(path); err == nil {
			if err := m.LoadFromFile(path); err != nil {
				return fmt.Errorf("loading %s: %w", path, err)
			}
			m.configPaths = append(m.configPaths, path)
		}
	}
	return nil
}

// LoadFromFile loads MCP configs from a specific JSON file.
func (m *MCPConfigManager) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfgFile MCPConfigFile
	if err := json.Unmarshal(data, &cfgFile); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cfg := range cfgFile.MCPServers {
		m.configs[name] = cfg
	}
	return nil
}

// LoadFromEnv loads MCP configs from environment variables.
// Format: TIANCAN_MCP_<NAME>_COMMAND=... TIANCAN_MCP_<NAME>_URL=...
func (m *MCPConfigManager) LoadFromEnv() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Scan environment for TIANCAN_MCP_ prefixes
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "TIANCAN_MCP_") {
			continue
		}
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := parts[1]

		// Parse: TIANCAN_MCP_<NAME>_<FIELD>
		trimmed := strings.TrimPrefix(key, "TIANCAN_MCP_")
		lastUnderscore := strings.LastIndex(trimmed, "_")
		if lastUnderscore < 0 {
			continue
		}
		name := strings.ToLower(trimmed[:lastUnderscore])
		field := strings.ToLower(trimmed[lastUnderscore+1:])

		cfg, ok := m.configs[name]
		if !ok {
			cfg = types.MCPConfig{}
		}

		switch field {
		case "command":
			cfg.Command = value
			cfg.Type = types.MCPTransportStdio
		case "url":
			cfg.URL = value
			if cfg.Type == "" {
				cfg.Type = types.MCPTransportHTTP
			}
		case "type":
			cfg.Type = types.MCPTransportType(value)
		case "args":
			cfg.Args = strings.Fields(value)
		}

		m.configs[name] = cfg
	}
}

// GetConfigs returns all loaded MCP configurations.
func (m *MCPConfigManager) GetConfigs() map[string]types.MCPConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(map[string]types.MCPConfig)
	for k, v := range m.configs {
		cp[k] = v
	}
	return cp
}

// GetConfig returns a specific server config.
func (m *MCPConfigManager) GetConfig(name string) (types.MCPConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.configs[name]
	return cfg, ok
}

// SetConfig adds or updates a server config.
func (m *MCPConfigManager) SetConfig(name string, cfg types.MCPConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[name] = cfg
}

// RemoveConfig removes a server config.
func (m *MCPConfigManager) RemoveConfig(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, name)
}

// --- Channel permissions (mirrors Claude Code's channel allowlist) ---

// SetPermission sets channel permissions for a server.
func (m *MCPConfigManager) SetPermission(perm *ChannelPermission) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permissions[perm.ServerName] = perm
}

// GetPermission returns permissions for a server.
func (m *MCPConfigManager) GetPermission(serverName string) *ChannelPermission {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.permissions[serverName]
}

// IsToolAllowed checks if a tool is allowed on a given server.
// Mirrors Claude Code's tool permission check for MCP tools.
func (m *MCPConfigManager) IsToolAllowed(serverName, toolName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	perm, ok := m.permissions[serverName]
	if !ok {
		return true // no restrictions = all allowed
	}

	// Check denied list first
	for _, denied := range perm.DeniedTools {
		if denied == toolName || denied == "*" {
			return false
		}
	}

	// If allowed list is specified, tool must be in it
	if len(perm.AllowedTools) > 0 {
		for _, allowed := range perm.AllowedTools {
			if allowed == toolName || allowed == "*" {
				return true
			}
		}
		return false
	}

	return true
}

// IsReadOnly checks if a server is read-only.
func (m *MCPConfigManager) IsReadOnly(serverName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if perm, ok := m.permissions[serverName]; ok {
		return perm.ReadOnly
	}
	return false
}

// GetAllPermissions returns all channel permissions.
func (m *MCPConfigManager) GetAllPermissions() map[string]*ChannelPermission {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(map[string]*ChannelPermission)
	for k, v := range m.permissions {
		cp[k] = v
	}
	return cp
}

// --- OAuth support (mirrors Claude Code's MCP OAuth) ---

// SetOAuthConfig sets OAuth configuration for a server.
func (m *MCPConfigManager) SetOAuthConfig(serverName string, cfg *OAuthConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oauthConfigs[serverName] = cfg
}

// GetOAuthConfig returns OAuth configuration for a server.
func (m *MCPConfigManager) GetOAuthConfig(serverName string) *OAuthConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.oauthConfigs[serverName]
}

// IsTokenExpired checks if the OAuth token for a server has expired.
func (m *MCPConfigManager) IsTokenExpired(serverName string) bool {
	m.mu.RLock()
	cfg := m.oauthConfigs[serverName]
	m.mu.RUnlock()
	if cfg == nil || !cfg.Enabled {
		return false
	}
	if cfg.Expiry == 0 {
		return cfg.AccessToken == ""
	}
	return cfg.Expiry < timeNowFunc().Unix()
}

// UpdateTokens updates OAuth tokens for a server.
func (m *MCPConfigManager) UpdateTokens(serverName, accessToken, refreshToken string, expiry int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg, ok := m.oauthConfigs[serverName]; ok {
		cfg.AccessToken = accessToken
		cfg.RefreshToken = refreshToken
		cfg.Expiry = expiry
	}
}

// InjectAuthHeaders adds OAuth Authorization headers to MCP requests.
func (m *MCPConfigManager) InjectAuthHeaders(serverName string, headers map[string]string) {
	m.mu.RLock()
	cfg := m.oauthConfigs[serverName]
	m.mu.RUnlock()

	if cfg != nil && cfg.Enabled && cfg.AccessToken != "" {
		headers["Authorization"] = fmt.Sprintf("Bearer %s", cfg.AccessToken)
	}
}

// timeNowFunc is overridable for testing
var timeNowFunc = time.Now

// GetConfigPaths returns the loaded config file paths.
func (m *MCPConfigManager) GetConfigPaths() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]string, len(m.configPaths))
	copy(cp, m.configPaths)
	return cp
}
