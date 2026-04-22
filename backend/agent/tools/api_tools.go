package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── API Testing Environment (shared state) ─────────────────────

type apiEnv struct {
	mu        sync.RWMutex
	variables map[string]string
	history   []APIRequestResult
}

var apiTestEnv = &apiEnv{variables: make(map[string]string)}

// ── APIRequestTool ─────────────────────────────────────────────

type APIRequestTool struct{}

func (t *APIRequestTool) Name() string            { return "api_request" }
func (t *APIRequestTool) IsReadOnly() bool        { return true }
func (t *APIRequestTool) IsConcurrencySafe() bool { return true }
func (t *APIRequestTool) Description() string {
	return "Send HTTP request for API testing. Args: method, url, headers (object), body (string), timeout (seconds), extractVars (object: jsonpath→varName)"
}
func (t *APIRequestTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"method":      map[string]interface{}{"type": "string", "description": "GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS"},
			"url":         map[string]interface{}{"type": "string", "description": "Request URL (supports {{var}} substitution)"},
			"headers":     map[string]interface{}{"type": "object", "description": "Request headers"},
			"body":        map[string]interface{}{"type": "string", "description": "Request body (JSON string or plain text)"},
			"timeout":     map[string]interface{}{"type": "integer", "description": "Timeout seconds (default 30)"},
			"extractVars": map[string]interface{}{"type": "object", "description": "Extract values from response: {jsonpath: varName}"},
		},
		"required": []string{"method", "url"},
	}
}
func (t *APIRequestTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	method, _ := args["method"].(string)
	rawURL, _ := args["url"].(string)
	timeoutSec := toInt(args["timeout"], 30)

	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)
	rawURL = substituteVars(rawURL)

	// Build request body
	var bodyReader io.Reader
	if bodyStr, ok := args["body"].(string); ok && bodyStr != "" {
		bodyStr = substituteVars(bodyStr)
		bodyReader = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Invalid request: %v", err), Success: false}, nil
	}

	// Apply headers
	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			if s, ok := v.(string); ok {
				req.Header.Set(k, substituteVars(s))
			}
		}
	}
	// Auto content-type for JSON body
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		if bodyStr, _ := args["body"].(string); strings.TrimSpace(bodyStr)[:1] == "{" || strings.TrimSpace(bodyStr)[:1] == "[" {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Request failed: %v", err), Success: false}, nil
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if len(respBody) > 50000 {
		respBody = respBody[:50000]
	}

	// Build result
	result := APIRequestResult{
		Method:     method,
		URL:        rawURL,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		Headers:    resp.Header,
		Body:       string(respBody),
		Duration:   elapsed.Milliseconds(),
	}

	// Extract variables
	if extractVars, ok := args["extractVars"].(map[string]interface{}); ok {
		apiTestEnv.mu.Lock()
		for jsonPath, varName := range extractVars {
			vn, _ := varName.(string)
			if vn == "" {
				continue
			}
			val := extractJSONPath(string(respBody), jsonPath)
			if val != "" {
				apiTestEnv.variables[vn] = val
			}
		}
		apiTestEnv.mu.Unlock()
	}

	// Save to history
	apiTestEnv.mu.Lock()
	result.Index = len(apiTestEnv.history) + 1
	apiTestEnv.history = append(apiTestEnv.history, result)
	apiTestEnv.mu.Unlock()

	// Format output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HTTP %d %s (%dms)\n", result.Status, result.StatusText, result.Duration))
	sb.WriteString(fmt.Sprintf("URL: %s %s\n", method, rawURL))
	// Response headers (compact)
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		sb.WriteString(fmt.Sprintf("Content-Type: %s\n", ct))
	}
	cl := resp.Header.Get("Content-Length")
	if cl != "" {
		sb.WriteString(fmt.Sprintf("Content-Length: %s\n", cl))
	}
	sb.WriteString("\n")
	// Body (truncated)
	bodyStr := string(respBody)
	if len(bodyStr) > 3000 {
		bodyStr = bodyStr[:3000] + "\n... (truncated)"
	}
	sb.WriteString(bodyStr)

	return types.ToolResult{Content: sb.String(), Success: resp.StatusCode < 400}, nil
}

// APIRequestResult holds the result of an API request.
type APIRequestResult struct {
	Index      int               `json:"index"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"`
	Headers    http.Header       `json:"headers,omitempty"`
	Body       string            `json:"body"`
	Duration   int64             `json:"durationMs"`
	Assertions []AssertionResult `json:"assertions,omitempty"`
}

// AssertionResult holds the result of a single assertion.
type AssertionResult struct {
	Type     string `json:"type"`   // "status", "header", "body", "jsonpath", "duration"
	Target   string `json:"target"` // what was asserted
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Passed   bool   `json:"passed"`
}

// ── APIAssertTool ──────────────────────────────────────────────

type APIAssertTool struct{}

func (t *APIAssertTool) Name() string            { return "api_assert" }
func (t *APIAssertTool) IsReadOnly() bool        { return true }
func (t *APIAssertTool) IsConcurrencySafe() bool { return true }
func (t *APIAssertTool) Description() string {
	return "Assert conditions on the last API response. Args: assertions (array of {type, target, expected, operator})"
}
func (t *APIAssertTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"assertions": map[string]interface{}{
				"type":        "array",
				"description": "Array of assertions: {type: status/header/body/jsonpath/duration, target, expected, operator: eq/neq/contains/gt/lt/regex}",
			},
		},
		"required": []string{"assertions"},
	}
}
func (t *APIAssertTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	assertionsRaw, ok := args["assertions"].([]interface{})
	if !ok || len(assertionsRaw) == 0 {
		return types.ToolResult{Content: "No assertions provided", Success: false}, nil
	}

	apiTestEnv.mu.RLock()
	if len(apiTestEnv.history) == 0 {
		apiTestEnv.mu.RUnlock()
		return types.ToolResult{Content: "No API request history. Run api_request first.", Success: false}, nil
	}
	lastResp := apiTestEnv.history[len(apiTestEnv.history)-1]
	apiTestEnv.mu.RUnlock()

	var results []AssertionResult
	allPassed := true

	for _, aRaw := range assertionsRaw {
		a, ok := aRaw.(map[string]interface{})
		if !ok {
			continue
		}

		aType, _ := a["type"].(string)
		target, _ := a["target"].(string)
		expected, _ := a["expected"].(string)
		operator, _ := a["operator"].(string)
		if operator == "" {
			operator = "eq"
		}

		var actual string
		switch aType {
		case "status":
			actual = fmt.Sprintf("%d", lastResp.Status)
		case "header":
			actual = lastResp.Headers.Get(target)
		case "body":
			actual = lastResp.Body
		case "jsonpath":
			actual = extractJSONPath(lastResp.Body, target)
		case "duration":
			actual = fmt.Sprintf("%d", lastResp.Duration)
		default:
			actual = fmt.Sprintf("unknown type: %s", aType)
		}

		passed := evaluateAssertion(actual, expected, operator)
		if !passed {
			allPassed = false
		}

		results = append(results, AssertionResult{
			Type: aType, Target: target, Expected: expected,
			Actual: actual, Passed: passed,
		})
	}

	// Update last history entry
	apiTestEnv.mu.Lock()
	if len(apiTestEnv.history) > 0 {
		apiTestEnv.history[len(apiTestEnv.history)-1].Assertions = results
	}
	apiTestEnv.mu.Unlock()

	d, _ := json.MarshalIndent(results, "", "  ")
	status := "PASS"
	if !allPassed {
		status = "FAIL"
	}
	return types.ToolResult{Content: fmt.Sprintf("Assertions: %s\n%s", status, string(d)), Success: allPassed}, nil
}

// ── APIRunCollectionTool ───────────────────────────────────────

type APIRunCollectionTool struct{}

func (t *APIRunCollectionTool) Name() string            { return "api_run_collection" }
func (t *APIRunCollectionTool) IsReadOnly() bool        { return true }
func (t *APIRunCollectionTool) IsConcurrencySafe() bool { return false }
func (t *APIRunCollectionTool) Description() string {
	return "Run a sequence of API requests as a collection. Args: requests (array of {method, url, headers, body, extractVars, assertions})"
}
func (t *APIRunCollectionTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"requests": map[string]interface{}{
				"type":        "array",
				"description": "Array of request objects: {method, url, headers, body, extractVars, assertions}",
			},
			"stopOnFail": map[string]interface{}{"type": "boolean", "description": "Stop on first failure (default true)"},
		},
		"required": []string{"requests"},
	}
}
func (t *APIRunCollectionTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	requests, ok := args["requests"].([]interface{})
	if !ok || len(requests) == 0 {
		return types.ToolResult{Content: "No requests provided", Success: false}, nil
	}
	stopOnFail := true
	if v, ok := args["stopOnFail"].(bool); ok {
		stopOnFail = v
	}

	var sb strings.Builder
	totalPassed := 0
	totalFailed := 0

	for i, reqRaw := range requests {
		reqArgs, ok := reqRaw.(map[string]interface{})
		if !ok {
			continue
		}

		sb.WriteString(fmt.Sprintf("\n── Request %d/%d ──\n", i+1, len(requests)))

		// Execute request
		reqResult, _ := (&APIRequestTool{}).Execute(ctx, reqArgs)
		sb.WriteString(reqResult.Content)
		sb.WriteString("\n")

		if !reqResult.Success {
			totalFailed++
			if stopOnFail {
				sb.WriteString("Collection stopped on failure\n")
				break
			}
		} else {
			totalPassed++
		}

		// Run assertions if provided
		if assertions, ok := reqArgs["assertions"].([]interface{}); ok && len(assertions) > 0 {
			assertResult, _ := (&APIAssertTool{}).Execute(ctx, map[string]interface{}{"assertions": assertions})
			sb.WriteString(assertResult.Content)
			sb.WriteString("\n")
			if !assertResult.Success {
				totalFailed++
				if stopOnFail {
					sb.WriteString("Collection stopped on assertion failure\n")
					break
				}
			}
		}
	}

	sb.WriteString(fmt.Sprintf("\n── Summary: %d passed, %d failed ──\n", totalPassed, totalFailed))
	return types.ToolResult{Content: sb.String(), Success: totalFailed == 0}, nil
}

// ── APIGetVarsTool ─────────────────────────────────────────────

type APIGetVarsTool struct{}

func (t *APIGetVarsTool) Name() string            { return "api_get_vars" }
func (t *APIGetVarsTool) IsReadOnly() bool        { return true }
func (t *APIGetVarsTool) IsConcurrencySafe() bool { return true }
func (t *APIGetVarsTool) Description() string     { return "Get current API test environment variables" }
func (t *APIGetVarsTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t *APIGetVarsTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	apiTestEnv.mu.RLock()
	defer apiTestEnv.mu.RUnlock()
	if len(apiTestEnv.variables) == 0 {
		return types.ToolResult{Content: "No variables set", Success: true}, nil
	}
	d, _ := json.MarshalIndent(apiTestEnv.variables, "", "  ")
	return types.ToolResult{Content: string(d), Success: true}, nil
}

// ── APISetVarTool ──────────────────────────────────────────────

type APISetVarTool struct{}

func (t *APISetVarTool) Name() string            { return "api_set_var" }
func (t *APISetVarTool) IsReadOnly() bool        { return false }
func (t *APISetVarTool) IsConcurrencySafe() bool { return false }
func (t *APISetVarTool) Description() string {
	return "Set API test environment variable. Args: name, value"
}
func (t *APISetVarTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"value": map[string]interface{}{"type": "string"},
		},
		"required": []string{"name", "value"},
	}
}
func (t *APISetVarTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	name, _ := args["name"].(string)
	value, _ := args["value"].(string)
	if name == "" {
		return types.ToolResult{Content: "name is required", Success: false}, nil
	}
	apiTestEnv.mu.Lock()
	apiTestEnv.variables[name] = value
	apiTestEnv.mu.Unlock()
	return types.ToolResult{Content: fmt.Sprintf("Set %s = %s", name, value), Success: true}, nil
}

// ── Helper functions ───────────────────────────────────────────

func substituteVars(s string) string {
	apiTestEnv.mu.RLock()
	defer apiTestEnv.mu.RUnlock()
	for k, v := range apiTestEnv.variables {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func extractJSONPath(body, path string) string {
	if body == "" || path == "" {
		return ""
	}
	// Simple dot-notation extractor: $.data.token → navigate JSON
	parts := strings.Split(strings.TrimPrefix(path, "$."), ".")
	var current interface{}
	if err := json.Unmarshal([]byte(body), &current); err != nil {
		return ""
	}
	for _, p := range parts {
		switch c := current.(type) {
		case map[string]interface{}:
			if v, ok := c[p]; ok {
				current = v
			} else {
				return ""
			}
		case []interface{}:
			idx := 0
			if _, err := fmt.Sscanf(p, "%d", &idx); err == nil && idx < len(c) {
				current = c[idx]
			} else {
				return ""
			}
		default:
			return ""
		}
	}
	switch v := current.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	case bool:
		return fmt.Sprintf("%v", v)
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func evaluateAssertion(actual, expected, operator string) bool {
	switch operator {
	case "eq", "==", "equals":
		return actual == expected
	case "neq", "!=":
		return actual != expected
	case "contains":
		return strings.Contains(actual, expected)
	case "not_contains":
		return !strings.Contains(actual, expected)
	case "gt", ">":
		return actual > expected
	case "lt", "<":
		return actual < expected
	case "regex", "=~":
		return strings.Contains(actual, expected) // simplified
	case "exists":
		return actual != ""
	default:
		return actual == expected
	}
}
