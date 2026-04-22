package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Resource Manager ─────────────────────────────────────────────

// ResourceManager handles MCP resource reading and caching.
type ResourceManager struct {
	mu     sync.RWMutex
	client *Client
	cache  map[string]*ResourceCacheEntry
	ttl    time.Duration
}

// ResourceCacheEntry caches a resource read result.
type ResourceCacheEntry struct {
	Content   string    `json:"content"`
	MimeType  string    `json:"mimeType"`
	CachedAt  time.Time `json:"cachedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// NewResourceManager creates a resource manager.
func NewResourceManager(client *Client, cacheTTL time.Duration) *ResourceManager {
	if cacheTTL <= 0 {
		cacheTTL = 5 * time.Minute
	}
	return &ResourceManager{
		client: client,
		cache:  make(map[string]*ResourceCacheEntry),
		ttl:    cacheTTL,
	}
}

// ReadResource reads a resource from an MCP server, using cache if available.
func (rm *ResourceManager) ReadResource(ctx context.Context, uri string) (string, string, error) {
	rm.mu.RLock()
	if entry, ok := rm.cache[uri]; ok && time.Now().Before(entry.ExpiresAt) {
		rm.mu.RUnlock()
		return entry.Content, entry.MimeType, nil
	}
	rm.mu.RUnlock()

	// Find which server owns this URI
	rm.client.mu.RLock()
	var targetConn *ServerConn
	for _, conn := range rm.client.servers {
		if conn.State == types.MCPConnConnected {
			targetConn = conn
			break
		}
	}
	rm.client.mu.RUnlock()

	if targetConn == nil {
		return "", "", fmt.Errorf("no connected MCP server for resource: %s", uri)
	}

	result, err := targetConn.sendRequest("resources/read", map[string]interface{}{
		"uri": uri,
	})
	if err != nil {
		return "", "", fmt.Errorf("read resource: %w", err)
	}

	content, mimeType := parseResourceContent(result)

	// Cache the result
	rm.mu.Lock()
	rm.cache[uri] = &ResourceCacheEntry{
		Content:   content,
		MimeType:  mimeType,
		CachedAt:  time.Now(),
		ExpiresAt: time.Now().Add(rm.ttl),
	}
	rm.mu.Unlock()

	return content, mimeType, nil
}

// ListResources lists available resources from all connected servers.
func (rm *ResourceManager) ListResources(ctx context.Context) []types.MCPResourceInfo {
	rm.client.mu.RLock()
	defer rm.client.mu.RUnlock()

	var resources []types.MCPResourceInfo
	for _, conn := range rm.client.servers {
		if conn.State != types.MCPConnConnected {
			continue
		}
		result, err := conn.sendRequest("resources/list", nil)
		if err != nil {
			continue
		}
		resources = append(resources, parseResourceList(conn.Name, result)...)
	}
	return resources
}

// InvalidateCache clears the resource cache.
func (rm *ResourceManager) InvalidateCache() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.cache = make(map[string]*ResourceCacheEntry)
}

// ── Prompt Template Manager ─────────────────────────────────────

// PromptManager handles MCP prompt templates.
type PromptManager struct {
	mu     sync.RWMutex
	client *Client
	cache  map[string][]types.MCPPromptInfo
}

// NewPromptManager creates a prompt template manager.
func NewPromptManager(client *Client) *PromptManager {
	return &PromptManager{
		client: client,
		cache:  make(map[string][]types.MCPPromptInfo),
	}
}

// ListPrompts discovers available prompt templates from all servers.
func (pm *PromptManager) ListPrompts(ctx context.Context) []types.MCPPromptInfo {
	pm.client.mu.RLock()
	defer pm.client.mu.RUnlock()

	var allPrompts []types.MCPPromptInfo
	for _, conn := range pm.client.servers {
		if conn.State != types.MCPConnConnected {
			continue
		}
		result, err := conn.sendRequest("prompts/list", nil)
		if err != nil {
			continue
		}
		prompts := parsePromptList(conn.Name, result)
		allPrompts = append(allPrompts, prompts...)
		pm.mu.Lock()
		pm.cache[conn.Name] = prompts
		pm.mu.Unlock()
	}
	return allPrompts
}

// GetPrompt renders a prompt template with the given arguments.
func (pm *PromptManager) GetPrompt(ctx context.Context, name string, args map[string]string) ([]types.Message, error) {
	pm.client.mu.RLock()
	var targetConn *ServerConn
	for _, conn := range pm.client.servers {
		if conn.State != types.MCPConnConnected {
			continue
		}
		pm.mu.RLock()
		prompts := pm.cache[conn.Name]
		pm.mu.RUnlock()
		for _, p := range prompts {
			if p.Name == name {
				targetConn = conn
				break
			}
		}
		if targetConn != nil {
			break
		}
	}
	pm.client.mu.RUnlock()

	if targetConn == nil {
		return nil, fmt.Errorf("prompt not found: %s", name)
	}

	result, err := targetConn.sendRequest("prompts/get", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("get prompt: %w", err)
	}

	return parsePromptResult(result), nil
}

// ── Tool Discovery ──────────────────────────────────────────────

// ToolDiscovery provides dynamic tool discovery from MCP servers.
type ToolDiscovery struct {
	mu     sync.RWMutex
	client *Client
	tools  map[string]types.MCPToolInfo
}

// NewToolDiscovery creates a tool discovery service.
func NewToolDiscovery(client *Client) *ToolDiscovery {
	return &ToolDiscovery{
		client: client,
		tools:  make(map[string]types.MCPToolInfo),
	}
}

// RefreshTools re-discovers tools from all connected servers.
func (td *ToolDiscovery) RefreshTools(ctx context.Context) error {
	td.client.mu.RLock()
	servers := make(map[string]*ServerConn)
	for k, v := range td.client.servers {
		servers[k] = v
	}
	td.client.mu.RUnlock()

	for name, conn := range servers {
		if conn.State != types.MCPConnConnected {
			continue
		}
		result, err := conn.sendRequest("tools/list", nil)
		if err != nil {
			continue
		}
		tools := parseToolsList(name, result)
		td.mu.Lock()
		for _, t := range tools {
			td.tools[t.Name] = t
		}
		td.mu.Unlock()

		// Also update client's tool map
		td.client.mu.Lock()
		for _, t := range tools {
			td.client.tools[t.Name] = t
		}
		conn.tools = tools
		td.client.mu.Unlock()
	}
	return nil
}

// GetToolDescriptions returns tool descriptions for system prompt injection.
func (td *ToolDiscovery) GetToolDescriptions() string {
	td.mu.RLock()
	defer td.mu.RUnlock()

	var sb strings.Builder
	for _, t := range td.tools {
		sb.WriteString(fmt.Sprintf("### %s\n%s\n", t.Name, t.Description))
		if t.InputSchema != nil {
			schema, _ := json.MarshalIndent(t.InputSchema, "", "  ")
			sb.WriteString(fmt.Sprintf("Input Schema:\n```json\n%s\n```\n\n", string(schema)))
		}
	}
	return sb.String()
}

// IsToolAvailable checks if a tool is available from any MCP server.
func (td *ToolDiscovery) IsToolAvailable(toolName string) bool {
	td.mu.RLock()
	defer td.mu.RUnlock()
	_, ok := td.tools[toolName]
	return ok
}

// ── Config Loader ───────────────────────────────────────────────

// LoadConfigFromFile loads MCP server configs from a JSON file.
func LoadConfigFromFile(path string) (map[string]types.MCPConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var configs map[string]types.MCPConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return configs, nil
}

// ConnectAll connects to all servers defined in a config map.
func (c *Client) ConnectAll(ctx context.Context, configs map[string]types.MCPConfig) []types.MCPServerState {
	var states []types.MCPServerState
	for name, cfg := range configs {
		state, err := c.ConnectToServer(name, cfg)
		if err != nil {
			states = append(states, types.MCPServerState{
				Name:  name,
				Type:  types.MCPConnFailed,
				Error: err.Error(),
			})
			continue
		}
		states = append(states, *state)
	}
	return states
}

// ── Parsing helpers ─────────────────────────────────────────────

func parseResourceContent(raw json.RawMessage) (string, string) {
	if raw == nil {
		return "", ""
	}
	var result struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text,omitempty"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return string(raw), "text/plain"
	}
	if len(result.Contents) == 0 {
		return "", ""
	}
	return result.Contents[0].Text, result.Contents[0].MimeType
}

func parseResourceList(serverName string, raw json.RawMessage) []types.MCPResourceInfo {
	if raw == nil {
		return nil
	}
	var result struct {
		Resources []struct {
			URI         string `json:"uri"`
			Name        string `json:"name"`
			Description string `json:"description"`
			MimeType    string `json:"mimeType"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	var resources []types.MCPResourceInfo
	for _, r := range result.Resources {
		resources = append(resources, types.MCPResourceInfo{
			URI:         r.URI,
			Name:        r.Name,
			Description: r.Description,
			MimeType:    r.MimeType,
			ServerName:  serverName,
		})
	}
	return resources
}

func parsePromptList(serverName string, raw json.RawMessage) []types.MCPPromptInfo {
	if raw == nil {
		return nil
	}
	var result struct {
		Prompts []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	var prompts []types.MCPPromptInfo
	for _, p := range result.Prompts {
		prompts = append(prompts, types.MCPPromptInfo{
			Name:        p.Name,
			Description: p.Description,
			ServerName:  serverName,
		})
	}
	return prompts
}

func parsePromptResult(raw json.RawMessage) []types.Message {
	if raw == nil {
		return nil
	}
	var result struct {
		Messages []struct {
			Role    string `json:"role"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	var messages []types.Message
	for _, m := range result.Messages {
		messages = append(messages, types.Message{
			Role:      types.MessageRole(m.Role),
			Content:   m.Content.Text,
			Timestamp: time.Now(),
		})
	}
	return messages
}
