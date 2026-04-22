package ralph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// FileSnapshot captures file state for rollback.
type FileSnapshot struct {
	Path    string    `json:"path"`
	Content string    `json:"content"`
	Exists  bool      `json:"exists"`
	ModTime time.Time `json:"modTime"`
}

// RollbackManager tracks file changes and supports reverting them.
type RollbackManager struct {
	mu        sync.Mutex
	snapshots map[string][]FileSnapshot
	rootPath  string
	enabled   bool
}

func NewRollbackManager(rootPath string) *RollbackManager {
	return &RollbackManager{
		snapshots: make(map[string][]FileSnapshot),
		rootPath:  rootPath,
		enabled:   os.Getenv("TIANCAN_DISABLE_ROLLBACK") != "1",
	}
}

func (rm *RollbackManager) Snapshot(path string) error {
	if !rm.enabled {
		return nil
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(rm.rootPath, path)
	}
	snap := FileSnapshot{Path: absPath, ModTime: time.Now()}
	data, err := os.ReadFile(absPath)
	if err != nil {
		snap.Exists = false
	} else {
		snap.Exists = true
		snap.Content = string(data)
	}
	rm.snapshots[absPath] = append(rm.snapshots[absPath], snap)
	return nil
}

func (rm *RollbackManager) Rollback() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	var errs []string
	for path, stack := range rm.snapshots {
		if len(stack) == 0 {
			continue
		}
		snap := stack[len(stack)-1]
		rm.snapshots[path] = stack[:len(stack)-1]
		if !snap.Exists {
			os.Remove(path)
		} else {
			os.MkdirAll(filepath.Dir(path), 0755)
			if err := os.WriteFile(path, []byte(snap.Content), 0644); err != nil {
				errs = append(errs, fmt.Sprintf("write %s: %v", path, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (rm *RollbackManager) ClearSnapshots() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.snapshots = make(map[string][]FileSnapshot)
}

// RalphConfig holds Ralph Loop V2 configuration.
type RalphConfig struct {
	MaxIterations      int  `json:"maxIterations"`
	EnableRollback     bool `json:"enableRollback"`
	EnableVerification bool `json:"enableVerification"`
	AutoRollbackOnFail bool `json:"autoRollbackOnFail"`
}

func DefaultRalphConfig() RalphConfig {
	return RalphConfig{
		MaxIterations: 3, EnableRollback: true,
		EnableVerification: true, AutoRollbackOnFail: false,
	}
}

// RalphLoopV2 is an enhanced self-correction loop with rollback support.
type RalphLoopV2 struct {
	chatFn   ChatFunc
	toolExec ToolExecFunc
	config   RalphConfig
	rollback *RollbackManager
	rootPath string
}

// NewRalphLoopV2 creates an enhanced Ralph Loop with rollback.
func NewRalphLoopV2(chatFn ChatFunc, toolExec ToolExecFunc, rootPath string, cfg RalphConfig) *RalphLoopV2 {
	return &RalphLoopV2{
		chatFn:   chatFn,
		toolExec: toolExec,
		config:   cfg,
		rollback: NewRollbackManager(rootPath),
		rootPath: rootPath,
	}
}

// Run executes the enhanced Ralph Loop: implement → verify → fix with rollback.
func (r *RalphLoopV2) Run(ctx context.Context, task string, agentDef types.AgentDefinition) (*types.RalphLoopResult, error) {
	var allMessages []types.Message
	iterations := 0
	fixesApplied := 0
	verdict := types.VerdictFail

	for iterations < r.config.MaxIterations {
		iterations++
		select {
		case <-ctx.Done():
			return &types.RalphLoopResult{
				Verdict: types.VerdictPartial, Iterations: iterations,
				FixesApplied: fixesApplied, Messages: allMessages,
			}, ctx.Err()
		default:
		}

		if r.config.EnableRollback {
			r.rollback.ClearSnapshots()
		}

		// Phase 1: Implement or Fix
		prompt := task
		if iterations > 1 {
			prompt = fmt.Sprintf("Fix issues:\n%s\n\nOriginal: %s",
				lastVerificationReport(allMessages), task)
		}

		implMsgs, err := r.runAgentTurn(ctx, prompt, agentDef)
		if err != nil {
			if r.config.AutoRollbackOnFail {
				r.rollback.Rollback()
			}
			return &types.RalphLoopResult{
				Verdict: types.VerdictFail, Iterations: iterations,
				FixesApplied: fixesApplied, Report: fmt.Sprintf("Error: %v", err),
				Messages: allMessages,
			}, err
		}
		allMessages = append(allMessages, implMsgs...)
		if iterations > 1 {
			fixesApplied++
		}

		// Phase 2: Verify
		if !r.config.EnableVerification {
			verdict = types.VerdictPass
			break
		}

		verifyMsgs, err := r.runVerifyTurn(ctx, fmt.Sprintf("Verify: %s\n%s",
			task, summarizeActions(implMsgs)))
		if err != nil {
			allMessages = append(allMessages, types.Message{
				Role: types.RoleSystem, Content: fmt.Sprintf("Verify error: %v", err),
				Timestamp: time.Now(),
			})
			continue
		}
		allMessages = append(allMessages, verifyMsgs...)

		// Phase 3: Check verdict
		verdict = extractVerdict(verifyMsgs)
		if verdict == types.VerdictPass {
			break
		}
		if verdict == types.VerdictFail && r.config.AutoRollbackOnFail {
			r.rollback.Rollback()
		}
	}

	return &types.RalphLoopResult{
		Verdict: verdict, Iterations: iterations, FixesApplied: fixesApplied,
		Report:   buildReport(allMessages, verdict, iterations, fixesApplied),
		Messages: allMessages,
	}, nil
}

// runAgentTurn runs a single agent turn using the existing RalphLoop helpers.
func (r *RalphLoopV2) runAgentTurn(ctx context.Context, prompt string, agentDef types.AgentDefinition) ([]types.Message, error) {
	var messages []types.Message
	messages = append(messages, types.Message{
		Role: types.RoleUser, Content: prompt, Timestamp: time.Now(),
	})
	response, err := r.chatFn(agentDef.SystemPrompt, messages)
	if err != nil {
		return messages, err
	}
	assistantMsg := types.Message{Role: types.RoleAssistant, Content: response, Timestamp: time.Now()}
	messages = append(messages, assistantMsg)

	// Parse and execute tool calls
	toolCalls := ParseToolCallsEnhanced(response)
	for _, tc := range toolCalls {
		result, err := r.toolExec(ctx, tc.Name, tc.Args)
		if err != nil {
			result = types.ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}
		messages = append(messages, types.Message{
			Role: types.RoleTool, Content: result.Content,
			ToolName: tc.Name, ToolUseID: tc.Name, Timestamp: time.Now(),
		})
	}
	return messages, nil
}

// runVerifyTurn runs a verification turn.
func (r *RalphLoopV2) runVerifyTurn(ctx context.Context, prompt string) ([]types.Message, error) {
	verifyDef := types.AgentDefinition{
		AgentType:       types.RoleVerifier,
		SystemPrompt:    verifierSystemPrompt,
		MaxTurns:        15,
		DisallowedTools: []string{"file_write", "file_edit"},
		PermissionMode:  "auto",
	}
	return r.runAgentTurn(ctx, prompt, verifyDef)
}
