package ralph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/safety"
	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
	webparser "github.com/rocky233/tiancan-ai-ide/backend/webai/parser"
	webtypes "github.com/rocky233/tiancan-ai-ide/backend/webai/types"
)

const (
	// MaxIterations is the maximum number of fix-verify cycles.
	MaxIterations = 3
	// MaxFixesPerIteration limits fixes per cycle to prevent infinite loops.
	MaxFixesPerIteration = 5
)

// RalphLoop implements the self-correction loop (Think → Act → Verify → Fix).
// Named after the "Ralph" pattern from Claude Code: an iterative
// implement-verify-fix cycle that continues until verification passes
// or max iterations are reached.
// ToolNameValidator checks tool names dynamically — replaces hardcoded tool lists.
type ToolNameValidator interface {
	IsValid(toolName string) bool
}

type RalphLoop struct {
	chatFn        ChatFunc
	toolExec      ToolExecFunc
	maxIter       int
	enabled       bool
	safetySys     *safety.SafetySystem
	toolValidator ToolNameValidator // dynamic tool name validation
}

// ChatFunc sends a prompt to the AI and returns the response.
type ChatFunc func(systemPrompt string, messages []types.Message) (string, error)

// ToolExecFunc executes a tool and returns the result.
type ToolExecFunc func(ctx context.Context, toolName string, args map[string]interface{}) (types.ToolResult, error)

// NewRalphLoop creates a new Ralph Loop instance.
func NewRalphLoop(chatFn ChatFunc, toolExec ToolExecFunc) *RalphLoop {
	enabled := os.Getenv("TIANCAN_DISABLE_RALPH") != "1"
	maxIter := MaxIterations
	if v := os.Getenv("TIANCAN_RALPH_MAX_ITER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxIter = n
		}
	}
	return &RalphLoop{
		chatFn:        chatFn,
		toolExec:      toolExec,
		maxIter:       maxIter,
		enabled:       enabled,
		safetySys:     safety.NewSafetySystem(),
		toolValidator: defaultToolValidator{},
	}
}

// SetToolValidator sets the dynamic tool name validator.
func (r *RalphLoop) SetToolValidator(v ToolNameValidator) {
	r.toolValidator = v
}

// defaultToolValidator accepts any non-empty tool name as valid.
// Real validation should be injected via SetToolValidator using the tool registry.
type defaultToolValidator struct{}

func (d defaultToolValidator) IsValid(toolName string) bool {
	return toolName != "" && !strings.HasPrefix(toolName, "/") && !strings.Contains(toolName, " ")
}

// SetSafetySystem sets the safety system for the Ralph Loop.
func (r *RalphLoop) SetSafetySystem(sys *safety.SafetySystem) {
	r.safetySys = sys
}

// IsEnabled returns whether the Ralph Loop is enabled.
func (r *RalphLoop) IsEnabled() bool {
	return r.enabled
}

// Run executes the Ralph Loop: implement → verify → fix cycle.
// The loop continues until verification passes or max iterations are reached.
func (r *RalphLoop) Run(ctx context.Context, task string, agentDef types.AgentDefinition, rootPath string) (*types.RalphLoopResult, error) {
	if !r.enabled {
		// Run single pass without verification
		return r.singlePass(ctx, task, agentDef, rootPath)
	}

	var allMessages []types.Message
	iterations := 0
	fixesApplied := 0
	verdict := types.VerdictFail

	for iterations < r.maxIter {
		iterations++
		select {
		case <-ctx.Done():
			return &types.RalphLoopResult{
				Verdict:      types.VerdictPartial,
				Iterations:   iterations,
				FixesApplied: fixesApplied,
				Report:       "Ralph Loop cancelled",
				Messages:     allMessages,
			}, ctx.Err()
		default:
		}

		// Phase 1: Implement (or fix based on previous verification)
		var implementPrompt string
		if iterations == 1 {
			implementPrompt = task
		} else {
			implementPrompt = fmt.Sprintf(
				"Fix the following issues found during verification:\n\n%s\n\nOriginal task: %s",
				lastVerificationReport(allMessages), task,
			)
		}

		implMessages, err := r.runAgentTurn(ctx, implementPrompt, agentDef, rootPath)
		if err != nil {
			return &types.RalphLoopResult{
				Verdict:      types.VerdictFail,
				Iterations:   iterations,
				FixesApplied: fixesApplied,
				Report:       fmt.Sprintf("Implementation error: %v", err),
				Messages:     allMessages,
			}, err
		}
		allMessages = append(allMessages, implMessages...)
		if iterations > 1 {
			fixesApplied++
		}

		// Phase 2: Verify
		verifyPrompt := fmt.Sprintf(
			"Verify the following implementation:\n\nTask: %s\n\nApproach taken: %s\n\nFiles changed: review the recent tool calls above.",
			task, summarizeActions(implMessages),
		)

		verifyMessages, err := r.runVerifyTurn(ctx, verifyPrompt, rootPath)
		if err != nil {
			// Verification failure is not fatal - continue loop
			allMessages = append(allMessages, types.Message{
				Role:      types.RoleSystem,
				Content:   fmt.Sprintf("Verification error: %v", err),
				Timestamp: time.Now(),
			})
			continue
		}
		allMessages = append(allMessages, verifyMessages...)

		// Phase 3: Check verdict
		verdict = extractVerdict(verifyMessages)
		if verdict == types.VerdictPass {
			break
		}

		// If PARTIAL, one more attempt
		if verdict == types.VerdictPartial && iterations >= r.maxIter-1 {
			break
		}
	}

	report := buildReport(allMessages, verdict, iterations, fixesApplied)

	return &types.RalphLoopResult{
		Verdict:      verdict,
		Iterations:   iterations,
		FixesApplied: fixesApplied,
		Report:       report,
		Messages:     allMessages,
	}, nil
}

// singlePass runs a single implementation pass without verification.
func (r *RalphLoop) singlePass(ctx context.Context, task string, agentDef types.AgentDefinition, rootPath string) (*types.RalphLoopResult, error) {
	messages, err := r.runAgentTurn(ctx, task, agentDef, rootPath)
	if err != nil {
		return &types.RalphLoopResult{
			Verdict:  types.VerdictFail,
			Report:   fmt.Sprintf("Implementation error: %v", err),
			Messages: messages,
		}, err
	}

	return &types.RalphLoopResult{
		Verdict:    types.VerdictPass,
		Iterations: 1,
		Report:     "Completed (no verification - Ralph Loop disabled)",
		Messages:   messages,
	}, nil
}

// runAgentTurn runs a single agent turn (implementation or fix).
func (r *RalphLoop) runAgentTurn(ctx context.Context, prompt string, agentDef types.AgentDefinition, rootPath string) ([]types.Message, error) {
	var messages []types.Message

	// Build system prompt
	systemPrompt := agentDef.SystemPrompt

	// Send user message
	messages = append(messages, types.Message{
		Role:      types.RoleUser,
		Content:   prompt,
		Timestamp: time.Now(),
	})

	// Get AI response
	response, err := r.chatFn(systemPrompt, messages)
	if err != nil {
		return messages, err
	}

	assistantMsg := types.Message{
		Role:      types.RoleAssistant,
		Content:   response,
		Timestamp: time.Now(),
	}
	messages = append(messages, assistantMsg)

	// Parse tool calls using enhanced parser (mirrors Claude Code's ParseToolCalls)
	toolCalls := ParseToolCallsEnhanced(response)
	for _, tc := range toolCalls {
		// Safety check: run PreTool hooks
		if r.safetySys != nil {
			hookResult := r.safetySys.RunPreToolHooks(ctx, &simpleTool{name: tc.Name}, tc.Name, tc.Args)
			if hookResult.Stopped && hookResult.FinalDecision == types.DecisionDeny {
				messages = append(messages, types.Message{
					Role:      types.RoleTool,
					Content:   fmt.Sprintf("Blocked by safety: %s", hookResult.FinalReason),
					ToolName:  tc.Name,
					ToolUseID: tc.Name,
					Timestamp: time.Now(),
				})
				continue
			}
			// Use modified input if hooks changed it
			if hookResult.InputModified {
				tc.Args = hookResult.Input
			}
		}

		result, err := r.toolExec(ctx, tc.Name, tc.Args)
		if err != nil {
			result = types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}

		// Safety check: run PostTool hooks
		if r.safetySys != nil {
			r.safetySys.RunPostToolHooks(ctx, &simpleTool{name: tc.Name}, tc.Name, result)
		}

		messages = append(messages, types.Message{
			Role:      types.RoleTool,
			Content:   result.Content,
			ToolName:  tc.Name,
			ToolUseID: tc.Name,
			Timestamp: time.Now(),
		})
	}

	return messages, nil
}

// runVerifyTurn runs a verification turn using the verifier agent.
func (r *RalphLoop) runVerifyTurn(ctx context.Context, prompt string, rootPath string) ([]types.Message, error) {
	verifyDef := types.AgentDefinition{
		AgentType:       types.RoleVerifier,
		SystemPrompt:    verifierSystemPrompt,
		MaxTurns:        15,
		DisallowedTools: []string{"file_write", "file_edit"},
		PermissionMode:  "auto",
	}
	return r.runAgentTurn(ctx, prompt, verifyDef, rootPath)
}

// simpleTool implements types.Tool for hook integration.
type simpleTool struct {
	name string
}

func (s *simpleTool) Name() string                        { return s.name }
func (s *simpleTool) Description() string                 { return "" }
func (s *simpleTool) InputSchema() map[string]interface{} { return nil }
func (s *simpleTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	return types.ToolResult{}, nil
}
func (s *simpleTool) IsReadOnly() bool        { return false }
func (s *simpleTool) IsConcurrencySafe() bool { return true }

// ParseToolCallsEnhanced delegates to the webai/parser which handles all
// tool call formats (XML, JSON, emoji, Anthropic, DeepSeek, etc).
// No hardcoded regex or tool name lists — the parser is format-aware, not name-aware.
func ParseToolCallsEnhanced(response string) []types.ToolCall {
	webCalls := webparser.ParseToolCalls(response)
	var calls []types.ToolCall
	for _, wc := range webCalls {
		args := make(map[string]interface{})
		if wc.Arguments != nil {
			json.Unmarshal(wc.Arguments, &args)
		}
		calls = append(calls, types.ToolCall{
			Name: wc.Name,
			Args: args,
		})
	}
	return calls
}

// webToolCallBlock is an alias for webtypes.ToolCallBlock to avoid import in tests.
type webToolCallBlock = webtypes.ToolCallBlock

// extractVerdict looks for VERDICT: PASS/FAIL/PARTIAL in messages.
func extractVerdict(messages []types.Message) types.RalphVerdict {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		content := strings.ToUpper(msg.Content)
		if strings.Contains(content, "VERDICT: PASS") {
			return types.VerdictPass
		}
		if strings.Contains(content, "VERDICT: FAIL") {
			return types.VerdictFail
		}
		if strings.Contains(content, "VERDICT: PARTIAL") {
			return types.VerdictPartial
		}
	}
	return types.VerdictFail
}

// lastVerificationReport extracts the last verification report from messages.
func lastVerificationReport(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleAssistant {
			return messages[i].Content
		}
	}
	return "No verification report found"
}

// summarizeActions creates a brief summary of actions taken.
func summarizeActions(messages []types.Message) string {
	var actions []string
	for _, m := range messages {
		if m.Role == types.RoleTool && m.ToolName != "" {
			actions = append(actions, fmt.Sprintf("- %s: %s", m.ToolName, truncate(m.Content, 100)))
		}
	}
	if len(actions) == 0 {
		return "No tool actions recorded"
	}
	return strings.Join(actions, "\n")
}

// buildReport creates the final Ralph Loop report.
func buildReport(messages []types.Message, verdict types.RalphVerdict, iterations int, fixes int) string {
	var sb strings.Builder
	sb.WriteString("Ralph Loop Report\n")
	sb.WriteString("Verdict: " + string(verdict) + "\n")
	sb.WriteString(fmt.Sprintf("Iterations: %d\n", iterations))
	sb.WriteString(fmt.Sprintf("Fixes applied: %d\n", fixes))
	sb.WriteString("\n")
	sb.WriteString(summarizeActions(messages))
	return sb.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

const verifierSystemPrompt = `You are a verification specialist. Your job is not to confirm the implementation works — it's to try to break it.

=== CRITICAL: DO NOT MODIFY THE PROJECT ===
You are STRICTLY PROHIBITED from creating, modifying, or deleting any files in the project directory.

=== VERIFICATION STRATEGY ===
1. Run the build (if applicable). A broken build is an automatic FAIL.
2. Run the project's test suite. Failing tests are an automatic FAIL.
3. **Generate tests**: Use the <generate_tests> tool to create unit tests for changed files, then review the results.
4. **Assess completion**: Use the <assess_completion> tool to get a structured completion score.
5. Run linters/type-checkers if configured.
6. Check for regressions.

=== AUTO-VERIFICATION WORKFLOW ===
After each implementation, you MUST:
1. Identify which source files were changed.
2. For each changed file, call generate_tests with:
   - file_path: the changed source file
   - test_content: appropriate unit tests (you MUST write the test code)
   - run: true
3. After all tests are generated and run, call assess_completion with:
   - task_description: the original task
   - files_changed: list of changed files
   - check_build: true
   - check_tests: true
   - check_lint: true

=== OUTPUT FORMAT ===
Every check MUST follow this structure:
### Check: [what you're verifying]
**Command run:** [exact command]
**Output observed:** [actual output]
**Result: PASS** (or FAIL)

End with exactly:
VERDICT: PASS
or
VERDICT: FAIL
or
VERDICT: PARTIAL`
