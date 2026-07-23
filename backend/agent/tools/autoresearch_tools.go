package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ─── AutoResearch Tool ────────────────────────────────────────────────

// AutoResearchResult 研究结果
type AutoResearchResult struct {
	Target         string           `json:"target"`
	Metric         string           `json:"metric"`
	Baseline       float64          `json:"baselineMs"`
	BestResult     float64          `json:"bestResultMs"`
	Improvement    float64          `json:"improvementPercent"`
	Iterations     int              `json:"iterations"`
	Converged      bool             `json:"converged"`
	BestHypothesis string           `json:"bestHypothesis"`
	Changes        []CodeChange     `json:"changes"`
	Warnings       []string         `json:"warnings"`
	History        []IterationEntry `json:"history"`
}

// CodeChange 代码变更记录
type CodeChange struct {
	Iteration     int    `json:"iteration"`
	Hypothesis    string `json:"hypothesis"`
	BeforeSize    int    `json:"beforeSize"`
	AfterSize     int    `json:"afterSize"`
	ChangePercent float64 `json:"changePercent"`
	ResultMs      float64 `json:"resultMs"`
	Improved      bool   `json:"improved"`
}

// IterationEntry 迭代历史记录
type IterationEntry struct {
	Iteration      int     `json:"iteration"`
	Hypothesis     string  `json:"hypothesis"`
	ResultMs       float64 `json:"resultMs"`
	IsImprovement  bool    `json:"isImprovement"`
	ChangeSize     int     `json:"changeSize"`
	DurationMs     int64   `json:"durationMs"`
}

// AutoResearchTool implements the hypothesis→modify→experiment→verify loop
// as a single tool that the AI agent can delegate complex optimization tasks to.
// Uses LLM for hypothesis generation and code modification when available.
type AutoResearchTool struct {
	rootPath       string
	llm            LLMCaller   // injected from ai.Service via Registry
	reportCallback func(string) // 可选的报告回调
}

// SetLLMCaller allows the registry to inject the LLM caller after construction.
func (t *AutoResearchTool) SetLLMCaller(caller LLMCaller) {
	t.llm = caller
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

// SetReportCallback 设置报告回调函数
func (t *AutoResearchTool) SetReportCallback(cb func(string)) {
	t.reportCallback = cb
}

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

	// 初始化研究结果
	result := AutoResearchResult{
		Target:     target,
		Metric:     metric,
		Changes:    make([]CodeChange, 0),
		History:    make([]IterationEntry, 0),
		Warnings:   make([]string, 0),
	}

	var report strings.Builder
	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString("       AutoResearch Report\n")
	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString(fmt.Sprintf("Target: %s\n", target))
	report.WriteString(fmt.Sprintf("Metric: %s\n", metric))
	report.WriteString(fmt.Sprintf("Max Iterations: %d\n\n", maxIter))

	// Phase 0: AI-driven hypothesis generation if not provided
	if hypothesis == "" {
		generated, err := t.generateHypothesis(ctx, target, metric)
		if err == nil && generated != "" {
			hypothesis = generated
			result.BestHypothesis = hypothesis
			report.WriteString(fmt.Sprintf("🤖 AI-Generated Hypothesis:\n   %s\n\n", hypothesis))
		} else {
			report.WriteString("⚠ No initial hypothesis (AI generation unavailable).\n\n")
		}
	} else {
		result.BestHypothesis = hypothesis
		report.WriteString(fmt.Sprintf("📝 Initial Hypothesis:\n   %s\n\n", hypothesis))
	}

	// Phase 0.5: pprof analysis if metric is performance-related
	if metric == "execution_time" || metric == "memory_usage" || metric == "cpu_usage" {
		pprofReport := runPprofAnalysis(t.rootPath, target)
		if pprofReport != "" {
			report.WriteString("📊 pprof Analysis:\n")
			for _, line := range strings.Split(pprofReport, "\n") {
				if line != "" {
					report.WriteString(fmt.Sprintf("   %s\n", line))
				}
			}
			report.WriteString("\n")
		}
	}

	// Phase 1: Baseline measurement with verification
	baseline, baselineErr := runBenchmark(t.rootPath, testCmd)
	if baselineErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Baseline measurement failed: %s", baselineErr))
		report.WriteString(fmt.Sprintf("❌ Baseline measurement failed: %s\n", baselineErr))
		return types.ToolResult{Content: report.String(), Success: false}, nil
	}
	result.Baseline = parseMillis(baseline)
	report.WriteString(fmt.Sprintf("📏 Baseline: %.2fms\n\n", result.Baseline))

	// Phase 2: Iterative improvement loop with automatic code modification
	bestMs := result.Baseline
	bestContent := ""
	iterations := 0
	noImproveCount := 0
	stagnationThreshold := 3 // 连续 3 次无改进则停止

	for i := 0; i < maxIter; i++ {
		iterStart := time.Now()
		iterations++
		result.Iterations = iterations

		report.WriteString(fmt.Sprintf("┌─ Iteration %d ─────────────────────────────────\n", i+1))

		// Read current file content
		readTool := &ReadFileTool{rootPath: t.rootPath}
		readResult, err := readTool.Execute(ctx, map[string]interface{}{"path": target})
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Iteration %d: Failed to read target", i+1))
			report.WriteString(fmt.Sprintf("   ❌ Failed to read target: %s\n", err))
			report.WriteString("└─────────────────────────────────────────────\n\n")
			break
		}
		if !readResult.Success {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Iteration %d: Read failed", i+1))
			report.WriteString(fmt.Sprintf("   ❌ Read failed: %s\n", readResult.Content))
			report.WriteString("└─────────────────────────────────────────────\n\n")
			break
		}
		currentContent := readResult.Content
		beforeSize := len(currentContent)

		// Auto-modify: propose and apply code changes via AI
		modified, modReport, err := t.autoModifyCode(ctx, target, currentContent, hypothesis, metric)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Iteration %d: Auto-modify failed", i+1))
			report.WriteString(fmt.Sprintf("   ⚠ Auto-modify failed: %s\n", err))
			report.WriteString("└─────────────────────────────────────────────\n\n")
			continue
		}
		afterSize := len(modified)
		changePercent := float64(afterSize-beforeSize) / float64(beforeSize) * 100

		report.WriteString(fmt.Sprintf("   🔧 Modification: %s\n", modReport))
		report.WriteString(fmt.Sprintf("   📝 Change: %d bytes (%.1f%%)\n", afterSize-beforeSize, changePercent))

		// Run test/benchmark on modified code
		resultMs, err := runBenchmarkWithMetric(t.rootPath, testCmd, metric)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Iteration %d: Benchmark failed, reverting", i+1))
			report.WriteString(fmt.Sprintf("   ❌ Benchmark failed: %s — reverting\n", err))
			// Revert to previous content
			writeTool := &WriteFileTool{rootPath: t.rootPath}
			writeTool.Execute(ctx, map[string]interface{}{"path": target, "content": currentContent})
			report.WriteString("└─────────────────────────────────────────────\n\n")
			continue
		}

		iterDuration := time.Since(iterStart).Milliseconds()

		// Compare with best
		isImprovement := resultMs < bestMs
		change := CodeChange{
			Iteration:     i + 1,
			Hypothesis:    hypothesis,
			BeforeSize:    beforeSize,
			AfterSize:     afterSize,
			ChangePercent: changePercent,
			ResultMs:      resultMs,
			Improved:      isImprovement,
		}
		result.Changes = append(result.Changes, change)

		entry := IterationEntry{
			Iteration:     i + 1,
			Hypothesis:    hypothesis,
			ResultMs:      resultMs,
			IsImprovement: isImprovement,
			ChangeSize:    afterSize - beforeSize,
			DurationMs:    iterDuration,
		}
		result.History = append(result.History, entry)

		report.WriteString(fmt.Sprintf("   📊 Result: %.2fms", resultMs))
		if isImprovement {
			improvement := (bestMs - resultMs) / bestMs * 100
			report.WriteString(fmt.Sprintf(" (↓ %.1f%% improvement!)\n", improvement))
			bestMs = resultMs
			bestContent = modified
			noImproveCount = 0
			report.WriteString("   ✅ ✓ Improvement detected!\n")
		} else {
			noImproveCount++
			if resultMs == bestMs {
				report.WriteString(" (no change)\n")
			} else {
				report.WriteString(fmt.Sprintf(" (worse by %.1f%%)\n", (resultMs-bestMs)/bestMs*100))
			}
			report.WriteString("   ⏪ No improvement — reverting\n")
			// Revert to best known content
			if bestContent != "" {
				writeTool := &WriteFileTool{rootPath: t.rootPath}
				writeTool.Execute(ctx, map[string]interface{}{"path": target, "content": bestContent})
			} else {
				writeTool := &WriteFileTool{rootPath: t.rootPath}
				writeTool.Execute(ctx, map[string]interface{}{"path": target, "content": currentContent})
			}
		}

		// Converged: no improvement for N consecutive iterations
		if noImproveCount >= stagnationThreshold {
			result.Converged = true
			report.WriteString(fmt.Sprintf("   🏁 Converged after %d iterations without improvement\n", noImproveCount))
			break
		}

		report.WriteString(fmt.Sprintf("   ⏱ Duration: %dms\n", iterDuration))
		report.WriteString("└─────────────────────────────────────────────\n\n")

		// 更新假设基于上一次结果
		if isImprovement && t.llm != nil {
			if newHyp := t.refineHypothesis(ctx, hypothesis, resultMs, bestMs); newHyp != "" {
				hypothesis = newHyp
				report.WriteString(fmt.Sprintf("   🔄 Refined hypothesis: %s\n\n", hypothesis))
			}
		}
	}

	// 最终结果
	result.BestResult = bestMs
	if result.Baseline > 0 {
		result.Improvement = (result.Baseline - result.BestResult) / result.Baseline * 100
	}

	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString("                  Summary\n")
	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString(fmt.Sprintf("Iterations: %d\n", result.Iterations))
	report.WriteString(fmt.Sprintf("Baseline: %.2fms\n", result.Baseline))
	report.WriteString(fmt.Sprintf("Best: %.2fms\n", result.BestResult))
	if result.Improvement > 0 {
		report.WriteString(fmt.Sprintf("🎉 Improvement: %.1f%%\n", result.Improvement))
	} else {
		report.WriteString("⚠ No improvement achieved.\n")
	}
	if result.Converged {
		report.WriteString("📍 Status: Converged (no further improvement detected)\n")
	}

	// 警告汇总
	if len(result.Warnings) > 0 {
		report.WriteString("\n⚠ Warnings:\n")
		for _, w := range result.Warnings {
			report.WriteString(fmt.Sprintf("  • %s\n", w))
		}
	}

	// 触发报告回调（如果配置了）
	if t.reportCallback != nil {
		t.reportCallback(report.String())
	}

	return types.ToolResult{Content: report.String(), Success: true}, nil
}

// runBenchmarkWithMetric 运行基准测试并提取指定指标
func runBenchmarkWithMetric(rootPath string, testCmd string, metric string) (float64, error) {
	if testCmd == "" {
		return 0, fmt.Errorf("no test command provided")
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", testCmd)
	cmd.Dir = rootPath
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err != nil {
		return 0, fmt.Errorf("test failed: %s", string(output[:min(len(output), 200)]))
	}

	// 尝试从输出中提取指标
	outputStr := string(output)

	switch metric {
	case "execution_time":
		// 尝试匹配 Go benchmark 输出格式 "X ns/op"
		if nsMatch := regexp.MustCompile(`(\d+)\s*ns/op`).FindStringSubmatch(outputStr); len(nsMatch) > 1 {
			if ns, err := strconv.ParseFloat(nsMatch[1], 64); err == nil {
				return ns / 1_000_000, nil // ns to ms
			}
		}
		// 尝试匹配 "X ms/op"
		if msMatch := regexp.MustCompile(`(\d+\.?\d*)\s*ms/op`).FindStringSubmatch(outputStr); len(msMatch) > 1 {
			if ms, err := strconv.ParseFloat(msMatch[1], 64); err == nil {
				return ms, nil
			}
		}
		// 回退到总执行时间
		return elapsed.Seconds() * 1000, nil

	case "memory_usage":
		// 尝试匹配 Go benchmark 的 B/op
		if bMatch := regexp.MustCompile(`(\d+)\s*B/op`).FindStringSubmatch(outputStr); len(bMatch) > 1 {
			if b, err := strconv.ParseFloat(bMatch[1], 64); err == nil {
				return b / 1024 / 1024, nil // bytes to MB
			}
		}
		return elapsed.Seconds() * 1000, nil // 回退

	case "cpu_usage":
		// CPU 使用率估算
		return elapsed.Seconds() * 100, nil // 简化的 CPU 负载估算

	default:
		return elapsed.Seconds() * 1000, nil
	}
}

// refineHypothesis 基于结果改进假设
func (t *AutoResearchTool) refineHypothesis(ctx context.Context, currentHyp string, currentMs, bestMs float64) string {
	if t.llm == nil {
		return ""
	}

	prompt := fmt.Sprintf(`基于以下优化结果，生成一个改进的假设：

当前假设: %s
当前结果: %.2fms
历史最佳: %.2fms
改进程度: %.1f%%

请生成一个更精确的假设来进一步优化性能。只返回一个简短的假设句子，不要解释。`, currentHyp, currentMs, bestMs, (bestMs-currentMs)/bestMs*100)

	resp, err := t.llm.CallLLM(ctx, prompt)
	if err != nil || resp == "" {
		return ""
	}
	return strings.TrimSpace(resp)
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

// generateHypothesis uses LLM to generate an optimization hypothesis for the target file.
// Falls back to heuristic-based hypothesis if LLM is unavailable.
func (t *AutoResearchTool) generateHypothesis(ctx context.Context, target, metric string) (string, error) {
	readTool := &ReadFileTool{rootPath: t.rootPath}
	readResult, err := readTool.Execute(ctx, map[string]interface{}{"path": target})
	if err != nil || !readResult.Success {
		return "", fmt.Errorf("cannot read target for hypothesis generation: %v", err)
	}
	content := readResult.Content
	if len(content) > 3000 {
		content = content[:3000] + "\n... (truncated)"
	}

	prompt := fmt.Sprintf("Analyze the following code and suggest ONE specific optimization hypothesis for improving %s. "+
		"Be concise (1-2 sentences). Only state the hypothesis, no explanation.\n\nFile: %s\n```\n%s\n```", metric, target, content)

	// Try real LLM call first
	if t.llm != nil {
		resp, err := t.llm.CallLLM(ctx, prompt)
		if err == nil && resp != "" {
			return strings.TrimSpace(resp), nil
		}
	}

	// Fallback: heuristic-based hypothesis
	return generateHeuristicHypothesis(content, metric), nil
}

// generateHeuristicHypothesis generates a basic hypothesis from code patterns.
func generateHeuristicHypothesis(code, metric string) string {
	switch metric {
	case "execution_time":
		if strings.Count(code, "for ") >= 2 {
			return "Nested loops may cause O(n²) complexity; consider reducing inner loop or using a map lookup."
		}
		if strings.Contains(code, "append(") || strings.Contains(code, ".append(") {
			return "Repeated append in a loop may cause repeated allocations; pre-allocate slice/array if size is known."
		}
		return "Consider profiling hot paths and caching repeated computations."
	case "memory_usage":
		if strings.Contains(code, "make(") || strings.Contains(code, "new(") {
			return "Large allocations may be avoidable; consider streaming or chunked processing."
		}
		return "Look for large in-memory data structures that could be streamed or lazily loaded."
	case "cpu_usage":
		return "Identify CPU-hot functions and check for unnecessary repeated computation or redundant regex compilation."
	default:
		return "Analyze the code for common performance anti-patterns relevant to " + metric
	}
}

// runPprofAnalysis runs Go pprof on the target if it's a Go project, returning a summary.
func runPprofAnalysis(rootPath, target string) string {
	// Check if this is a Go project
	goMod := filepath.Join(rootPath, "go.mod")
	if _, err := os.Stat(goMod); err != nil {
		return "" // Not a Go project, skip pprof
	}

	// Try to run go test -cpuprofile
	profileFile := filepath.Join(rootPath, ".tiancan", "cpuprofile.out")
	os.MkdirAll(filepath.Join(rootPath, ".tiancan"), 0755)

	testCmd := fmt.Sprintf("cd %s && go test -cpuprofile=%s ./... 2>&1 | tail -5", rootPath, profileFile)
	cmd := exec.Command("sh", "-c", testCmd)
	cmd.Dir = rootPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("pprof benchmark failed: %s", string(output[:min(len(output), 300)]))
	}

	// Run go tool pprof -top
	topCmd := fmt.Sprintf("go tool pprof -top %s 2>&1 | head -20", profileFile)
	cmd = exec.Command("sh", "-c", topCmd)
	cmd.Dir = rootPath
	topOutput, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	// Clean up profile file
	os.Remove(profileFile)

	// Extract top functions using regex
	topFuncs := regexp.MustCompile(`\S+\s+\d+%.*`).FindAllString(string(topOutput), 5)
	if len(topFuncs) == 0 {
		return string(topOutput[:min(len(topOutput), 500)])
	}
	return "Top CPU consumers:\n" + strings.Join(topFuncs, "\n")
}

// autoModifyCode uses LLM to propose code modifications, then applies them.
// Falls back to heuristic-based modifications if LLM is unavailable.
func (t *AutoResearchTool) autoModifyCode(ctx context.Context, target, currentContent, hypothesis, metric string) (string, string, error) {
	codeSnippet := currentContent
	if len(codeSnippet) > 4000 {
		codeSnippet = codeSnippet[:4000] + "\n... (truncated)"
	}

	prompt := fmt.Sprintf("Optimize the following code for %s. Hypothesis: %s\n"+
		"Apply ONLY small, safe changes. Return the COMPLETE modified file content, nothing else.\n"+
		"Do NOT add comments about what you changed. Just return the code.\n\n"+
		"File: %s\n```\n%s\n```", metric, hypothesis, target, codeSnippet)

	var modified string
	var desc string

	// Try real LLM modification first
	if t.llm != nil {
		resp, err := t.llm.CallLLM(ctx, prompt)
		if err == nil && resp != "" {
			// Extract code from LLM response (may be wrapped in markdown code block)
			modified = extractCodeFromResponse(resp)
			if modified != "" && modified != currentContent {
				desc = "LLM-generated optimization"
			}
		}
	}

	// Fallback: heuristic-based modification
	if modified == "" || modified == currentContent {
		heuristicMod, heuristicDesc, err := applyHeuristicModification(currentContent, hypothesis, metric)
		if err != nil {
			return "", "", fmt.Errorf("modification failed: %v", err)
		}
		modified = heuristicMod
		desc = heuristicDesc
	}

	// Write the modified content to the file
	writeTool := &WriteFileTool{rootPath: t.rootPath}
	_, writeErr := writeTool.Execute(ctx, map[string]interface{}{
		"path":    target,
		"content": modified,
	})
	if writeErr != nil {
		return "", "", fmt.Errorf("failed to write modified file: %v", writeErr)
	}

	return modified, desc, nil
}

// extractCodeFromResponse extracts code content from an LLM response that may
// contain markdown code blocks or extra text.
func extractCodeFromResponse(resp string) string {
	// Try to extract from markdown code block
	startMarker := "```"
	startIdx := strings.Index(resp, startMarker)
	if startIdx == -1 {
		return strings.TrimSpace(resp)
	}
	// Skip the opening ``` and optional language tag
	afterStart := startIdx + len(startMarker)
	// Skip language tag line (e.g. ```go)
	if afterStart < len(resp) && resp[afterStart] != '\n' {
		newlineIdx := strings.Index(resp[afterStart:], "\n")
		if newlineIdx != -1 {
			afterStart += newlineIdx + 1
		} else {
			afterStart = len(resp)
		}
	} else if afterStart < len(resp) {
		afterStart++ // skip the newline after ```
	}

	// Find closing ```
	endMarker := "```"
	endIdx := strings.Index(resp[afterStart:], endMarker)
	if endIdx == -1 {
		return strings.TrimSpace(resp[afterStart:])
	}

	return strings.TrimSpace(resp[afterStart : afterStart+endIdx])
}

// applyHeuristicModification applies simple pattern-based code modifications.
func applyHeuristicModification(code, hypothesis, metric string) (string, string, error) {
	// Apply safe heuristic transformations based on hypothesis keywords
	switch {
	case strings.Contains(hypothesis, "map lookup") || strings.Contains(hypothesis, "pre-allocate"):
		// These are structural changes that require understanding — mark as no-op for safety
		return code, "heuristic: structural change needed (manual review required)", nil
	case strings.Contains(hypothesis, "caching") || strings.Contains(hypothesis, "cache"):
		// No automatic cache insertion — too risky
		return code, "heuristic: caching optimization suggested (requires manual implementation)", nil
	case strings.Contains(hypothesis, "streaming") || strings.Contains(hypothesis, "lazy"):
		return code, "heuristic: streaming/lazy loading suggested (requires manual implementation)", nil
	default:
		// No safe automatic modification available
		return code, "heuristic: no safe auto-modification available for this hypothesis", nil
	}
}

// ─── Web Search Tool (for AutoResearch extension) ─────────────────────

// WebSearchTool allows the agent to search the web for information.
// Uses SearXNG for full search results with pagination support.
// Falls back to DuckDuckGo instant answers if SearXNG is unavailable.
type WebSearchTool struct{}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "搜索互联网获取信息（用于研究和技术查询），支持分页"
}
func (t *WebSearchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索查询",
			},
			"page": map[string]interface{}{
				"type":        "number",
				"description": "页码（从1开始，默认1）",
			},
			"categories": map[string]interface{}{
				"type":        "string",
				"description": "搜索类别（可选：general, news, science, it, files, images）",
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
	page := 1
	if v, ok := args["page"].(float64); ok && v > 0 {
		page = int(v)
	}
	categories, _ := args["categories"].(string)

	// Try SearXNG first (configurable via TIANCAN_SEARXNG_URL env)
	searxURL := os.Getenv("TIANCAN_SEARXNG_URL")
	if searxURL == "" {
		searxURL = "https://searx.be" // public SearXNG instance
	}

	result, err := searchSearXNG(searxURL, query, page, categories)
	if err == nil && result != "" {
		return types.ToolResult{Content: result, Success: true}, nil
	}

	// Fallback to DuckDuckGo instant answers
	return searchDuckDuckGo(query)
}

// searchSearXNG queries a SearXNG instance and returns formatted results.
func searchSearXNG(baseURL, query string, page int, categories string) (string, error) {
	pagenum := page - 1 // SearXNG uses 0-indexed pages
	url := fmt.Sprintf("%s/search?q=%s&format=json&pageno=%d", baseURL, query, pagenum)
	if categories != "" {
		url += "&categories=" + categories
	}

	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(context.Background(), map[string]interface{}{"url": url})
	if err != nil {
		return "", err
	}

	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &resp); err != nil {
		return "", err
	}

	results, ok := resp["results"].([]interface{})
	if !ok || len(results) == 0 {
		return "", fmt.Errorf("no results from SearXNG")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("搜索结果 (第%d页, 共%d条):\n", page, len(results)))
	for i, r := range results {
		item, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		title, _ := item["title"].(string)
		url, _ := item["url"].(string)
		snippet, _ := item["content"].(string)
		b.WriteString(fmt.Sprintf("%d. %s\n   URL: %s\n", i+1, title, url))
		if snippet != "" {
			b.WriteString(fmt.Sprintf("   %s\n", snippet))
		}
	}

	// Add pagination hint
	if len(results) >= 10 {
		b.WriteString(fmt.Sprintf("\n提示: 使用 page=%d 获取下一页结果", page+1))
	}

	return b.String(), nil
}

// searchDuckDuckGo is the fallback search using DuckDuckGo instant answers.
func searchDuckDuckGo(query string) (types.ToolResult, error) {
	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(context.Background(), map[string]interface{}{
		"url": fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1", query),
	})
	if err != nil {
		return types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	var ddg map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &ddg); err == nil {
		if abstract, ok := ddg["AbstractText"].(string); ok && abstract != "" {
			return types.ToolResult{Content: abstract, Success: true}, nil
		}
	}

	return result, nil
}
