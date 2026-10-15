package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// ─── AutoResearch Tool ────────────────────────────────────────────────

// AutoResearchResult 研究结果
type AutoResearchResult struct {
	AgentID         string           `json:"agentId"`
	Target          string           `json:"target"`
	Metric          string           `json:"metric"`
	Baseline        float64          `json:"baselineMs"`
	BestResult      float64          `json:"bestResultMs"`
	Improvement     float64          `json:"improvementPercent"`
	Iterations      int              `json:"iterations"`
	Converged       bool             `json:"converged"`
	BestHypothesis  string           `json:"bestHypothesis"`
	Changes         []CodeChange     `json:"changes"`
	Warnings        []string         `json:"warnings"`
	History         []IterationEntry `json:"history"`
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
	llm            LLMCaller // injected from ai.Service via Registry
	reportCallback func(string)
	progressCh     chan<- ProgressUpdate // 进度更新通道
	app            interface{}           // Wails App 实例，用于发送事件

	// AI-driven 研究闭环状态
	activeHypotheses []string            // 当前活跃的假设列表
	iterationHistory []IterationEntry    // 迭代历史
	bestResult       *AutoResearchResult // 最佳结果
}

// ResearchContext 研究上下文，用于 AI 驱动的决策
type ResearchContext struct {
	Target       string            `json:"target"`
	Metric       string            `json:"metric"`
	CurrentCode  string            `json:"currentCode"`
	Hypothesis   string            `json:"hypothesis"`
	History      []IterationEntry  `json:"history"`
	BestMs       float64           `json:"bestMs"`
	BaselineMs   float64           `json:"baselineMs"`
	Iteration    int               `json:"iteration"`
	MaxIter      int               `json:"maxIter"`
}

// DecisionRequest AI 决策请求
type DecisionRequest struct {
	Context    ResearchContext `json:"context"`
	Action     string          `json:"action"`      // "hypothesis", "modify", "revert", "converge"
	Description string         `json:"description"`
}

// ProgressUpdate 进度更新
type ProgressUpdate struct {
	AgentID     string  `json:"agentId"`       // Agent ID 用于追踪
	Phase       string  `json:"phase"`         // "hypothesis", "iteration", "baseline", "verification", "complete"
	Iteration   int     `json:"iteration"`     // 当前迭代
	TotalIters  int     `json:"totalIters"`    // 总迭代数
	Message     string  `json:"message"`       // 状态消息
	Progress    float64 `json:"progress"`      // 0-100 进度百分比
	ResultMs    float64 `json:"resultMs"`      // 当前结果
	BaselineMs  float64 `json:"baselineMs"`    // 基线
	Improvement float64 `json:"improvement"`   // 改进百分比
	Target      string  `json:"target"`        // 目标文件
	Metric      string  `json:"metric"`        // 优化指标
}

// emitComplete 发送完成事件到前端
func (t *AutoResearchTool) emitComplete(result *AutoResearchResult) {
	if t.app != nil {
		t.emitWailsEvent("autoresearch:complete", result)
	}
	// 发送最终进度更新
	t.emitProgress(ProgressUpdate{
		AgentID:     result.AgentID,
		Phase:       "complete",
		Message:     "研究完成",
		Progress:    100,
		ResultMs:    result.BestResult,
		BaselineMs:  result.Baseline,
		Improvement: result.Improvement,
		Target:      result.Target,
		Metric:      result.Metric,
	})
}

// emitStart 发送开始事件到前端
func (t *AutoResearchTool) emitStart(agentID, target, metric string, totalIters int) {
	if t.app != nil {
		t.emitWailsEvent("autoresearch:start", map[string]interface{}{
			"agentId":     agentID,
			"target":      target,
			"metric":      metric,
			"totalIters":  totalIters,
			"startTime":   time.Now().Unix(),
		})
	}
	// 发送初始进度
	t.emitProgress(ProgressUpdate{
		AgentID:    agentID,
		Phase:      "baseline",
		Message:    "正在测量基线性能...",
		Progress:   5,
		Target:    target,
		Metric:    metric,
	})
}

// SetProgressChannel 设置进度更新通道
func (t *AutoResearchTool) SetProgressChannel(ch chan<- ProgressUpdate) {
	t.progressCh = ch
}

// SetApp 设置 Wails App 实例用于发送事件
func (t *AutoResearchTool) SetApp(app interface{}) {
	t.app = app
}

// SetReportCallback 设置报告回调函数
func (t *AutoResearchTool) SetReportCallback(cb func(string)) {
	t.reportCallback = cb
}

// emitProgress 发送进度更新
func (t *AutoResearchTool) emitProgress(p ProgressUpdate) {
	// 发送到 channel
	if t.progressCh != nil {
		select {
		case t.progressCh <- p:
		default:
			// channel 满了，跳过
		}
	}

	// 发送到 Wails 事件（如果可用）
	if t.app != nil {
		t.emitWailsEvent("autoresearch:progress", p)
	}
}

// emitWailsEvent 发送 Wails 事件到前端
func (t *AutoResearchTool) emitWailsEvent(eventName string, data interface{}) {
	if t.app == nil {
		return
	}

	// 使用反射调用 Wails 的 Event.Emit 方法
	// Wails App 类型有 Event.Emit(eventName, data) 方法
	if app, ok := t.app.(interface{ Emit(string, interface{}) }); ok {
		app.Emit(eventName, data)
	}
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

	// 生成 Agent ID 用于追踪
	agentID := fmt.Sprintf("autoresearch-%d", time.Now().UnixNano())

	// 初始化研究结果
	result := AutoResearchResult{
		Target:     target,
		Metric:     metric,
		Changes:    make([]CodeChange, 0),
		History:    make([]IterationEntry, 0),
		Warnings:   make([]string, 0),
	}

	// 发送开始事件
	t.emitStart(agentID, target, metric, maxIter)

	// 发送开始进度
	t.emitProgress(ProgressUpdate{
		AgentID:    agentID,
		Phase:      "baseline",
		TotalIters: maxIter,
		Message:    "Starting AutoResearch...",
		Progress:   0,
		Target:     target,
		Metric:     metric,
	})

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

		// 发送迭代开始进度
		t.emitProgress(ProgressUpdate{
			Phase:      "iteration",
			Iteration:  i + 1,
			TotalIters: maxIter,
			Message:    fmt.Sprintf("Running iteration %d...", i+1),
			Progress:   float64(i) / float64(maxIter) * 100,
			BaselineMs: result.Baseline,
		})

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

			// 发送改进进度
			t.emitProgress(ProgressUpdate{
				Phase:       "iteration",
				Iteration:   i + 1,
				TotalIters:  maxIter,
				Message:     fmt.Sprintf("Iteration %d: %.1f%% improvement!", i+1, improvement),
				Progress:    float64(i+1) / float64(maxIter) * 100,
				ResultMs:    resultMs,
				BaselineMs:  result.Baseline,
				Improvement: improvement,
			})
		} else {
			noImproveCount++
			// 发送无改进进度
			t.emitProgress(ProgressUpdate{
				Phase:      "iteration",
				Iteration:  i + 1,
				TotalIters: maxIter,
				Message:    fmt.Sprintf("Iteration %d: no improvement, reverting", i+1),
				Progress:   float64(i+1) / float64(maxIter) * 100,
				ResultMs:   resultMs,
				BaselineMs: result.Baseline,
			})
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

	// 发送完成进度
	t.emitProgress(ProgressUpdate{
		Phase:       "complete",
		TotalIters:  maxIter,
		Message:     fmt.Sprintf("AutoResearch complete: %.1f%% improvement", result.Improvement),
		Progress:    100,
		ResultMs:    bestMs,
		BaselineMs:  result.Baseline,
		Improvement: result.Improvement,
	})

	// 触发报告回调（如果配置了）
	if t.reportCallback != nil {
		t.reportCallback(report.String())
	}

	// Phase 3: Final verification
	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString("       Final Verification\n")
	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	// Run verification test
	verified, verifyErr := t.verifyChanges(t.rootPath, target, bestContent)
	if verifyErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Verification error: %s", verifyErr))
		report.WriteString(fmt.Sprintf("⚠ Verification: %s\n", verifyErr))
	} else if verified {
		report.WriteString("✅ All verifications passed!\n")
	} else {
		result.Warnings = append(result.Warnings, "Verification: some checks failed")
		report.WriteString("⚠ Some verification checks failed\n")
	}

	// Final benchmark comparison
	result.BestResult = bestMs
	baselineMs := result.Baseline
	if baselineMs > 0 && bestMs < baselineMs {
		result.Improvement = (baselineMs - bestMs) / baselineMs * 100
	}

	// Summary
	report.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString("       Summary\n")
	report.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	report.WriteString(fmt.Sprintf("Total Iterations: %d\n", iterations))
	report.WriteString(fmt.Sprintf("Baseline: %.2fms\n", baselineMs))
	report.WriteString(fmt.Sprintf("Best Result: %.2fms\n", bestMs))
	if result.Improvement > 0 {
		report.WriteString(fmt.Sprintf("Improvement: %.1f%%\n", result.Improvement))
	} else {
		report.WriteString("No improvement achieved.\n")
	}

	// Report best changes
	if len(result.Changes) > 0 {
		report.WriteString("\nBest Changes:\n")
		for _, change := range result.Changes {
			if change.Improved {
				report.WriteString(fmt.Sprintf("  • Iter %d: %.1f%% improvement\n", change.Iteration, (result.Baseline-change.ResultMs)/result.Baseline*100))
			}
		}
	}

	// Emit completion event if callback set
	if t.reportCallback != nil {
		t.reportCallback(report.String())
	}

	// 发送完成事件
	result.AgentID = agentID
	t.emitComplete(&result)

	return types.ToolResult{Content: report.String(), Success: true}, nil
}

// verifyChanges 使用 AI 验证代码变更
func (t *AutoResearchTool) verifyChanges(rootPath, target, content string) (bool, error) {
	if t.llm == nil {
		return true, nil // 无 LLM 时跳过验证
	}

	ctx := context.Background()

	// 读取原始文件
	readTool := &ReadFileTool{rootPath: rootPath}
	originalResult, err := readTool.Execute(ctx, map[string]interface{}{"path": target})
	if err != nil || !originalResult.Success {
		return false, fmt.Errorf("cannot read original file: %v", err)
	}

	prompt := fmt.Sprintf(`Verify the following code changes for correctness.
Focus on: syntax errors, logic errors, potential bugs, and security issues.

ORIGINAL CODE:
%s

NEW CODE:
%s

Respond with:
1. Is the change syntactically correct? (yes/no)
2. Does it maintain the original functionality? (yes/no/maybe)
3. Any potential issues? (brief description or "none")
4. Overall recommendation: (safe/proceed with caution/unsafe)`, originalResult.Content, content)

	resp, err := t.llm.CallLLM(ctx, prompt)
	if err != nil || resp == "" {
		return true, nil // LLM 失败时默认通过
	}

	// 简单检查 LLM 响应
	lower := strings.ToLower(resp)
	if strings.Contains(lower, "unsafe") || strings.Contains(lower, "incorrect") {
		return false, fmt.Errorf("AI flagged potential issues: %s", resp)
	}

	return true, nil
}

// getBestIteration 获取最佳迭代的结果
func getBestIteration(history []IterationEntry) *IterationEntry {
	if len(history) == 0 {
		return nil
	}

	best := &history[0]
	for i := 1; i < len(history); i++ {
		if history[i].IsImprovement && history[i].ResultMs < best.ResultMs {
			best = &history[i]
		}
	}
	return best
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

// AI-driven Research Decision Making

// DecisionResult AI 决策结果
type DecisionResult struct {
	Action        string   `json:"action"` // "continue", "modify_strategy", "revert", "converge"
	NextHypothesis string   `json:"nextHypothesis,omitempty"`
	Reason        string   `json:"reason"`
	Confidence    float64  `json:"confidence"` // 0-1
	Warnings      []string `json:"warnings,omitempty"`
}

// MakeResearchDecision AI 驱动的决策：决定下一步行动
func (t *AutoResearchTool) MakeResearchDecision(ctx context.Context, researchCtx *ResearchContext) *DecisionResult {
	if t.llm == nil {
		// Fallback to simple heuristics
		return t.makeHeuristicDecision(researchCtx)
	}

	prompt := fmt.Sprintf(`你是自动化代码优化的 AI 决策器。基于以下研究上下文，决定下一步行动：

研究目标: %s
优化指标: %s
当前迭代: %d/%d
当前假设: %s
当前结果: %.2fms
历史最佳: %.2fms
基线: %.2fms
改进: %.1f%%

迭代历史:
%s

请分析以上信息，决定下一步行动：
1. "continue" - 继续当前策略，基于当前假设继续优化
2. "modify_strategy" - 修改策略，生成新的假设方向
3. "revert" - 回退到最佳状态，放弃当前尝试
4. "converge" - 收敛，停止研究循环

返回 JSON 格式：
{
  "action": "continue|modify_strategy|revert|converge",
  "nextHypothesis": "如果修改策略，描述新的假设",
  "reason": "决策原因",
  "confidence": 0.0-1.0,
  "warnings": ["可选警告"]
}

只返回 JSON，不要有其他内容。`, researchCtx.Target, researchCtx.Metric,
		researchCtx.Iteration, researchCtx.MaxIter,
		researchCtx.Hypothesis, researchCtx.BestMs,
		minFloat(researchCtx.BestMs, researchCtx.BaselineMs),
		researchCtx.BaselineMs,
		calculateImprovement(researchCtx.BaselineMs, researchCtx.BestMs),
		formatHistory(researchCtx.History))

	resp, err := t.llm.CallLLM(ctx, prompt)
	if err != nil || resp == "" {
		return t.makeHeuristicDecision(researchCtx)
	}

	// 解析 AI 响应
	result, err := parseDecisionResponse(resp)
	if err != nil {
		return t.makeHeuristicDecision(researchCtx)
	}

	return result
}

// makeHeuristicDecision 基于启发式规则的决策（无 AI 时使用）
func (t *AutoResearchTool) makeHeuristicDecision(ctx *ResearchContext) *DecisionResult {
	result := &DecisionResult{Confidence: 0.5}

	// 检查是否收敛
	improvement := calculateImprovement(ctx.BaselineMs, ctx.BestMs)
	if improvement < 1.0 && ctx.Iteration > 2 {
		// 连续迭代改进 < 1%，认为收敛
		result.Action = "converge"
		result.Reason = fmt.Sprintf("改进率 %.1f%% 低于阈值，停止研究", improvement)
		return result
	}

	// 检查迭代次数
	if ctx.Iteration >= ctx.MaxIter {
		result.Action = "converge"
		result.Reason = "达到最大迭代次数"
		return result
	}

	// 检查是否有改进
	if ctx.BestMs < ctx.BaselineMs {
		result.Action = "continue"
		result.Reason = "当前策略有效，继续优化"
		result.Confidence = 0.7
	} else {
		result.Action = "modify_strategy"
		result.NextHypothesis = t.getAlternativeHypothesis(ctx.Hypothesis, ctx.Metric)
		result.Reason = "当前策略无效，尝试其他方向"
		result.Warnings = []string{"回退到之前的最佳状态"}
	}

	return result
}

// parseDecisionResponse 解析 AI 决策响应
func parseDecisionResponse(resp string) (*DecisionResult, error) {
	resp = strings.TrimSpace(resp)

	// 尝试提取 JSON
	if !strings.HasPrefix(resp, "{") {
		// 尝试找到 JSON 开始位置
		start := strings.Index(resp, "{")
		end := strings.LastIndex(resp, "}")
		if start != -1 && end != -1 {
			resp = resp[start : end+1]
		}
	}

	var result DecisionResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, err
	}

	// 验证 action
	validActions := map[string]bool{"continue": true, "modify_strategy": true, "revert": true, "converge": true}
	if !validActions[result.Action] {
		result.Action = "continue"
	}

	return &result, nil
}

// getAlternativeHypothesis 获取替代假设方向
func (t *AutoResearchTool) getAlternativeHypothesis(currentHypothesis, metric string) string {
	switch metric {
	case "execution_time":
		if strings.Contains(currentHypothesis, "loop") {
			return "考虑使用更高效的数据结构或缓存"
		}
		if strings.Contains(currentHypothesis, "memory") {
			return "尝试减少内存分配，关注执行效率"
		}
		return "尝试算法层面的优化"
	case "memory_usage":
		if strings.Contains(currentHypothesis, "allocation") {
			return "考虑使用对象池或预分配策略"
		}
		return "尝试减少不必要的内存分配"
	default:
		return "从不同角度重新审视代码"
	}
}

// formatHistory 格式化迭代历史为文本
func formatHistory(history []IterationEntry) string {
	if len(history) == 0 {
		return "无历史记录"
	}
	var sb strings.Builder
	for _, h := range history {
		status := "❌"
		if h.IsImprovement {
			status = "✅"
		}
		sb.WriteString(fmt.Sprintf("  Iter %d: %s %.2fms - %s\n",
			h.Iteration, status, h.ResultMs, h.Hypothesis))
	}
	return sb.String()
}

// calculateImprovement 计算改进百分比
func calculateImprovement(baseline, current float64) float64 {
	if baseline <= 0 || current <= 0 {
		return 0
	}
	return (baseline - current) / baseline * 100
}

// minFloat 返回两个浮点数的最小值
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ResearchIteration 单次研究迭代的结果
type ResearchIteration struct {
	Iteration    int     `json:"iteration"`
	Hypothesis   string  `json:"hypothesis"`
	Modification string  `json:"modification"`
	BeforeResult string  `json:"beforeResult"`
	AfterResult  string  `json:"afterResult"`
	Improvement  float64 `json:"improvement"` // 百分比
	Converged    bool    `json:"converged"`
}

// ResearchReport 研究报告
type ResearchReport struct {
	Target       string             `json:"target"`
	Metric       string             `json:"metric"`
	Baseline     string             `json:"baseline"`
	BestResult   string             `json:"bestResult"`
	BestImprovement float64         `json:"bestImprovement"`
	Iterations   []ResearchIteration `json:"iterations"`
	TotalTime    time.Duration      `json:"totalTime"`
	Recommendations []string        `json:"recommendations"`
	Converged    bool               `json:"converged"`
	FinalStatus  string             `json:"finalStatus"`
}

// RunBenchmarkWithMetrics 执行带详细指标提取的基准测试
func RunBenchmarkWithMetrics(rootPath string, testCmd string, metric string) (string, float64, error) {
	if testCmd == "" {
		return "no_test_command", 0, nil
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", testCmd)
	cmd.Dir = rootPath
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	resultStr := string(output)
	elapsedMs := elapsed.Seconds() * 1000

	// 尝试从输出中提取具体指标
	var extractedValue float64
	switch metric {
	case "execution_time":
		extractedValue = elapsedMs
		// 尝试从输出中提取更精确的时间
		if ms := extractMetricFromOutput(resultStr, `(\d+\.?\d*)\s*ms`); ms > 0 {
			extractedValue = ms
		}
	case "memory_usage":
		// 尝试从输出中提取内存使用
		if mb := extractMetricFromOutput(resultStr, `(\d+\.?\d*)\s*MB`); mb > 0 {
			extractedValue = mb
		} else if kb := extractMetricFromOutput(resultStr, `(\d+\.?\d*)\s*KB`); kb > 0 {
			extractedValue = kb / 1024
		}
	case "cpu_usage":
		if pct := extractMetricFromOutput(resultStr, `(\d+\.?\d*)\s*%`); pct > 0 {
			extractedValue = pct
		}
	}

	if err != nil {
		return fmt.Sprintf("FAIL(%.0fms)", elapsedMs), extractedValue, fmt.Errorf("test failed: %s", string(output[:min(len(output), 200)]))
	}

	return fmt.Sprintf("%.0fms", extractedValue), extractedValue, nil
}

// extractMetricFromOutput 从命令输出中提取指标值
func extractMetricFromOutput(output string, pattern string) float64 {
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(output)
	if len(matches) > 1 {
		if v, err := strconv.ParseFloat(matches[1], 64); err == nil {
			return v
		}
	}
	return 0
}

// GenerateResearchReport 生成完整的研究报告
func GenerateResearchReport(iterations []ResearchIteration, baseline string, best float64, totalTime time.Duration, target string, metric string) *ResearchReport {
	report := &ResearchReport{
		Target:         target,
		Metric:         metric,
		Baseline:       baseline,
		Iterations:     iterations,
		TotalTime:      totalTime,
		Recommendations: []string{},
	}

	if len(iterations) > 0 {
		// 找最佳迭代
		bestIter := iterations[0]
		for _, iter := range iterations {
			if iter.Improvement > bestIter.Improvement {
				bestIter = iter
			}
		}
		report.BestResult = bestIter.AfterResult
		report.BestImprovement = bestIter.Improvement

		// 检查是否收敛
		report.Converged = bestIter.Converged
		if bestIter.Improvement > 0 {
			report.FinalStatus = "improved"
		} else if bestIter.Improvement == 0 {
			report.FinalStatus = "no_improvement"
		} else {
			report.FinalStatus = "regressed"
		}

		// 生成建议
		report.Recommendations = generateRecommendations(iterations, metric)
	}

	return report
}

// generateRecommendations 根据迭代历史生成优化建议
func generateRecommendations(iterations []ResearchIteration, metric string) []string {
	var recs []string

	// 检查改进趋势
	improvements := make([]float64, len(iterations))
	for i, iter := range iterations {
		improvements[i] = iter.Improvement
	}

	// 如果多次迭代没有改进
	noImproveCount := 0
	for i := len(improvements) - 1; i >= 0; i-- {
		if improvements[i] <= 0 {
			noImproveCount++
		} else {
			break
		}
	}

	if noImproveCount >= 2 {
		recs = append(recs, "建议手动审查代码，人工优化可能更有效")
	}

	// 基于指标类型的建议
	switch metric {
	case "execution_time":
		recs = append(recs, "考虑使用 pprof 进行性能分析定位热点")
		recs = append(recs, "检查是否有重复计算或不必要的内存分配")
	case "memory_usage":
		recs = append(recs, "考虑使用流式处理减少内存占用")
		recs = append(recs, "检查是否有大对象未被及时释放")
	case "cpu_usage":
		recs = append(recs, "考虑使用缓存减少重复计算")
		recs = append(recs, "检查是否有不必要的正则表达式编译")
	}

	return recs
}

// ReportToMarkdown 将研究报告转换为 Markdown 格式
func (r *ResearchReport) ReportToMarkdown() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# AutoResearch 报告\n\n"))
	sb.WriteString(fmt.Sprintf("**目标文件**: %s\n", r.Target))
	sb.WriteString(fmt.Sprintf("**优化指标**: %s\n", r.Metric))
	sb.WriteString(fmt.Sprintf("**基准测试**: %s\n", r.Baseline))
	sb.WriteString(fmt.Sprintf("**最佳结果**: %s (%.1f%% 改进)\n", r.BestResult, r.BestImprovement))
	sb.WriteString(fmt.Sprintf("**迭代次数**: %d\n", len(r.Iterations)))
	sb.WriteString(fmt.Sprintf("**总耗时**: %s\n", r.TotalTime.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("**状态**: %s\n\n", r.FinalStatus))

	if len(r.Iterations) > 0 {
		sb.WriteString("## 迭代详情\n\n")
		sb.WriteString("| 迭代 | 假设 | 修改 | 改进 |\n")
		sb.WriteString("|------|------|------|------|\n")
		for _, iter := range r.Iterations {
			impStr := "—"
			if iter.Improvement > 0 {
				impStr = fmt.Sprintf("+%.1f%%", iter.Improvement)
			} else if iter.Improvement < 0 {
				impStr = fmt.Sprintf("%.1f%%", iter.Improvement)
			}
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n",
				iter.Iteration, iter.Hypothesis, iter.Modification, impStr))
		}
		sb.WriteString("\n")
	}

	if len(r.Recommendations) > 0 {
		sb.WriteString("## 建议\n\n")
		for _, rec := range r.Recommendations {
			sb.WriteString(fmt.Sprintf("- %s\n", rec))
		}
	}

	return sb.String()
}

// VisualizeResults 生成可嵌入的结果展示
func (r *ResearchReport) VisualizeResults() string {
	var sb strings.Builder

	sb.WriteString("```\nAutoResearch 优化结果\n")
	sb.WriteString(fmt.Sprintf("目标: %s\n", r.Target))
	sb.WriteString(fmt.Sprintf("指标: %s\n", r.Metric))
	sb.WriteString(strings.Repeat("-", 40) + "\n")

	// 进度条可视化
	if r.BestImprovement > 0 {
		barLen := int(r.BestImprovement / 5) // 每 5% 一个字符
		if barLen > 20 {
			barLen = 20
		}
		sb.WriteString(fmt.Sprintf("改进: [%s%s] %.1f%%\n",
			strings.Repeat("█", barLen),
			strings.Repeat("░", 20-barLen),
			r.BestImprovement))
	}

	sb.WriteString(fmt.Sprintf("基准: %s → 最佳: %s\n", r.Baseline, r.BestResult))
	sb.WriteString(fmt.Sprintf("迭代: %d 次 | 耗时: %s\n", len(r.Iterations), r.TotalTime.Round(time.Second)))
	sb.WriteString("```\n")

	return sb.String()
}

// ─── AutoResearch 执行器 ───────────────────────────────────────────────

// AutoResearchExecutor 协调 AutoResearch 的执行
type AutoResearchExecutor struct {
	rootPath    string
	llm         LLMCaller
	maxAgents   int
	agentType   string
	isolation   bool
}

// NewAutoResearchExecutor 创建 AutoResearch 执行器
func NewAutoResearchExecutor(rootPath string, llm LLMCaller) *AutoResearchExecutor {
	return &AutoResearchExecutor{
		rootPath:  rootPath,
		llm:       llm,
		maxAgents: 3, // 默认最多 3 个并行 agent
		agentType: "research",
		isolation: true,
	}
}

// SetMaxAgents 设置最大并行 agent 数
func (e *AutoResearchExecutor) SetMaxAgents(n int) {
	if n > 0 && n <= 10 {
		e.maxAgents = n
	}
}

// SetAgentType 设置使用的 agent 类型
func (e *AutoResearchExecutor) SetAgentType(t string) {
	e.agentType = t
}

// SetIsolation 设置是否启用 worktree 隔离
func (e *AutoResearchExecutor) SetIsolation(enabled bool) {
	e.isolation = enabled
}

// ParallelHypothesisGeneration 并行生成多个假设
func (e *AutoResearchExecutor) ParallelHypothesisGeneration(ctx context.Context, target, metric string, count int) ([]string, error) {
	if count <= 0 {
		count = 3
	}
	if count > e.maxAgents {
		count = e.maxAgents
	}

	// 读取目标文件
	readTool := &ReadFileTool{rootPath: e.rootPath}
	readResult, err := readTool.Execute(ctx, map[string]interface{}{"path": target})
	if err != nil || !readResult.Success {
		return nil, fmt.Errorf("cannot read target: %v", err)
	}
	code := readResult.Content
	if len(code) > 3000 {
		code = code[:3000] + "\n... (truncated)"
	}

	// 并行生成假设
	prompts := []string{
		fmt.Sprintf("分析以下代码，从【算法复杂度】角度提出优化假设。文件: %s\n```\n%s\n```", target, code),
		fmt.Sprintf("分析以下代码，从【内存分配】角度提出优化假设。文件: %s\n```\n%s\n```", target, code),
		fmt.Sprintf("分析以下代码，从【代码结构】角度提出优化假设。文件: %s\n```\n%s\n```", target, code),
	}

	if count > 3 {
		prompts = append(prompts,
			fmt.Sprintf("分析以下代码，从【I/O 优化】角度提出优化假设。文件: %s\n```\n%s\n```", target, code),
		)
	}

	// 限制并发数
	if len(prompts) > count {
		prompts = prompts[:count]
	}

	type result struct {
		hypothesis string
		err        error
	}
	resultCh := make(chan result, len(prompts))
	var wg sync.WaitGroup

	for _, prompt := range prompts {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if e.llm != nil {
				resp, err := e.llm.CallLLM(ctx, p)
				if err == nil && resp != "" {
					resultCh <- result{hypothesis: strings.TrimSpace(resp), err: nil}
					return
				}
			}
			// Fallback to heuristic
			resultCh <- result{hypothesis: generateHeuristicHypothesis(code, metric), err: nil}
		}(prompt)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var hypotheses []string
	for r := range resultCh {
		if r.err == nil && r.hypothesis != "" {
			hypotheses = append(hypotheses, r.hypothesis)
		}
	}

	return hypotheses, nil
}

// ParallelCodeModification 并行尝试多种代码修改
func (e *AutoResearchExecutor) ParallelCodeModification(ctx context.Context, target, currentContent string, hypotheses []string, metric string) ([]CodeModification, error) {
	if len(hypotheses) == 0 {
		return nil, fmt.Errorf("no hypotheses provided")
	}
	if len(hypotheses) > e.maxAgents {
		hypotheses = hypotheses[:e.maxAgents]
	}

	type result struct {
		mod CodeModification
		err error
	}
	resultCh := make(chan result, len(hypotheses))
	var wg sync.WaitGroup

	for _, hyp := range hypotheses {
		wg.Add(1)
		go func(hypothesis string) {
			defer wg.Done()

			codeSnippet := currentContent
			if len(codeSnippet) > 4000 {
				codeSnippet = codeSnippet[:4000] + "\n... (truncated)"
			}

			prompt := fmt.Sprintf("优化以下代码。假设: %s\n指标: %s\n只返回优化后的代码，不要解释。\n```\n%s\n```",
				hypothesis, metric, codeSnippet)

			var modified string
			if e.llm != nil {
				resp, err := e.llm.CallLLM(ctx, prompt)
				if err == nil && resp != "" {
					modified = extractCodeFromResponse(resp)
				}
			}

			if modified == "" || modified == currentContent {
				modified, _, _ = applyHeuristicModification(currentContent, hypothesis, metric)
			}

			resultCh <- result{
				mod: CodeModification{
					Hypothesis: hypothesis,
					Content:    modified,
					Modified:   modified != currentContent,
				},
				err: nil,
			}
		}(hyp)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var modifications []CodeModification
	for r := range resultCh {
		if r.err == nil {
			modifications = append(modifications, r.mod)
		}
	}

	return modifications, nil
}

// CodeModification 代表一次代码修改
type CodeModification struct {
	Hypothesis string
	Content    string
	Modified   bool
	Safe       bool
}

// MergeResults 合并多个修改的结果
func (e *AutoResearchExecutor) MergeResults(results []BenchmarkResult) *MergedResearchResult {
	merged := &MergedResearchResult{
		Results:    results,
		BestIndex: -1,
	}

	if len(results) == 0 {
		return merged
	}

	// 找最佳结果
	bestValue := results[0].Value
	merged.BestIndex = 0

	for i, r := range results {
		if r.LowerIsBetter && r.Value < bestValue {
			bestValue = r.Value
			merged.BestIndex = i
		} else if !r.LowerIsBetter && r.Value > bestValue {
			bestValue = r.Value
			merged.BestIndex = i
		}
	}

	if merged.BestIndex >= 0 {
		merged.BestResult = results[merged.BestIndex]
	}

	return merged
}

// BenchmarkResult 基准测试结果
type BenchmarkResult struct {
	Hypothesis string
	Value      float64
	Unit       string
	LowerIsBetter bool
	Passed      bool
	Error       string
}

// MergedResearchResult 合并后的研究结果
type MergedResearchResult struct {
	Results    []BenchmarkResult
	BestIndex  int
	BestResult BenchmarkResult
}
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

// SearchSource 搜索源
type SearchSource string

const (
	SourceAuto     SearchSource = "auto"     // 自动选择最佳源
	SourceSearXNG  SearchSource = "searxng" // SearXNG 元搜索引擎
	SourceDuckDuck SearchSource = "duckduckgo"
	SourceBing     SearchSource = "bing"
)

// SearchResult 单条搜索结果
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Engine  string `json:"engine,omitempty"`
}

// WebSearchTool allows the agent to search the web for information.
// Supports multiple search engines: SearXNG, DuckDuckGo, Bing.
// Falls back gracefully when primary source is unavailable.
type WebSearchTool struct {
	defaultSource SearchSource
}

func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{
		defaultSource: SearchSource(os.Getenv("TIANCAN_SEARCH_SOURCE")),
	}
}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "搜索互联网获取最新信息。当你需要了解最新技术文档、查询API用法、搜索代码示例、查找错误解决方案、了解新版本特性时，必须使用此工具。参数：query=搜索内容，source=搜索引擎(可选，默认auto)，page=页码(可选)，per_page=每页数量(可选)，time_range=时间范围(可选day/week/month/year)"
}
func (t *WebSearchTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索查询",
			},
			"source": map[string]interface{}{
				"type": "string",
				"enum": []string{"auto", "searxng", "duckduckgo", "bing"},
				"description": "搜索源 (auto=自动选择, searxng=元搜索, duckduckgo=即时答案, bing=Bing搜索)",
			},
			"page": map[string]interface{}{
				"type":        "number",
				"description": "页码（从1开始，默认1）",
			},
			"per_page": map[string]interface{}{
				"type":        "number",
				"description": "每页结果数（默认10，最大50）",
			},
			"category": map[string]interface{}{
				"type": "string",
				"enum": []string{"general", "news", "science", "it", "files", "images", "videos"},
				"description": "搜索类别",
			},
			"safe": map[string]interface{}{
				"type":        "boolean",
				"description": "安全搜索（默认开启）",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "搜索语言代码（zh/en/ja等，默认auto）",
			},
			"time_range": map[string]interface{}{
				"type": "string",
				"enum": []string{"day", "week", "month", "year"},
				"description": "时间范围过滤",
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

	// 解析参数
	source := SearchSource(getStringArg(args, "source", string(t.defaultSource)))
	if source == "" || source == "auto" {
		source = "searxng" // 默认使用 SearXNG
	}
	page := getIntArg(args, "page", 1)
	perPage := getIntArg(args, "per_page", 10)
	if perPage > 50 {
		perPage = 50
	}
	category := getStringArg(args, "category", "")
	safe := getBoolArg(args, "safe", true)
	language := getStringArg(args, "language", "auto")
	timeRange := getStringArg(args, "time_range", "")

	var results []SearchResult
	var err error
	var usedEngine string

	// 根据源执行搜索
	switch source {
	case "searxng":
		results, err = t.searchSearXNG(ctx, query, page, perPage, category, safe, language, timeRange)
		usedEngine = "SearXNG"
	case "bing":
		results, err = t.searchBing(ctx, query, page, perPage, safe, language, timeRange)
		usedEngine = "Bing"
	case "duckduckgo":
		results, err = t.searchDuckDuckGoHTML(ctx, query, page, perPage, safe)
		usedEngine = "DuckDuckGo"
	default:
		// 尝试 SearXNG，失败则降级到其他源
		results, err = t.searchSearXNG(ctx, query, page, perPage, category, safe, language, timeRange)
		if err != nil {
			results, err = t.searchBing(ctx, query, page, perPage, safe, language, timeRange)
			usedEngine = "Bing"
		} else {
			usedEngine = "SearXNG"
		}
	}

	if err != nil {
		// 最终降级：尝试 DuckDuckGo 即时答案
		abstract, fallbackErr := t.searchDuckDuckGoInstant(query)
		if fallbackErr == nil && abstract != "" {
			return types.ToolResult{
				Content: fmt.Sprintf("📋 来自 DuckDuckGo 即时答案:\n\n%s\n\n💡 提示: 详细搜索结果不可用，可尝试设置 source='searxng'", abstract),
				Success: true,
			}, nil
		}
		return types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	// 格式化结果
	return t.formatResults(query, results, page, perPage, usedEngine)
}

// searchSearXNG 使用 SearXNG 元搜索引擎
func (t *WebSearchTool) searchSearXNG(ctx context.Context, query string, page, perPage int, category string, safe bool, language, timeRange string) ([]SearchResult, error) {
	baseURL := os.Getenv("TIANCAN_SEARXNG_URL")
	if baseURL == "" {
		baseURL = "https://searx.be"
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("pageno", strconv.Itoa(page))
	params.Set("engines", "google,bing,duckduckgo") // 指定引擎
	params.Set("tokens", "20")                      // 结果数

	if category != "" {
		params.Set("categories", category)
	}
	if safe {
		params.Set("safesearch", "1")
	}
	if language != "" && language != "auto" {
		params.Set("language", language)
	}
	if timeRange != "" {
		params.Set("time_range", timeRange)
	}

	fullURL := fmt.Sprintf("%s/search?%s", baseURL, params.Encode())

	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(ctx, map[string]interface{}{"url": fullURL})
	if err != nil {
		return nil, fmt.Errorf("SearXNG 请求失败: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &resp); err != nil {
		return nil, fmt.Errorf("SearXNG 响应解析失败: %v", err)
	}

	results, ok := resp["results"].([]interface{})
	if !ok || len(results) == 0 {
		return nil, fmt.Errorf("SearXNG 返回空结果")
	}

	var searchResults []SearchResult
	for _, r := range results {
		item, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		result := SearchResult{
			Title:   getString(item, "title"),
			URL:     getString(item, "url"),
			Snippet: getString(item, "content"),
			Engine:  getString(item, "engine"),
		}
		if result.URL != "" {
			searchResults = append(searchResults, result)
		}
		if len(searchResults) >= perPage {
			break
		}
	}

	return searchResults, nil
}

// searchBing 使用 Bing 搜索 API（网页抓取）
func (t *WebSearchTool) searchBing(ctx context.Context, query string, page, perPage int, safe bool, language, timeRange string) ([]SearchResult, error) {
	// Bing 的非官方 API（用于演示，实际生产应使用官方 API）
	// 这里使用 html scraping 方式
	safeParam := "on"
	if !safe {
		safeParam = "off"
	}

	offset := (page - 1) * perPage
	url := fmt.Sprintf("https://www.bing.com/search?q=%s&first=%d&safeSearch=%s",
		url.QueryEscape(query), offset+1, safeParam)

	if language != "" && language != "auto" {
		url += "&setlang=" + language
	}

	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(ctx, map[string]interface{}{"url": url})
	if err != nil {
		return nil, fmt.Errorf("Bing 请求失败: %v", err)
	}

	// 解析 Bing 搜索结果
	return parseBingHTML(result.Content), nil
}

// searchDuckDuckGoHTML 使用 DuckDuckGo HTML 搜索
func (t *WebSearchTool) searchDuckDuckGoHTML(ctx context.Context, query string, page, perPage int, safe bool) ([]SearchResult, error) {
	offset := (page - 1) * perPage
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s&s=%d", url.QueryEscape(query), offset)

	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(ctx, map[string]interface{}{"url": searchURL})
	if err != nil {
		return nil, fmt.Errorf("DuckDuckGo 请求失败: %v", err)
	}

	return parseDuckDuckGoHTML(result.Content), nil
}

// searchDuckDuckGoInstant 获取 DuckDuckGo 即时答案
func (t *WebSearchTool) searchDuckDuckGoInstant(query string) (string, error) {
	fetchTool := &WebFetchTool{}
	result, err := fetchTool.Execute(context.Background(), map[string]interface{}{
		"url": fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1", url.QueryEscape(query)),
	})
	if err != nil {
		return "", err
	}

	var ddg map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &ddg); err != nil {
		return "", err
	}

	if abstract, ok := ddg["AbstractText"].(string); ok && abstract != "" {
		return abstract, nil
	}

	return "", fmt.Errorf("无即时答案")
}

// formatResults 格式化搜索结果
func (t *WebSearchTool) formatResults(query string, results []SearchResult, page, perPage int, engine string) (types.ToolResult, error) {
	if len(results) == 0 {
		return types.ToolResult{Content: "未找到相关结果", Success: true}, nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔍 搜索结果: %s\n", query))
	b.WriteString(fmt.Sprintf("📊 引擎: %s | 第 %d 页\n", engine, page))
	b.WriteString(fmt.Sprintf("📄 结果数: %d\n\n", len(results)))
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	for i, r := range results {
		b.WriteString(fmt.Sprintf("【%d】%s\n", page*perPage-perPage+i+1, r.Title))
		b.WriteString(fmt.Sprintf("🔗 %s\n", r.URL))
		if r.Snippet != "" {
			// 清理 HTML 实体
			snippet := cleanHTMLEntities(r.Snippet)
			b.WriteString(fmt.Sprintf("📝 %s\n", snippet))
		}
		if r.Engine != "" && r.Engine != engine {
			b.WriteString(fmt.Sprintf("🏷 来源: %s\n", r.Engine))
		}
		b.WriteString("\n")
	}

	// 添加导航提示
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	if page > 1 {
		b.WriteString(fmt.Sprintf("⬅️ 上一页: page=%d\n", page-1))
	}
	b.WriteString(fmt.Sprintf("➡️ 下一页: page=%d\n", page+1))
	b.WriteString("💡 使用 source 参数切换搜索源: searxng/bing/duckduckgo\n")

	return types.ToolResult{Content: b.String(), Success: true}, nil
}

// parseBingHTML 解析 Bing 搜索结果 HTML
func parseBingHTML(html string) []SearchResult {
	var results []SearchResult

	// 简单的正则匹配
	titleRe := regexp.MustCompile(`<h2[^>]*><a[^>]*href="([^"]*)"[^>]*>([^<]*)</a></h2>`)
	snippetRe := regexp.MustCompile(`<p class="b_paractraph"[^>]*><a href="[^"]*"[^>]*>([^<]*)</a></p>`)

	titleMatches := titleRe.FindAllStringSubmatch(html, -1)
	snippetMatches := snippetRe.FindAllStringSubmatch(html, -1)

	for i, match := range titleMatches {
		if len(match) >= 3 {
			result := SearchResult{
				URL:   match[1],
				Title: cleanHTMLEntities(match[2]),
			}
			if i < len(snippetMatches) && len(snippetMatches[i]) >= 2 {
				result.Snippet = cleanHTMLEntities(snippetMatches[i][1])
			}
			results = append(results, result)
		}
	}

	return results
}

// parseDuckDuckGoHTML 解析 DuckDuckGo HTML 结果
func parseDuckDuckGoHTML(html string) []SearchResult {
	var results []SearchResult

	// 匹配 DuckDuckGo 结果格式
	resultRe := regexp.MustCompile(`<a class="result__a" href="([^"]*)">([^<]*)</a>`)
	snippetRe := regexp.MustCompile(`<a class="result__snippet"[^>]*>([^<]*)</a>`)

	titleMatches := resultRe.FindAllStringSubmatch(html, -1)
	snippetMatches := snippetRe.FindAllStringSubmatch(html, -1)

	for i, match := range titleMatches {
		if len(match) >= 3 {
			result := SearchResult{
				URL:   match[1],
				Title: cleanHTMLEntities(match[2]),
			}
			if i < len(snippetMatches) && len(snippetMatches[i]) >= 2 {
				result.Snippet = cleanHTMLEntities(snippetMatches[i][1])
			}
			results = append(results, result)
		}
	}

	return results
}

// cleanHTMLEntities 清理 HTML 实体
func cleanHTMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "<[^>]+>", "") // 移除剩余 HTML 标签
	s = strings.TrimSpace(s)
	return s
}

// 辅助函数
func getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

func getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return defaultVal
}

func getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return defaultVal
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
