package tools

import (
	"testing"
)

func TestParseMillis(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{"Simple", "123ms", 123},
		{"With spaces", "  456ms  ", 456},
		{"Zero", "0ms", 0},
		{"Float", "12.5ms", 12.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMillis(tt.input)
			if result != tt.expected {
				t.Errorf("parseMillis(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractCodeFromResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"Plain code",
			"func main() { println(\"hello\") }",
			"func main() { println(\"hello\") }",
		},
		{
			"Markdown code block",
			"Here is the code:\n```go\nfunc main() {}\n```\nDone",
			"func main() {}",
		},
		{
			"No code block",
			"Just some text",
			"Just some text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractCodeFromResponse(tt.input)
			if result != tt.expected {
				t.Errorf("extractCodeFromResponse(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateHeuristicHypothesis(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		metric string
	}{
		{"Nested loops", "for i := 0; i < n; i++ { for j := 0; j < n; j++ {}}", "execution_time"},
		{"Append in loop", "for i := 0; i < n; i++ { arr = append(arr, i)}", "execution_time"},
		{"Memory allocation", "data := make([]byte, 1000000)", "memory_usage"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateHeuristicHypothesis(tt.code, tt.metric)
			if result == "" {
				t.Error("generateHeuristicHypothesis returned empty string")
			}
			t.Logf("Hypothesis for %s (%s): %s", tt.name, tt.metric, result)
		})
	}
}

func TestApplyHeuristicModification(t *testing.T) {
	code := "func original() {}"
	hypothesis := "nested loops may cause O(n²) complexity"
	metric := "execution_time"

	modified, desc, err := applyHeuristicModification(code, hypothesis, metric)
	if err != nil {
		t.Errorf("applyHeuristicModification returned error: %v", err)
	}
	if modified != code {
		t.Errorf("Heuristic modification should not change code for structural changes")
	}
	if desc == "" {
		t.Error("Description should not be empty")
	}

	t.Logf("Heuristic modification result: %s", desc)
}

func TestWebSearchToolSchema(t *testing.T) {
	tool := &WebSearchTool{}
	schema := tool.InputSchema()

	// Verify schema structure
	if schema["type"] != "object" {
		t.Errorf("Expected type 'object', got %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties is not a map")
	}

	// Check required fields
	if _, ok := props["query"]; !ok {
		t.Error("query field missing")
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("required is not a string slice")
	}

	if !contains(required, "query") {
		t.Error("query should be required")
	}
}

func TestAutoResearchToolSchema(t *testing.T) {
	tool := &AutoResearchTool{}
	schema := tool.InputSchema()

	// Verify schema structure
	if schema["type"] != "object" {
		t.Errorf("Expected type 'object', got %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties is not a map")
	}

	// Check required fields
	requiredFields := []string{"target", "metric"}
	for _, field := range requiredFields {
		if _, ok := props[field]; !ok {
			t.Errorf("Required field %s missing", field)
		}
	}

	t.Logf("AutoResearchTool schema: %+v", schema)
}

func TestWebSearchToolExecute(t *testing.T) {
	tool := &WebSearchTool{}

	// Test empty query
	result, err := tool.Execute(nil, map[string]interface{}{"query": ""})
	if err != nil {
		t.Errorf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("Empty query should not succeed")
	}

	// Test actual search (may fail without network)
	result, err = tool.Execute(nil, map[string]interface{}{"query": "golang programming"})
	if err != nil {
		t.Logf("Search failed (expected without network): %v", err)
	} else {
		t.Logf("Search result: %s", result.Content)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
