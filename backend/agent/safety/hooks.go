package safety

import (
	"context"
	"fmt"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// HookEventType defines when a hook fires.
type HookEventType string

const (
	HookPreToolUse  HookEventType = "PreToolUse"
	HookPostToolUse HookEventType = "PostToolUse"
)

// HookResult captures the outcome of a single hook execution.
type HookResult struct {
	EventType          HookEventType           `json:"eventType"`
	ToolName           string                  `json:"toolName"`
	PermissionBehavior types.PermissionDecision `json:"permissionBehavior,omitempty"`
	UpdatedInput       map[string]interface{}   `json:"updatedInput,omitempty"`
	BlockingError      string                  `json:"blockingError,omitempty"`
	PreventContinuation bool                   `json:"preventContinuation,omitempty"`
	StopReason         string                  `json:"stopReason,omitempty"`
	AdditionalContext  string                  `json:"additionalContext,omitempty"`
	HookPermissionReason string                `json:"hookPermissionReason,omitempty"`
	HookSource         string                  `json:"hookSource,omitempty"`
	DurationMs         int64                   `json:"durationMs"`
}

// HookFunc is the signature for a hook callback.
type HookFunc func(ctx context.Context, eventType HookEventType, toolName string, toolUseID string, input map[string]interface{}) HookResult

// HookEntry wraps a HookFunc with metadata.
type HookEntry struct {
	Name      string
	EventType HookEventType
	ToolName  string // "*" for all tools
	Fn        HookFunc
	Priority  int // lower = runs first
}

// HookPipelineResult aggregates results from running all hooks.
type HookPipelineResult struct {
	HookResults               []HookResult
	FinalDecision             types.PermissionDecision
	FinalReason               string
	DecisionSource            string
	Input                     map[string]interface{}
	InputModified              bool
	ShouldPreventContinuation  bool
	StopReason                string
	AdditionalContexts        []string
	Stopped                   bool
}

// RunPreToolHooks executes all PreToolUse hooks for the given tool.
// Mirrors Claude Code's runPreToolUseHooks().
func (s *SafetySystem) RunPreToolHooks(ctx context.Context, tool types.Tool, toolUseID string, input map[string]interface{}) HookPipelineResult {
	s.hooksMu.RLock()
	var hooks []HookEntry
	for _, h := range s.hooks {
		if h.EventType == HookPreToolUse && (h.ToolName == "*" || h.ToolName == tool.Name()) {
			hooks = append(hooks, h)
		}
	}
	s.hooksMu.RUnlock()

	result := HookPipelineResult{Input: input}

	for _, hook := range hooks {
		start := time.Now()
		hr := hook.Fn(ctx, HookPreToolUse, tool.Name(), toolUseID, result.Input)
		hr.DurationMs = time.Since(start).Milliseconds()
		result.HookResults = append(result.HookResults, hr)

		if hr.BlockingError != "" {
			result.FinalDecision = types.DecisionDeny
			result.FinalReason = getPreToolHookBlockingMessage(
				fmt.Sprintf("PreToolUse:%s", tool.Name()), hr.BlockingError)
			result.Stopped = true
			return result
		}

		if hr.PreventContinuation {
			result.ShouldPreventContinuation = true
			if hr.StopReason != "" {
				result.StopReason = hr.StopReason
			}
		}

		if hr.PermissionBehavior != "" {
			result.FinalDecision = hr.PermissionBehavior
			result.FinalReason = hr.HookPermissionReason
			result.DecisionSource = fmt.Sprintf("hook:PreToolUse:%s", hook.Name)
			if hr.PermissionBehavior == types.DecisionDeny {
				result.Stopped = true
				return result
			}
		}

		if hr.UpdatedInput != nil {
			result.Input = hr.UpdatedInput
			result.InputModified = true
		}

		if hr.AdditionalContext != "" {
			result.AdditionalContexts = append(result.AdditionalContexts, hr.AdditionalContext)
		}

		if ctx.Err() != nil {
			result.Stopped = true
			return result
		}
	}

	return result
}

// RunPostToolHooks executes all PostToolUse hooks for the given tool.
func (s *SafetySystem) RunPostToolHooks(ctx context.Context, tool types.Tool, toolUseID string, result types.ToolResult) []HookResult {
	s.hooksMu.RLock()
	var hooks []HookEntry
	for _, h := range s.hooks {
		if h.EventType == HookPostToolUse && (h.ToolName == "*" || h.ToolName == tool.Name()) {
			hooks = append(hooks, h)
		}
	}
	s.hooksMu.RUnlock()

	var results []HookResult
	for _, hook := range hooks {
		start := time.Now()
		input := map[string]interface{}{
			"content": result.Content,
			"success": result.Success,
			"error":   result.Error,
		}
		hr := hook.Fn(ctx, HookPostToolUse, tool.Name(), toolUseID, input)
		hr.DurationMs = time.Since(start).Milliseconds()
		results = append(results, hr)
		if ctx.Err() != nil {
			break
		}
	}
	return results
}

// RegisterHook adds a hook to the safety system.
func (s *SafetySystem) RegisterHook(entry HookEntry) {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	s.hooks = append(s.hooks, entry)
	for i := len(s.hooks) - 1; i > 0; i-- {
		if s.hooks[i].Priority < s.hooks[i-1].Priority {
			s.hooks[i], s.hooks[i-1] = s.hooks[i-1], s.hooks[i]
		}
	}
}

// ClearHooks removes all registered hooks.
func (s *SafetySystem) ClearHooks() {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	s.hooks = nil
}

func getPreToolHookBlockingMessage(hookName, blockingError string) string {
	return fmt.Sprintf("Hook %s blocked execution: %s", hookName, blockingError)
}
