package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/approval"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/clarify"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/compact"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/config"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/goal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/mcp"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/memory"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/parallel"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/preset"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/ralph"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/roles"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/safety"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/toolguard"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/tools"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
	"github.com/linlurui/tiancan-ai-ide/backend/skills"
	webparser "github.com/linlurui/tiancan-ai-ide/backend/webai/parser"
)

// AgentRunResult holds the result of an agent run.
type AgentRunResult struct {
	Done        bool             `json:"done"`
	Error       string           `json:"error,omitempty"`
	Pending     bool             `json:"pending,omitempty"` // G9: clarification awaiting user (not an error)
	Messages    []Message        `json:"messages"`
	AgentType   string           `json:"agentType,omitempty"`
	TotalTokens int              `json:"totalTokens"`
	DurationMs  int64            `json:"durationMs"`
	RalphResult *RalphLoopResult `json:"ralphResult,omitempty"`
}

// StreamEvent represents a streaming event from the agent loop.
type StreamEvent struct {
	Type    string `json:"type"`           // "token", "tool_call", "tool_result", "thinking", "done", "error", "compact"
	Content string `json:"content"`        // content depends on type
	Name    string `json:"name,omitempty"` // tool name for tool_call/tool_result
	Success bool   `json:"success,omitempty"`
}

// StreamChatFunc is the streaming variant of ChatFunc.
// It calls onToken for each token and returns the full accumulated response.
type StreamChatFunc func(systemPrompt string, messages []Message, onToken func(token string)) (string, error)

// SkillMatcher is the minimal skill-subsystem surface the agent needs.
// The production implementation is *skills.Manager (in-memory registry loaded
// from the filesystem — the "FSProvider"). It is an interface so tests can
// inject a fake without touching disk (G5).
type SkillMatcher interface {
	MatchTrigger(input string) []*skills.Skill
}

// ProgressCallback is called by the agent loop to report iteration progress.
type ProgressCallback func(iteration int, toolName string, toolArgs map[string]interface{}, output string, isDone bool, err string)

// Agent is the main orchestrator that wires all subsystems together.
type Agent struct {
	rootPath     string
	homeDir      string
	registry     *tools.Registry
	safetySys    *safety.SafetySystem
	memorySys    *memory.MemorySystem
	compactSvc   *compact.CompactService
	mcpClient    *mcp.Client
	ralphLoop    *ralph.RalphLoop
	parallelExec *parallel.Executor
	rollbackMgr  *ralph.RollbackManager
	skillMatcher SkillMatcher
	toolGuard    *toolguard.Guard
	dupDetector  *toolguard.Detector
	approvalSys  *approval.ApprovalSystem

	// G9: intent-clarification state machine + clarifier.
	clarifier         clarify.Clarifier
	clarifyMachine    *clarify.Machine
	clarifyTimeoutSec int
	clarifyMaxRounds  int

	// G10: permission preset manager — owns the active {sandbox, approval}
	// tuple and applies it to the G4/G8 subsystems via the applier seams below.
	presetMgr   *preset.Manager
	sandboxMode preset.SandboxMode

	// G12: goal-driven + plan-mode state machine. Holds the active goal (with a
	// CAS revision, round budget, and plan-mode flag). Nil-safe: the Run loops
	// only touch it when non-nil, so callers that never set a goal are unaffected.
	goalStore *goal.Store

	// G14/G15: session event log + live projection. Every model turn and tool step
	// is appended to journalLog and folded (in place) into sessionProj. The log is
	// the single source of truth; state is its pure fold (G14), and G15 emits the
	// turn/step events into it.
	journalLog  *journal.Log
	sessionProj *journal.Projector

	mu          sync.Mutex
	messages    []Message
	configFiles []config.ConfigFile
	progressCB  ProgressCallback
}

// NewAgent creates a new agent with all subsystems initialized.
func NewAgent(rootPath string) *Agent {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = "/tmp"
	}

	registry := tools.NewRegistry(rootPath)
	safetySys := safety.NewSafetySystem()
	memorySys := memory.NewMemorySystem(homeDir)
	compactSvc := compact.NewCompactService()
	mcpClient := mcp.NewClient()
	rollbackMgr := ralph.NewRollbackManager(rootPath)

	// G5: skill registry + FSProvider — load skills from disk so the main loop
	// can inject matched skill guidance as a pre-step user message.
	skillMgr := skills.NewManager(rootPath)
	_ = skillMgr.LoadAll()

	a := &Agent{
		rootPath:          rootPath,
		homeDir:           homeDir,
		registry:          registry,
		safetySys:         safetySys,
		memorySys:         memorySys,
		compactSvc:        compactSvc,
		mcpClient:         mcpClient,
		parallelExec:      parallel.NewExecutor(4),
		rollbackMgr:       rollbackMgr,
		skillMatcher:      skillMgr,
		toolGuard:         toolguard.New(toolguard.DefaultConfig()),
		dupDetector:       toolguard.NewDetector(0, 0),
		approvalSys:       approval.NewDefault(),
		clarifier:         clarify.NewDefault(),
		clarifyMachine:    clarify.NewMachine(),
		clarifyTimeoutSec: clarify.DefaultTimeoutSec(),
		clarifyMaxRounds:  clarify.DefaultMaxRounds(),
		sandboxMode:       preset.SandboxOn,
		goalStore:         goal.NewStore(),
		journalLog:        openSessionJournal(),
	}
	// G14/G15: build the live projection over the fresh (empty) session log. NewProjector
	// replays any pre-existing events, so a log restored from disk replays correctly.
	a.sessionProj, _ = journal.NewProjector(&sessionState{}, a.journalLog)

	// G10: permission-preset manager. The agent implements the two applier seams
	// so the manager can drive the G4 sandbox + G8 approval subsystems when the
	// active tuple changes. Default wiring already matches the `auto` preset
	// ({sandbox:on, approval:ask}), so no explicit apply is needed at startup.
	presetMgr, _ := preset.NewManager(preset.NewRegistry(), preset.NewMemoryPersister())
	presetMgr.SetAppliers(a, a)
	a.presetMgr = presetMgr

	// Load config files
	a.loadConfig()

	// Ensure memory directory exists
	memorySys.EnsureDirExists()

	return a
}

// openSessionJournal returns a file-backed session journal when TIANCAN_JOURNAL_PATH
// is set, otherwise an in-memory log. A failure to open the file (e.g. bad path,
// permission) silently falls back to an in-memory log so agent construction never
// breaks because of journaling.
func openSessionJournal() *journal.Log {
	if p := os.Getenv("TIANCAN_JOURNAL_PATH"); p != "" {
		if fl, err := journal.OpenLog(p); err == nil {
			return fl
		}
		// fall back to in-memory on any open error
	}
	return journal.NewLog()
}

// SetProgressCallback sets the callback for agent loop progress reporting.
func (a *Agent) SetProgressCallback(cb ProgressCallback) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progressCB = cb
}

// GetRollbackManager returns the rollback manager for file versioning.
func (a *Agent) GetRollbackManager() *ralph.RollbackManager {
	return a.rollbackMgr
}

// Registry returns the tool registry so callers can inject shared dependencies.
func (a *Agent) Registry() *tools.Registry {
	return a.registry
}

// SetSkillMatcher overrides the skill matcher (registry + FSProvider).
// Primarily for tests; the production default is wired in NewAgent.
func (a *Agent) SetSkillMatcher(sm SkillMatcher) {
	a.skillMatcher = sm
}

// SetToolGuard overrides the per-tool execution guard (deadline + the
// parent-cancel vs own-deadline distinction). Primarily for tests.
func (a *Agent) SetToolGuard(g *toolguard.Guard) {
	a.toolGuard = g
}

// SetDupDetector overrides the duplicate/rejected-call detector. Primarily for
// tests — a low threshold makes the redirect observable within a few iterations.
func (a *Agent) SetDupDetector(d *toolguard.Detector) {
	a.dupDetector = d
}

// SetApprovalSystem overrides the user-approval resolver (G8 four-value closed
// set, fail-closed). Primarily for tests; the production default is wired in
// NewAgent via approval.NewDefault().
func (a *Agent) SetApprovalSystem(s *approval.ApprovalSystem) {
	a.approvalSys = s
}

// SetClarifier overrides the clarifier (G9). Primarily for tests; the production
// default (fail-closed silent clarifier) is wired in NewAgent via
// clarify.NewDefault().
func (a *Agent) SetClarifier(c clarify.Clarifier) {
	a.clarifier = c
}

// --- G10: permission presets -------------------------------------------------
//
// The agent is the runtime applier for both preset knobs. The preset.Manager
// calls these only when the active tuple actually changes; the agent never
// imports the preset package's callers, keeping the leaf package dependency-free.

// SetPresetManager overrides the permission-preset manager (G10). Primarily for
// tests; the production default is wired in NewAgent.
func (a *Agent) SetPresetManager(m *preset.Manager) {
	a.presetMgr = m
}

// ApplySandbox implements preset.SandboxApplier (G10). Records the sandbox knob;
// the actual enforcement on the G4 sandbox provider is a G13 concern and reads
// this value.
func (a *Agent) ApplySandbox(mode preset.SandboxMode) {
	a.mu.Lock()
	a.sandboxMode = mode
	a.mu.Unlock()
}

// ApplyApproval implements preset.ApprovalApplier (G10). Translates the approval
// knob into the four-value approval system's runtime configuration.
func (a *Agent) ApplyApproval(mode preset.ApprovalMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch mode {
	case preset.ApprovalAllow:
		a.approvalSys.SetAnswerer(approval.NewPermissiveAnswerer())
		a.approvalSys.SetDenyAll(false)
	case preset.ApprovalNever:
		a.approvalSys.SetDenyAll(true)
	default: // ApprovalAsk — the fail-closed default
		a.approvalSys.SetAnswerer(approval.NewRejectingAnswerer())
		a.approvalSys.SetDenyAll(false)
	}
}

// ApplyKnobs sets the active {sandbox, approval} tuple. It persists (and applies
// to the runtime subsystems) only when a knob actually changed. Returns whether
// a write happened.
func (a *Agent) ApplyKnobs(k preset.Knobs) (bool, error) {
	return a.presetMgr.ApplyKnobs(k)
}

// SelectPreset selects a named preset by name (see preset.Manager.SelectPreset).
func (a *Agent) SelectPreset(name string) (bool, error) {
	return a.presetMgr.SelectPreset(name)
}

// ActivePresetName returns the effective preset name: a matching named preset, or
// `custom` if the active tuple diverged from every named preset.
func (a *Agent) ActivePresetName() string {
	return a.presetMgr.ActiveName()
}

// CurrentKnobs returns the active {sandbox, approval} tuple.
func (a *Agent) CurrentKnobs() preset.Knobs {
	return a.presetMgr.Current()
}

// SandboxMode returns the currently active sandbox knob.
func (a *Agent) SandboxMode() preset.SandboxMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sandboxMode
}

// --- G12: goal-driven + plan mode ---------------------------------------------
//
// The goal state machine lives in the leaf `goal` package; the agent owns a
// Store and exposes a thin, dependency-injectable API. Plan-mode enforcement is
// delegated to the G8 approval system (SetDenyAll), NOT to a tool interceptor:
// entering plan mode flips the approval system to deny-all, so any tool that
// requires approval is rejected by the fail-closed path. The plan-mode paragraph
// injected into the system prompt is guidance only.

// SetGoalStore overrides the goal store (G12). Primarily for tests; the production
// default is wired in NewAgent via goal.NewStore().
func (a *Agent) SetGoalStore(s *goal.Store) {
	a.goalStore = s
}

// GetGoal returns a snapshot of the current goal.
func (a *Agent) GetGoal() goal.Goal {
	if a.goalStore == nil {
		return goal.Goal{}
	}
	return a.goalStore.Get()
}

// SetGoal applies a goal via compare-and-swap against the expected revision.
func (a *Agent) SetGoal(g goal.Goal, expect int64) (goal.Goal, error) {
	if a.goalStore == nil {
		return goal.Goal{}, goal.ErrTerminal
	}
	return a.goalStore.Set(g, expect)
}

// ConsumeRound spends one round of the goal budget. Returns the budget-exhausted
// or terminal error so the caller can stop the loop fail-closed.
func (a *Agent) ConsumeRound() error {
	if a.goalStore == nil {
		return nil
	}
	_, err := a.goalStore.ConsumeRound()
	return err
}

// SetRoundBudget sets the goal's round budget (0 = unlimited).
func (a *Agent) SetRoundBudget(n int) error {
	if a.goalStore == nil {
		return goal.ErrTerminal
	}
	_, err := a.goalStore.SetBudget(n)
	return err
}

// EnterPlanMode flips the goal into plan mode and enforces it independently via the
// G8 approval system: any tool requiring approval is now denied by the fail-closed
// path. Read-only tools (DecisionAllow) still run — that is appropriate for
// planning. The system prompt is updated on the next buildSystemPrompt call.
func (a *Agent) EnterPlanMode(description string) error {
	if a.goalStore == nil {
		return goal.ErrTerminal
	}
	if _, err := a.goalStore.EnterPlanMode(description); err != nil {
		return err
	}
	if a.approvalSys != nil {
		a.approvalSys.SetDenyAll(true)
	}
	return nil
}

// ExitPlanMode leaves plan mode and re-syncs the approval subsystem to the active
// permission preset's knob (so deny-all is cleared unless the preset itself denies).
func (a *Agent) ExitPlanMode() error {
	if a.goalStore == nil {
		return goal.ErrTerminal
	}
	if _, err := a.goalStore.ExitPlanMode(); err != nil {
		return err
	}
	if a.approvalSys != nil && a.presetMgr != nil {
		a.ApplyApproval(a.presetMgr.Current().Approval)
	}
	return nil
}

// maybeClarify inspects the model response for a clarification request.
//
// Return contract:
//   - ok == false: not a clarification (or already settled / rounds exhausted) —
//     the machine is moved to Settled and the caller proceeds as normal.
//   - ok == true && pending == true: clarification is open but no answer is
//     available yet. The caller MUST treat this as {pending:true}, never as an
//     error (G9: timeout is not an error).
//   - ok == true && pending == false: answer holds the user's answer, ready to
//     be fed back as a user message.
func (a *Agent) maybeClarify(ctx context.Context, response string) (answer string, pending bool, ok bool) {
	prompt, isClar := clarify.IsClarification(response)
	if !isClar {
		a.clarifyMachine.Settle()
		return "", false, false
	}
	if a.clarifyMachine.IsSettled() {
		return "", false, false
	}
	state, _ := a.clarifyMachine.Open()
	_ = state
	if a.clarifyMachine.Rounds() > a.clarifyMaxRounds {
		// Too many rounds without settling — stop clarifying to avoid an
		// ask/answer loop, and let the response stand.
		a.clarifyMachine.Settle()
		return "", false, false
	}
	timeout := time.Duration(a.clarifyTimeoutSec) * time.Second
	ans, pend, _ := a.clarifier.Clarify(ctx, prompt, timeout)
	return ans, pend, true
}

// executeRegisteredTool runs the safety check + deadline-guarded execution for a
// registered tool, and tracks duplicate/rejected calls so a model hammering the
// same denied call is redirected with a reminder (G6).
//
// Returns the tool-result content, whether it succeeded, whether the tool
// actually executed (the allow path, so callers can emit progress callbacks),
// and the error string to surface on failure.
func (a *Agent) executeRegisteredTool(ctx context.Context, tool Tool, tc ToolCall) (content string, success bool, executed bool, errStr string) {
	// G6: duplicate / rejected-call reminder — short-circuit before re-running.
	if a.dupDetector != nil {
		if remind, reason := a.dupDetector.ShouldRemind(tc.Name, tc.Args); remind {
			a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeReminded)
			return reason, false, false, ""
		}
	}

	permResult, level := a.safetySys.CheckPermission(tool, tc.Args, a.rootPath)
	switch permResult.Decision {
	case DecisionDeny:
		if a.dupDetector != nil {
			a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeDenied)
		}
		return fmt.Sprintf("Permission denied: %s", permResult.Reason), false, false, ""
	case DecisionAsk:
		// G8: resolve the four-value closed set instead of a hard non-interactive
		// deny. allowed-once executes the tool; everything else is a fail-closed
		// denial.
		outcome := a.resolveApproval(ctx, tc.Name, tc.Args, permResult.Reason, int(level))
		if !outcome.IsDenial() {
			res := a.runToolGuarded(ctx, tool, tc)
			if a.dupDetector != nil {
				if res.Success {
					a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeOK)
				} else {
					a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeFailed)
				}
			}
			es := ""
			if !res.Success {
				es = res.Error
			}
			return res.Content, res.Success, true, es
		}
		if a.dupDetector != nil {
			a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeDenied)
		}
		return fmt.Sprintf("Permission denied: %s", approval.DenialReason(outcome, tc.Name)), false, false, ""
	case DecisionAllow:
		res := a.runToolGuarded(ctx, tool, tc)
		if a.dupDetector != nil {
			if res.Success {
				a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeOK)
			} else {
				a.dupDetector.Record(tc.Name, tc.Args, toolguard.OutcomeFailed)
			}
		}
		es := ""
		if !res.Success {
			es = res.Error
		}
		return res.Content, res.Success, true, es
	}
	// Unreachable: CheckPermission only returns Deny / Ask / Allow.
	return fmt.Sprintf("Tool %s requires permission", tc.Name), false, false, ""
}

// resolveApproval consults the user-approval system for a DecisionAsk request
// and returns one of the four closed-set outcomes (G8). If the approval system
// is not wired, it fails closed to Unavailable (still a denial).
func (a *Agent) resolveApproval(ctx context.Context, toolName string, args map[string]interface{}, reason string, level int) approval.Outcome {
	if a.approvalSys == nil {
		return approval.OutcomeUnavailable
	}
	out, _ := a.approvalSys.Resolve(ctx, approval.Request{
		ToolName: toolName,
		Args:     args,
		Reason:   reason,
		Level:    level,
	})
	return out
}

// runToolGuarded runs the tool under a per-tool deadline. The guard distinguishes
// a parent-context cancellation from the tool's own deadline (G6).
func (a *Agent) runToolGuarded(ctx context.Context, tool Tool, tc ToolCall) ToolResult {
	if a.toolGuard == nil {
		res, err := tool.Execute(ctx, tc.Args)
		if err != nil {
			return ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}
		return res
	}
	res, err := a.toolGuard.Execute(ctx, func(c context.Context) (ToolResult, error) {
		return tool.Execute(c, tc.Args)
	})
	if err != nil {
		switch e := err.(type) {
		case *toolguard.ErrParentCancelled:
			return ToolResult{
				Content: fmt.Sprintf("Tool %s cancelled: parent context done (%v)", tc.Name, e.Err),
				Success: false, Error: err.Error(),
			}
		case *toolguard.ErrDeadline:
			return ToolResult{
				Content: fmt.Sprintf("Tool %s timed out after %s", tc.Name, e.Deadline),
				Success: false, Error: err.Error(),
			}
		default:
			return ToolResult{Content: err.Error(), Success: false, Error: err.Error()}
		}
	}
	return res
}

// injectSkillPreSteps returns user-role messages for skills whose triggers match
// the incoming user request. These are prepended before the actual user message
// so the model sees the skill guidance as part of the first turn.
//
// KV-cache contract (G5): the guidance is rendered into user messages here and
// must NEVER enter buildSystemPrompt — keeping the system prompt static lets the
// provider reuse the cached prefix across skill matches.
func (a *Agent) injectSkillPreSteps(userMessage string) []Message {
	if a.skillMatcher == nil {
		return nil
	}
	matched := a.skillMatcher.MatchTrigger(userMessage)
	if len(matched) == 0 {
		return nil
	}
	pre := make([]Message, 0, len(matched))
	for _, s := range matched {
		pre = append(pre, Message{
			Role:      RoleUser,
			Content:   s.RenderPreStep(),
			Timestamp: time.Now(),
		})
	}
	return pre
}

// loadConfig loads TIANCAN.md config files.
func (a *Agent) loadConfig() {
	configs, err := config.LoadAllConfigs(a.rootPath, a.homeDir)
	if err == nil {
		a.configFiles = configs
	}
}

// Run executes the main agent loop (Think-Act-Observe-Repeat).
// chatFn is the function that sends messages to the AI model.
func (a *Agent) Run(ctx context.Context, userMessage string, maxIterations int, chatFn ChatFunc) AgentRunResult {
	if maxIterations <= 0 {
		maxIterations = 20
	}
	start := time.Now()

	// Determine if task should be routed
	agentDef := roles.GeneralAgent
	if roles.ShouldRoute(userMessage) {
		agentDef = roles.RouterAgent
	}

	// Check if Ralph Loop should be used for non-trivial tasks
	useRalph := a.shouldUseRalphLoop(userMessage)
	if useRalph {
		return a.runWithRalph(ctx, userMessage, agentDef, chatFn)
	}

	// Build system prompt
	systemPrompt := a.buildSystemPrompt(agentDef)

	// Initialize messages
	a.mu.Lock()
	// G5: inject matched skill guidance as pre-step user messages, BEFORE the
	// real user request (keeps it out of the system prompt for KV-cache safety).
	for _, ps := range a.injectSkillPreSteps(userMessage) {
		a.messages = append(a.messages, ps)
	}
	a.messages = append(a.messages, Message{
		Role:      RoleUser,
		Content:   userMessage,
		Timestamp: time.Now(),
	})
	messages := make([]Message, len(a.messages))
	copy(messages, a.messages)
	a.mu.Unlock()

	totalTokens := 0

	// G15: turn/step loop with a double-queue inbox and a whenIdle do/while.
	// The do/while re-prompts the model whenever the inbox is drained (no pending
	// steps), so termination is driven by the model's final answer (no tool calls and
	// no open clarification), not by the iteration cap. maxIterations is only a
	// fail-closed backstop for non-terminating scripts.
	iteration := 0
	for {
		if iteration >= maxIterations {
			a.mu.Lock()
			a.messages = messages
			a.mu.Unlock()
			return AgentRunResult{
				Done:        true,
				Messages:    messages,
				TotalTokens: totalTokens,
				DurationMs:  time.Since(start).Milliseconds(),
			}
		}
		iteration++

		select {
		case <-ctx.Done():
			a.mu.Lock()
			a.messages = messages
			a.mu.Unlock()
			return AgentRunResult{Done: true, Error: "cancelled", Messages: messages, DurationMs: time.Since(start).Milliseconds()}
		default:
		}

		// G12: spend a goal round; fail-closed when the budget is exhausted.
		if err := a.ConsumeRound(); err != nil {
			a.mu.Lock()
			a.messages = messages
			a.mu.Unlock()
			return AgentRunResult{
				Done:        true,
				Error:       "goal round budget exhausted",
				Messages:    messages,
				TotalTokens: totalTokens,
				DurationMs:  time.Since(start).Milliseconds(),
			}
		}

		// Check auto-compact
		if a.compactSvc.ShouldAutoCompact(messages) {
			result, err := a.compactSvc.CompactConversation(messages, func(prompt string) (string, error) {
				return chatFn(systemPrompt, []Message{{Role: RoleUser, Content: prompt}})
			})
			if err == nil && result != nil {
				messages = append(result.SummaryMessages, messages[len(messages)-4:]...)
			}
		}

		// ---- TURN: ask the model ----
		response, err := chatFn(systemPrompt, messages)
		if err != nil {
			a.mu.Lock()
			a.messages = messages
			a.mu.Unlock()
			return AgentRunResult{
				Done:       true,
				Error:      fmt.Sprintf("AI request failed: %v", err),
				Messages:   messages,
				DurationMs: time.Since(start).Milliseconds(),
			}
		}

		assistantMsg := Message{
			Role:      RoleAssistant,
			Content:   response,
			Timestamp: time.Now(),
		}
		messages = append(messages, assistantMsg)
		totalTokens += len(response) / 4

		// Parse tool calls and record the turn in the session log (G14/G15).
		toolCalls := parseToolCallFromResponse(response)
		a.emitTurn(response, len(toolCalls))

		if len(toolCalls) == 0 {
			// G9: the response may be a clarification request rather than a
			// final answer. Resolve it before declaring done.
			answer, pending, isClar := a.maybeClarify(ctx, response)
			if isClar && pending {
				// Timeout / offline user → {pending:true}, NOT an error.
				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				return AgentRunResult{
					Done:        true,
					Pending:     true,
					Messages:    messages,
					TotalTokens: totalTokens,
					DurationMs:  time.Since(start).Milliseconds(),
				}
			}
			if isClar {
				// The user's answer is fed back as a user message and the
				// loop re-runs with it in context (do/while re-prompt).
				messages = append(messages, Message{
					Role:      RoleUser,
					Content:   answer,
					Timestamp: time.Now(),
				})
				totalTokens += len(answer) / 4
				continue
			}

			// No more tool calls - done
			a.mu.Lock()
			a.messages = messages
			cb := a.progressCB
			a.mu.Unlock()

			// Extract memories
			a.memorySys.ExtractMemories(messages)

			if cb != nil {
				cb(iteration, "", nil, response, true, "")
			}

			return AgentRunResult{
				Done:        true,
				Messages:    messages,
				TotalTokens: totalTokens,
				DurationMs:  time.Since(start).Milliseconds(),
			}
		}

		// ---- INBOX (request queue): execute the requested tool calls as steps,
		// then commit their results in MODEL ORDER (double-queue: request inbox +
		// result queue re-sequenced to model order). ----
		outcomes := a.runSteps(ctx, toolCalls)
		for i, tc := range toolCalls {
			o := outcomes[i]
			messages = append(messages, Message{
				Role:     RoleTool,
				Content:  o.Content,
				ToolName: tc.Name,
				Success:  o.Success,
			})
			a.emitStep(tc, ToolResult{Content: o.Content, Success: o.Success})
			if o.Executed {
				totalTokens += len(o.Content) / 4
				a.mu.Lock()
				cb := a.progressCB
				a.mu.Unlock()
				if cb != nil {
					cb(iteration, tc.Name, tc.Args, o.Content, false, o.ErrStr)
				}
			}
		}
		// whenIdle do/while: with the inbox drained we loop back and re-prompt the
		// model, unless it just produced a final answer (handled in the branch above).
	}
}

// RunStream executes the agent loop with streaming events sent to the returned channel.
// The caller must drain the channel to avoid blocking. The channel is closed when done.
func (a *Agent) RunStream(ctx context.Context, userMessage string, maxIterations int, streamChatFn StreamChatFunc) (<-chan StreamEvent, error) {
	if maxIterations <= 0 {
		maxIterations = 20
	}
	ch := make(chan StreamEvent, 256)

	go func() {
		defer close(ch)

		agentDef := roles.GeneralAgent
		if roles.ShouldRoute(userMessage) {
			agentDef = roles.RouterAgent
		}

		systemPrompt := a.buildSystemPrompt(agentDef)

		a.mu.Lock()
		// G5: inject matched skill guidance as pre-step user messages (see Run).
		for _, ps := range a.injectSkillPreSteps(userMessage) {
			a.messages = append(a.messages, ps)
		}
		a.messages = append(a.messages, Message{
			Role:      RoleUser,
			Content:   userMessage,
			Timestamp: time.Now(),
		})
		messages := make([]Message, len(a.messages))
		copy(messages, a.messages)
		a.mu.Unlock()

		totalTokens := 0

		// G15: turn/step loop with a double-queue inbox and a whenIdle do/while
		// (see Run for the full rationale; this is the streaming twin).
		iteration := 0
		for {
			if iteration >= maxIterations {
				ch <- StreamEvent{Type: "error", Content: fmt.Sprintf("达到最大迭代次数 %d", maxIterations)}
				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				return
			}
			iteration++

			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: "error", Content: "cancelled"}
				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				return
			default:
			}

			// G12: spend a goal round; fail-closed when the budget is exhausted.
			if err := a.ConsumeRound(); err != nil {
				ch <- StreamEvent{Type: "error", Content: "goal round budget exhausted"}
				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				return
			}

			// Auto-compact check
			if a.compactSvc.ShouldAutoCompact(messages) {
				ch <- StreamEvent{Type: "compact", Content: "Compacting conversation..."}
				result, err := a.compactSvc.CompactConversation(messages, func(prompt string) (string, error) {
					return streamChatFn(systemPrompt, []Message{{Role: RoleUser, Content: prompt}}, nil)
				})
				if err == nil && result != nil {
					messages = append(result.SummaryMessages, messages[len(messages)-4:]...)
				}
			}

			// Streaming chat call
			var fullResp strings.Builder
			response, err := streamChatFn(systemPrompt, messages, func(token string) {
				fullResp.WriteString(token)
				ch <- StreamEvent{Type: "token", Content: token}
			})
			if err != nil {
				ch <- StreamEvent{Type: "error", Content: fmt.Sprintf("AI request failed: %v", err)}
				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				return
			}

			resp := fullResp.String()
			if resp == "" {
				resp = response
			}

			messages = append(messages, Message{
				Role:      RoleAssistant,
				Content:   resp,
				Timestamp: time.Now(),
			})
			totalTokens += len(resp) / 4

			// Parse tool calls and record the turn in the session log (G14/G15).
			toolCalls := parseToolCallFromResponse(resp)
			a.emitTurn(resp, len(toolCalls))

			if len(toolCalls) == 0 {
				// G9: the response may be a clarification request.
				answer, pending, isClar := a.maybeClarify(ctx, resp)
				if isClar && pending {
					// Timeout / offline user → {pending:true}, NOT an error.
					a.mu.Lock()
					a.messages = messages
					a.mu.Unlock()
					ch <- StreamEvent{Type: "pending", Content: resp}
					return
				}
				if isClar {
					// The user's answer is fed back as a user message and the
					// loop re-runs with it in context (do/while re-prompt).
					messages = append(messages, Message{
						Role:      RoleUser,
						Content:   answer,
						Timestamp: time.Now(),
					})
					totalTokens += len(answer) / 4
					continue
				}

				a.mu.Lock()
				a.messages = messages
				a.mu.Unlock()
				a.memorySys.ExtractMemories(messages)
				ch <- StreamEvent{Type: "done", Content: resp}
				return
			}

			// ---- INBOX: execute requested tool calls as steps, commit results in
			// model order (double-queue inbox + result re-sequencing). ----
			outcomes := a.runSteps(ctx, toolCalls)
			for i, tc := range toolCalls {
				o := outcomes[i]
				argsJSON, _ := json.Marshal(tc.Args)
				ch <- StreamEvent{Type: "tool_call", Name: tc.Name, Content: string(argsJSON)}
				ch <- StreamEvent{Type: "tool_result", Name: tc.Name, Content: o.Content, Success: o.Success}
				messages = append(messages, Message{Role: RoleTool, Content: o.Content, ToolName: tc.Name, Success: o.Success})
				a.emitStep(tc, ToolResult{Content: o.Content, Success: o.Success})
				if o.Executed {
					totalTokens += len(o.Content) / 4
				}
			}
			// whenIdle do/while: re-prompt the model after the inbox drains.
		}
	}()

	return ch, nil
}

// shouldUseRalphLoop determines if a task should use the Ralph Loop.
func (a *Agent) shouldUseRalphLoop(task string) bool {
	if envconfig.EnvBool("TIANCAN_DISABLE_RALPH") {
		return false
	}
	// Use Ralph Loop for tasks that involve multiple file edits or API changes
	indicators := []string{
		"implement", "refactor", "migrate", "fix bug", "add feature",
		"create", "build", "port",
		"实现", "重构", "迁移", "修复", "添加", "创建", "构建", "移植",
	}
	lower := strings.ToLower(task)
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// runWithRalph runs the agent with the Ralph Loop for self-correction.
func (a *Agent) runWithRalph(ctx context.Context, task string, agentDef AgentDefinition, chatFn ChatFunc) AgentRunResult {
	start := time.Now()

	rl := ralph.NewRalphLoop(
		func(systemPrompt string, msgs []Message) (string, error) {
			return chatFn(systemPrompt, msgs)
		},
		func(ctx context.Context, toolName string, args map[string]interface{}) (ToolResult, error) {
			return a.registry.Execute(ctx, toolName, args)
		},
	)

	result, err := rl.Run(ctx, task, agentDef, a.rootPath)
	if err != nil {
		return AgentRunResult{
			Done:       true,
			Error:      fmt.Sprintf("Ralph Loop error: %v", err),
			Messages:   result.Messages,
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	return AgentRunResult{
		Done:        true,
		Messages:    result.Messages,
		AgentType:   string(agentDef.AgentType),
		TotalTokens: compact.EstimateTokens(result.Messages),
		DurationMs:  time.Since(start).Milliseconds(),
		RalphResult: result,
	}
}

// buildSystemPrompt assembles the full system prompt from all subsystems.
func (a *Agent) buildSystemPrompt(agentDef AgentDefinition) string {
	var sb strings.Builder

	// Base agent prompt
	sb.WriteString(agentDef.SystemPrompt)
	sb.WriteString("\n\n")

	// Config instructions (TIANCAN.md)
	if len(a.configFiles) > 0 {
		sb.WriteString(config.BuildSystemPrompt(a.configFiles))
		sb.WriteString("\n\n")
	}

	// Memory prompt
	if memPrompt := a.memorySys.LoadMemoryPrompt(); memPrompt != "" {
		sb.WriteString(memPrompt)
		sb.WriteString("\n\n")
	}

	// Available tools listing
	sb.WriteString("# Available Tools\n\n")
	for _, t := range a.registry.All() {
		ro := ""
		if t.IsReadOnly() {
			ro = " (read-only)"
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s%s\n", t.Name(), t.Description(), ro))
	}

	// MCP tools
	for _, t := range a.mcpClient.GetTools() {
		ro := ""
		if t.ReadOnly {
			ro = " (read-only)"
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s%s [MCP:%s]\n", t.Name, t.Description, ro, t.ServerName))
	}

	sb.WriteString("\n")

	// Search guidance
	sb.WriteString("## Important: When to Search\n\n")
	sb.WriteString("Use **web_search** when you need:\n")
	sb.WriteString("- Current documentation or tutorials\n")
	sb.WriteString("- API usage examples from the web\n")
	sb.WriteString("- Solutions to unknown errors\n")
	sb.WriteString("- Latest version features or changelogs\n")
	sb.WriteString("- Technical blog posts or articles\n\n")

	// Safety mode indicator
	if a.safetySys.IsAutoMode() {
		sb.WriteString("Running in **auto mode** — tool calls are automatically classified for safety.\n\n")
	}

	// G12: plan mode — guidance only. The hard enforcement is delegated to the
	// approval system (deny-all), so this paragraph never needs to be trusted as
	// the enforcement boundary.
	if a.goalStore != nil && a.goalStore.Get().Mode == goal.ModePlan {
		sb.WriteString("## Plan Mode\n\n")
		sb.WriteString("You are in **PLAN MODE**. Propose the change and explain your plan clearly. ")
		sb.WriteString("Do NOT execute mutating tools — any tool that requires approval is blocked by the ")
		sb.WriteString("environment. Present a plan for review instead of carrying it out.\n\n")
	}

	// Project root
	sb.WriteString(fmt.Sprintf("Project root: %s\n", a.rootPath))

	return sb.String()
}

// ConnectMCPServer connects to an MCP server and registers its tools.
func (a *Agent) ConnectMCPServer(name string, cfg MCPConfig) (*MCPServerState, error) {
	state, err := a.mcpClient.ConnectToServer(name, cfg)
	if err != nil {
		return state, err
	}
	// Register MCP tools in the tool registry
	if state.Type == MCPConnConnected {
		for _, t := range state.Tools {
			a.registry.RegisterMCP(&mcpToolWrapper{info: t, client: a.mcpClient})
		}
	}
	return state, nil
}

// DisconnectMCPServer disconnects from an MCP server.
func (a *Agent) DisconnectMCPServer(name string) error {
	a.registry.UnregisterMCP(name)
	return a.mcpClient.DisconnectServer(name)
}

// GetMCPServers returns all MCP server states.
func (a *Agent) GetMCPServers() []MCPServerState {
	return a.mcpClient.GetServers()
}

// GetConfigFiles returns loaded config files.
func (a *Agent) GetConfigFiles() []config.ConfigFile {
	return a.configFiles
}

// GetTokenWarningState returns token usage warnings.
func (a *Agent) GetTokenWarningState() (int, bool, bool) {
	tokenCount := compact.EstimateTokens(a.messages)
	percentLeft, isAboveWarning, isAboveAutoCompact := a.compactSvc.CalculateTokenWarningState(tokenCount)
	return percentLeft, isAboveWarning, isAboveAutoCompact
}

// --- Tool call parsing ---

// parseToolCallFromResponse delegates to webai/parser which handles all formats
// (XML, JSON, emoji, Anthropic, DeepSeek, function-call, etc) without hardcoded tool lists.
func parseToolCallFromResponse(response string) []ToolCall {
	webCalls := webparser.ParseToolCalls(response)
	var calls []ToolCall
	for _, wc := range webCalls {
		args := make(map[string]interface{})
		if wc.Arguments != nil {
			json.Unmarshal(wc.Arguments, &args)
		}
		calls = append(calls, ToolCall{Name: wc.Name, Args: args})
	}
	return calls
}

// extractJSON tries to find a JSON array in the response.
func extractJSON(s string) string {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return "[]"
}

// allKnownTools is dynamically populated from the tool registry.
// No hardcoded tool names — tools are registered at runtime.
var allKnownTools = map[string]bool{}

// RegisterToolName adds a tool name to the known tools set.
func RegisterToolName(name string) {
	allKnownTools[name] = true
}

// IsKnownTool checks if a tool name is registered.
func IsKnownTool(name string) bool {
	return allKnownTools[name]
}

// parseXMLToolCalls and parseFunctionCallToolCalls are replaced by
// webai/parser.ParseToolCalls which handles all formats without hardcoded tool lists.
// The agent-level parseToolCallFromResponse delegates to it.

// --- MCP tool wrapper ---

// mcpToolWrapper wraps an MCP tool info as a Tool interface.
type mcpToolWrapper struct {
	info   MCPToolInfo
	client *mcp.Client
}

func (t *mcpToolWrapper) Name() string                        { return t.info.Name }
func (t *mcpToolWrapper) Description() string                 { return t.info.Description }
func (t *mcpToolWrapper) InputSchema() map[string]interface{} { return t.info.InputSchema }
func (t *mcpToolWrapper) IsReadOnly() bool                    { return t.info.ReadOnly }
func (t *mcpToolWrapper) IsConcurrencySafe() bool             { return t.info.ReadOnly }
func (t *mcpToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error) {
	return t.client.CallTool(ctx, t.info.Name, args)
}

// ResolvePath resolves a path relative to root, handling zero-dependency resolution.
func ResolvePath(path, rootPath string) string {
	if filepath.IsAbs(path) {
		return path
	}
	// Handle ~/ paths
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return filepath.Join(rootPath, path)
}
