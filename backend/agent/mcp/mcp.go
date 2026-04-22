package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// Client manages connections to MCP servers.
type Client struct {
	servers map[string]*ServerConn
	mu      sync.RWMutex
	tools   map[string]types.MCPToolInfo
}

// ServerConn represents a connection to a single MCP server.
type ServerConn struct {
	Name      string
	Config    types.MCPConfig
	State     types.MCPServerConnType
	transport types.MCPTransportType
	// stdio
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	// http / sse
	httpClient  *http.Client
	baseURL     string
	msgEndpoint string // SSE: separate POST endpoint, HTTP: same as baseURL
	sseCancel   context.CancelFunc
	// common
	tools   []types.MCPToolInfo
	pending map[int]chan<- json.RawMessage
	mu      sync.Mutex
	nextID  int
}

// NewClient creates a new MCP client.
func NewClient() *Client {
	return &Client{
		servers: make(map[string]*ServerConn),
		tools:   make(map[string]types.MCPToolInfo),
	}
}

// ConnectToServer establishes a connection to an MCP server.
func (c *Client) ConnectToServer(name string, cfg types.MCPConfig) (*types.MCPServerState, error) {
	switch cfg.Type {
	case types.MCPTransportStdio:
		return c.connectStdio(name, cfg)
	case types.MCPTransportHTTP:
		return c.connectHTTP(name, cfg)
	case types.MCPTransportSSE:
		return c.connectSSE(name, cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", cfg.Type)
	}
}

// connectHTTP connects to an MCP server over HTTP (JSON-RPC POST).
func (c *Client) connectHTTP(name string, cfg types.MCPConfig) (*types.MCPServerState, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("MCP HTTP transport requires a URL")
	}
	conn := &ServerConn{
		Name:        name,
		Config:      cfg,
		State:       types.MCPConnPending,
		transport:   types.MCPTransportHTTP,
		httpClient:  &http.Client{Timeout: 60 * time.Second},
		baseURL:     cfg.URL,
		msgEndpoint: cfg.URL,
		pending:     make(map[int]chan<- json.RawMessage),
		nextID:      1,
	}

	_, err := conn.sendRequest("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "tiancan-ai-ide", "version": "1.0.0"},
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP MCP initialize: %w", err)
	}
	conn.sendNotification("notifications/initialized", nil)
	conn.State = types.MCPConnConnected

	toolsResult, err := conn.sendRequest("tools/list", nil)
	if err == nil {
		conn.tools = parseToolsList(name, toolsResult)
	}

	c.mu.Lock()
	c.servers[name] = conn
	for _, t := range conn.tools {
		c.tools[t.Name] = t
	}
	c.mu.Unlock()

	return &types.MCPServerState{Name: name, Type: conn.State, Config: cfg, Tools: conn.tools}, nil
}

// connectSSE connects to an MCP server over SSE + HTTP POST.
func (c *Client) connectSSE(name string, cfg types.MCPConfig) (*types.MCPServerState, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("MCP SSE transport requires a URL")
	}

	httpClient := &http.Client{Timeout: 0} // no timeout for SSE stream
	sseCtx, sseCancel := context.WithCancel(context.Background())

	// Determine the POST endpoint for sending messages.
	// Convention: POST endpoint is cfg.URL with /message appended (if not already), or cfg.URL directly.
	msgEndpoint := cfg.URL
	if !strings.HasSuffix(msgEndpoint, "/message") {
		msgEndpoint = strings.TrimRight(cfg.URL, "/") + "/message"
	}

	conn := &ServerConn{
		Name:        name,
		Config:      cfg,
		State:       types.MCPConnPending,
		transport:   types.MCPTransportSSE,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		baseURL:     cfg.URL,
		msgEndpoint: msgEndpoint,
		sseCancel:   sseCancel,
		pending:     make(map[int]chan<- json.RawMessage),
		nextID:      1,
	}

	// Start SSE reader goroutine
	sseReq, err := http.NewRequestWithContext(sseCtx, "GET", cfg.URL, nil)
	if err != nil {
		sseCancel()
		return nil, fmt.Errorf("SSE request: %w", err)
	}
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp, err := httpClient.Do(sseReq)
	if err != nil {
		sseCancel()
		return nil, fmt.Errorf("SSE connect: %w", err)
	}
	if sseResp.StatusCode != http.StatusOK {
		sseResp.Body.Close()
		sseCancel()
		return nil, fmt.Errorf("SSE connect status %d", sseResp.StatusCode)
	}
	go conn.sseReadLoop(sseResp.Body)

	_, err = conn.sendRequest("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "tiancan-ai-ide", "version": "1.0.0"},
	})
	if err != nil {
		sseCancel()
		return nil, fmt.Errorf("SSE MCP initialize: %w", err)
	}
	conn.sendNotification("notifications/initialized", nil)
	conn.State = types.MCPConnConnected

	toolsResult, err := conn.sendRequest("tools/list", nil)
	if err == nil {
		conn.tools = parseToolsList(name, toolsResult)
	}

	c.mu.Lock()
	c.servers[name] = conn
	for _, t := range conn.tools {
		c.tools[t.Name] = t
	}
	c.mu.Unlock()

	return &types.MCPServerState{Name: name, Type: conn.State, Config: cfg, Tools: conn.tools}, nil
}

// sseReadLoop reads SSE events and dispatches JSON-RPC responses to pending channels.
func (c *ServerConn) sseReadLoop(body io.ReadCloser) {
	defer body.Close()
	scanner := bufio.NewScanner(body)
	var dataBuf strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			dataBuf.WriteString(strings.TrimPrefix(line, "data:"))
		} else if line == "" && dataBuf.Len() > 0 {
			data := strings.TrimSpace(dataBuf.String())
			dataBuf.Reset()
			if data == "" || data == "[DONE]" {
				continue
			}
			var resp jsonRPCResponse
			if err := json.Unmarshal([]byte(data), &resp); err != nil {
				continue
			}
			if resp.ID > 0 {
				c.mu.Lock()
				ch, ok := c.pending[resp.ID]
				if ok {
					delete(c.pending, resp.ID)
				}
				c.mu.Unlock()
				if ok {
					if resp.Error != nil {
						ch <- nil
					} else {
						ch <- resp.Result
					}
				}
			}
		}
	}
}

// connectStdio connects to a stdio-based MCP server.
func (c *Client) connectStdio(name string, cfg types.MCPConfig) (*types.MCPServerState, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}

	conn := &ServerConn{
		Name:    name,
		Config:  cfg,
		State:   types.MCPConnPending,
		cmd:     cmd,
		stdin:   stdinPipe,
		stdout:  bufio.NewReader(stdoutPipe),
		pending: make(map[int]chan<- json.RawMessage),
		nextID:  1,
	}

	// Start reading responses
	go conn.readLoop()

	// Send initialize request
	_, err = conn.sendRequest("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "tiancan-ai-ide",
			"version": "1.0.0",
		},
	})
	if err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	// Send initialized notification
	conn.sendNotification("notifications/initialized", nil)

	conn.State = types.MCPConnConnected

	// Fetch tools
	toolsResult, err := conn.sendRequest("tools/list", nil)
	if err != nil {
		// Non-fatal: server might not support tools
		conn.State = types.MCPConnConnected
	} else {
		conn.tools = parseToolsList(name, toolsResult)
	}

	c.mu.Lock()
	c.servers[name] = conn
	for _, t := range conn.tools {
		c.tools[t.Name] = t
	}
	c.mu.Unlock()

	state := &types.MCPServerState{
		Name:   name,
		Type:   conn.State,
		Config: cfg,
		Tools:  conn.tools,
	}
	return state, nil
}

// DisconnectServer closes a connection to an MCP server.
func (c *Client) DisconnectServer(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, ok := c.servers[name]
	if !ok {
		return fmt.Errorf("server not found: %s", name)
	}

	// Remove tools
	for _, t := range conn.tools {
		delete(c.tools, t.Name)
	}

	// Kill process (stdio)
	if conn.cmd != nil && conn.cmd.Process != nil {
		conn.cmd.Process.Kill()
	}
	// Cancel SSE context
	if conn.sseCancel != nil {
		conn.sseCancel()
	}

	delete(c.servers, name)
	return nil
}

// CallTool invokes a tool on an MCP server.
func (c *Client) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (types.ToolResult, error) {
	c.mu.RLock()
	toolInfo, ok := c.tools[toolName]
	c.mu.RUnlock()
	if !ok {
		return types.ToolResult{}, fmt.Errorf("MCP tool not found: %s", toolName)
	}

	c.mu.RLock()
	conn, ok := c.servers[toolInfo.ServerName]
	c.mu.RUnlock()
	if !ok {
		return types.ToolResult{}, fmt.Errorf("MCP server not connected: %s", toolInfo.ServerName)
	}

	result, err := conn.sendRequest("tools/call", map[string]interface{}{
		"name":      toolInfo.Name,
		"arguments": args,
	})
	if err != nil {
		return types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}, nil
	}

	// Parse result content
	content := parseToolResultContent(result)
	return types.ToolResult{Content: content, Success: true}, nil
}

// GetServers returns all server states.
func (c *Client) GetServers() []types.MCPServerState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var states []types.MCPServerState
	for _, conn := range c.servers {
		states = append(states, types.MCPServerState{
			Name:   conn.Name,
			Type:   conn.State,
			Config: conn.Config,
			Tools:  conn.tools,
		})
	}
	return states
}

// GetTools returns all MCP tools.
func (c *Client) GetTools() []types.MCPToolInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var tools []types.MCPToolInfo
	for _, t := range c.tools {
		tools = append(tools, t)
	}
	return tools
}

// --- JSON-RPC helpers ---

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

func (c *ServerConn) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	if c.transport == types.MCPTransportHTTP || c.transport == types.MCPTransportSSE {
		return c.sendHTTPRequest(method, params)
	}
	return c.sendStdioRequest(method, params)
}

func (c *ServerConn) sendStdioRequest(method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	data = append(data, '\n')

	if _, err := c.stdin.Write(data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case result := <-ch:
		return result, nil
	case <-time.After(30 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("request timeout: %s", method)
	}
}

// sendHTTPRequest sends a JSON-RPC request over HTTP POST.
// For SSE transport, the response arrives via the SSE stream (via pending channel).
// For HTTP transport, the response is read directly from the POST response body.
func (c *ServerConn) sendHTTPRequest(method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	var ch chan json.RawMessage
	if c.transport == types.MCPTransportSSE {
		ch = make(chan json.RawMessage, 1)
		c.pending[id] = ch
	}
	c.mu.Unlock()

	req := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		if ch != nil {
			c.mu.Lock()
			delete(c.pending, id)
			c.mu.Unlock()
		}
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.msgEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ch != nil {
			c.mu.Lock()
			delete(c.pending, id)
			c.mu.Unlock()
		}
		return nil, err
	}
	defer resp.Body.Close()

	if c.transport == types.MCPTransportSSE {
		// Response arrives via SSE stream
		select {
		case result := <-ch:
			return result, nil
		case <-time.After(30 * time.Second):
			c.mu.Lock()
			delete(c.pending, id)
			c.mu.Unlock()
			return nil, fmt.Errorf("SSE response timeout: %s", method)
		}
	}

	// HTTP: read response body directly
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("invalid JSON-RPC response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func (c *ServerConn) sendNotification(method string, params interface{}) error {
	notif := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(notif)
	if err != nil {
		return err
	}
	if c.transport == types.MCPTransportHTTP || c.transport == types.MCPTransportSSE {
		httpReq, err := http.NewRequest("POST", c.msgEndpoint, bytes.NewReader(data))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func (c *ServerConn) readLoop() {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		if resp.ID > 0 {
			c.mu.Lock()
			ch, ok := c.pending[resp.ID]
			if ok {
				delete(c.pending, resp.ID)
			}
			c.mu.Unlock()
			if ok && ch != nil {
				if resp.Error != nil {
					ch <- nil
				} else {
					ch <- resp.Result
				}
			}
		}
	}
}

func parseToolsList(serverName string, raw json.RawMessage) []types.MCPToolInfo {
	if raw == nil {
		return nil
	}
	var result struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
			Annotations struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}

	var tools []types.MCPToolInfo
	for _, t := range result.Tools {
		fqn := fmt.Sprintf("mcp__%s__%s", serverName, t.Name)
		tools = append(tools, types.MCPToolInfo{
			Name:        fqn,
			Description: t.Description,
			ServerName:  serverName,
			InputSchema: t.InputSchema,
			ReadOnly:    t.Annotations.ReadOnlyHint,
		})
	}
	return tools
}

func parseToolResultContent(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return string(raw)
	}
	var texts []string
	for _, c := range result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n")
}
