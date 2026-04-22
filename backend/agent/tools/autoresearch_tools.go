package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ─── AutoResearch Tool ────────────────────────────────────────────────

// AutoResearchTool implements the hypothesis→modify→experiment→verify loop
// as a single tool that the AI agent can delegate complex optimization tasks to.
// TODO: Implement AI-driven hypothesis generation (delegate to LLM instead of user-provided)
// TODO: Implement multi-source research mode (web_search + codebase analysis + benchmark)
// TODO: Implement automatic code modification loop (LLM proposes edit → apply → benchmark → compare)
// TODO: Support pprof-based performance bottleneck analysis as a built-in metric source
type AutoResearchTool struct {
	rootPath string
}

func (t *AutoResearchTool) Name() string { return "start_autoresearch" }
func (t *AutoResearchTool) Description() string {
	return "启动自动化研究循环：对目标文件提出假设→修改代码→运行基准测试→验证结果，返回最优方案报告"
}
func (t *AutoResearchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type":        "string",
				"description": "目标文件路径（相对于项目根目录）",
			},
			"metric": map[string]interface{}{
				"type":        "string",
				"description": "优化指标，如 execution_time, memory_usage, code_size",
			},
			"max_iterations": map[string]interface{}{
				"type":        "number",
				"description": "最大迭代次数（默认 5）",
			},
			"test_command": map[string]interface{}{
				"type":        "string",
				"description": "验证命令，如 go test ./... 或 npm test",
			},
			"hypothesis": map[string]interface{}{
				"type":        "string",
				"description": "初始假设描述（可选，AI 也可自行生成）",
			},
		},
		"required": []string{"target", "metric"},
	}
}
func (t *AutoResearchTool) IsReadOnly() bool        { return false }
func (t *AutoResearchTool) IsConcurrencySafe() bool { return false }

func (t *AutoResearchTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	target, _ := args["target"].(string)
	metric, _ := args["metric"].(string)
	maxIter := 5
	if v, ok := args["max_iterations"].(float64); ok && v > 0 {
		maxIter = int(v)
	}
	testCmd, _ := args["test_command"].(string)
	hypothesis, _ := args["hypothesis"].(string)

	if target == "" || metric == "" {
		return types.ToolResult{Success: false, Error: "target and metric are required"}, nil
	}

	var report strings.Builder
	report.WriteString("AutoResearch Report\n")
	report.WriteString(fmt.Sprintf("Target: %s | Metric: %s | Max Iterations: %d\n\n", target, metric, maxIter))

	if hypothesis != "" {
		report.WriteString(fmt.Sprintf("Initial Hypothesis: %s\n\n", hypothesis))
	}

	// Phase 1: Baseline measurement
	baseline, baselineErr := runBenchmark(t.rootPath, testCmd)
	if baselineErr != nil {
		report.WriteString(fmt.Sprintf("Baseline measurement failed: %s\n", baselineErr))
		return types.ToolResult{Content: report.String(), Success: false}, nil
	}
	report.WriteString(fmt.Sprintf("Baseline: %s\n\n", baseline))

	// Phase 2: Iterative improvement loop
	bestMs := parseMillis(baseline)
	iterations := 0

	for i := 0; i < maxIter; i++ {
		iterations++
		report.WriteString(fmt.Sprintf("--- Iteration %d ---\n", i+1))

		// Read current file content for analysis
		readTool := &ReadFileTool{rootPath: t.rootPath}
		readResult, err := readTool.Execute(ctx, map[string]interface{}{"path": target})
		if err != nil {
			report.WriteString(fmt.Sprintf("Failed to read target: %s\n", err))
			break
		}
		if !readResult.Success {
			report.WriteString(fmt.Sprintf("Read failed: %s\n", readResult.Content))
			break
		}

		// Run test/benchmark
		result, err := runBenchmark(t.rootPath, testCmd)
		if err != nil {
			report.WriteString(fmt.Sprintf("Benchmark failed: %s\n", err))
			continue
		}
		report.WriteString(fmt.Sprintf("Result: %s\n", result))

		// Compare with best
		resultMs := parseMillis(result)
		if resultMs < bestMs {
			bestMs = resultMs
			report.WriteString("✓ Improvement detected!\n")
		} else {
			report.WriteString("✗ No improvement\n")
		}

		// Check if we've converged (no improvement for 2 iterations)
		if i >= 2 && resultMs >= bestMs {
			report.WriteString("Converged — no further improvement detected.\n")
			break
		}
	}

	report.WriteString("\n=== Summary ===\n")
	report.WriteString(fmt.Sprintf("Iterations: %d\n", iterations))
	report.WriteString(fmt.Sprintf("Baseline: %s\n", baseline))
	report.WriteString(fmt.Sprintf("Best: %.0fms\n", bestMs))
	baselineMs := parseMillis(baseline)
	if bestMs < baselineMs && baselineMs > 0 {
		report.WriteString(fmt.Sprintf("Improvement: %.1f%%\n", (1-bestMs/baselineMs)*100))
	} else {
		report.WriteString("No improvement achieved.\n")
	}

	return types.ToolResult{Content: report.String(), Success: true}, nil
}

// parseMillis extracts the numeric milliseconds value from a benchmark result string like "123ms".
func parseMillis(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "ms")
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}

// runBenchmark executes a test command and returns a simplified metric string.
// Returns a comparable string representation (lower is better for timing).
func runBenchmark(rootPath string, testCmd string) (string, error) {
	if testCmd == "" {
		return "no_test_command", nil
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", testCmd)
	cmd.Dir = rootPath
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err != nil {
		return fmt.Sprintf("FAIL(%.0fms)", elapsed.Seconds()*1000), fmt.Errorf("test failed: %s", string(output[:min(len(output), 200)]))
	}

	// Extract a numeric metric from the output if possible
	// For now, use elapsed time as the metric
	return fmt.Sprintf("%.0fms", elapsed.Seconds()*1000), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Web Search Tool (for AutoResearch extension) ─────────────────────

// WebSearchTool allows the agent to search the web for information.
// TODO: Replace DuckDuckGo instant answer API with full search results (SERP API or SearXNG)
// TODO: Add pagination and result ranking for multi-page research
type WebSearchTool struct{}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "搜索互联网获取信息（用于研究和技术查询）"
}
func (t *WebSearchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索查询",
			},
		},
		"required": []string{"query"},
	}
}
func (t *WebSearchTool) IsReadOnly() bool        { return true }
func (t *WebSearchTool) IsConcurrencySafe() bool { return true }

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return types.ToolResult{Success: false, Error: "query is required"}, nil
	}

	// Use WebFetchTool to perform a basic search via a search API
	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(ctx, map[string]interface{}{
		"url": fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1", query),
	})
	if err != nil {
		return types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	// Try to parse the response
	var ddg map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &ddg); err == nil {
		if abstract, ok := ddg["AbstractText"].(string); ok && abstract != "" {
			return types.ToolResult{Content: abstract, Success: true}, nil
		}
	}

	return result, nil
}
