package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/testkit"
)

// newG15Agent builds an isolated Agent for G15 loop tests (HOME on a temp dir,
// Ralph loop disabled so routing/self-correction never hijacks the scripted task).
func newG15Agent(t *testing.T) *Agent {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TIANCAN_DISABLE_RALPH", "1")
	return NewAgent(t.TempDir())
}

// g15SlowTool is a controllable Tool whose execution latency can be set, so tests
// can force tool-completion order to differ from the model's requested order.
type g15SlowTool struct {
	name  string
	delay time.Duration
	safe  bool
	calls atomic.Int32
}

func (s *g15SlowTool) Name() string                        { return s.name }
func (s *g15SlowTool) Description() string                 { return "g15 slow tool " + s.name }
func (s *g15SlowTool) InputSchema() map[string]interface{} { return map[string]interface{}{} }
func (s *g15SlowTool) IsReadOnly() bool                    { return true }
func (s *g15SlowTool) IsConcurrencySafe() bool             { return s.safe }
func (s *g15SlowTool) Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return ToolResult{Content: s.name + " ok", Success: true}, nil
}

// TestG15_ModelOrderSubmissionParallel is the core G15 反证: when a model turn
// requests [slow_first, fast_second] and both run in parallel, fast_second
// (0ms) finishes LONG before slow_first (60ms) — yet the results must be committed
// to the conversation in MODEL ORDER [slow_first, fast_second]. If ordering were
// "whatever finished first", the order would be reversed; the test fails.
func TestG15_ModelOrderSubmissionParallel(t *testing.T) {
	agent := newG15Agent(t)
	slow := &g15SlowTool{name: "slow_first", delay: 60 * time.Millisecond, safe: true}
	fast := &g15SlowTool{name: "fast_second", delay: 0, safe: true}
	agent.Registry().Register(slow)
	agent.Registry().Register(fast)

	adapter := testkit.NewMockAdapter(
		testkit.ToolCallResponse(
			testkit.MockToolCall{Name: "slow_first", Args: map[string]interface{}{}},
			testkit.MockToolCall{Name: "fast_second", Args: map[string]interface{}{}},
		),
		testkit.TextResponse("done"),
	)

	res := agent.Run(context.Background(), "go", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run not done: %+v", res)
	}
	if res.Error != "" {
		t.Fatalf("run error: %q", res.Error)
	}
	if got := len(adapter.Calls()); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	if slow.calls.Load() != 1 || fast.calls.Load() != 1 {
		t.Fatalf("call counts slow=%d fast=%d, want 1/1", slow.calls.Load(), fast.calls.Load())
	}

	// Tool messages must appear in MODEL ORDER regardless of completion order.
	var toolOrder []string
	for _, m := range res.Messages {
		if m.Role == RoleTool {
			toolOrder = append(toolOrder, m.ToolName)
		}
	}
	if len(toolOrder) != 2 || toolOrder[0] != "slow_first" || toolOrder[1] != "fast_second" {
		t.Fatalf("tool order = %v, want [slow_first fast_second]", toolOrder)
	}
}

// TestG15_runStepsCommitsInModelOrder exercises the double-queue inbox sequencer
// directly: even though fast completes instantly and slow sleeps, the returned
// outcomes are indexed by the model's request order.
func TestG15_runStepsCommitsInModelOrder(t *testing.T) {
	agent := newG15Agent(t)
	slow := &g15SlowTool{name: "slow", delay: 50 * time.Millisecond, safe: true}
	fast := &g15SlowTool{name: "fast", delay: 0, safe: true}
	agent.Registry().Register(slow)
	agent.Registry().Register(fast)

	out := agent.runSteps(context.Background(), []ToolCall{{Name: "slow"}, {Name: "fast"}})
	if out[0].ToolName != "slow" || out[1].ToolName != "fast" {
		t.Fatalf("out order = [%s %s], want [slow fast]", out[0].ToolName, out[1].ToolName)
	}
}

// TestG15_WhenIdleDoWhile re-prompts the model after each inbox drains. A script
// of [toolA turn] -> [toolB turn] -> [final] must drive 3 model calls and execute
// both tools, proving the do/while re-prompt (not the iteration cap) is what
// advances the loop.
func TestG15_WhenIdleDoWhile(t *testing.T) {
	agent := newG15Agent(t)
	a := &g15SlowTool{name: "toolA", delay: 5 * time.Millisecond, safe: true}
	b := &g15SlowTool{name: "toolB", delay: 5 * time.Millisecond, safe: true}
	agent.Registry().Register(a)
	agent.Registry().Register(b)

	adapter := testkit.NewMockAdapter(
		testkit.ToolCallResponse(testkit.MockToolCall{Name: "toolA", Args: map[string]interface{}{}}),
		testkit.ToolCallResponse(testkit.MockToolCall{Name: "toolB", Args: map[string]interface{}{}}),
		testkit.TextResponse("all done"),
	)

	res := agent.Run(context.Background(), "go", 5, adapter.Func())
	if !res.Done || res.Error != "" {
		t.Fatalf("run failed: %+v", res)
	}
	if got := len(adapter.Calls()); got != 3 {
		t.Fatalf("model calls = %d, want 3", got)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Fatalf("toolA calls=%d toolB calls=%d, want 1/1", a.calls.Load(), b.calls.Load())
	}
}

// TestG15_JournalRecordsTurnsAndSteps verifies the G14/G15 integration: every
// model turn and tool step is appended to the session log, in order, and the live
// projection reflects Turns/Steps correctly.
func TestG15_JournalRecordsTurnsAndSteps(t *testing.T) {
	agent := newG15Agent(t)
	gate := testkit.NewGateTool("probe_tool",
		testkit.WithConcurrencySafe(true), testkit.WithReadOnly(true), testkit.WithAutoRelease())
	agent.Registry().Register(gate)

	adapter := testkit.NewMockAdapter(
		testkit.ToolCallResponse(testkit.MockToolCall{Name: "probe_tool", Args: map[string]interface{}{"value": "x"}}),
		testkit.TextResponse("探针完成"),
	)
	res := agent.Run(context.Background(), "probe", 5, adapter.Func())
	if !res.Done || res.Error != "" {
		t.Fatalf("run failed: %+v", res)
	}

	events := agent.Journal().Events()
	// Expected: turn(1, with tool call) -> step(probe) -> turn(2, final answer).
	if len(events) < 3 {
		t.Fatalf("journal events = %d, want >= 3", len(events))
	}
	if events[0].Kind != "turn" {
		t.Fatalf("events[0].Kind=%q want turn", events[0].Kind)
	}
	if events[1].Kind != "step" {
		t.Fatalf("events[1].Kind=%q want step", events[1].Kind)
	}
	if events[2].Kind != "turn" {
		t.Fatalf("events[2].Kind=%q want turn", events[2].Kind)
	}

	proj, ok := agent.SessionProjection().(*sessionState)
	if !ok {
		t.Fatalf("projection type = %T, want *sessionState", agent.SessionProjection())
	}
	if proj.Turns != 2 {
		t.Fatalf("Turns=%d want 2", proj.Turns)
	}
	if proj.Steps != 1 {
		t.Fatalf("Steps=%d want 1", proj.Steps)
	}
	if !proj.LastToolOK {
		t.Fatalf("LastToolOK=%v want true", proj.LastToolOK)
	}
}

// TestG15_LogIsFaithfulSourceOfTruth rebuilds a fresh projection by replaying the
// session log from scratch (the G14 invariant: state == pure fold of the log). If
// the log were not a faithful record, the replayed projection would diverge.
func TestG15_LogIsFaithfulSourceOfTruth(t *testing.T) {
	agent := newG15Agent(t)
	gate := testkit.NewGateTool("probe_tool",
		testkit.WithConcurrencySafe(true), testkit.WithReadOnly(true), testkit.WithAutoRelease())
	agent.Registry().Register(gate)
	adapter := testkit.NewMockAdapter(
		testkit.ToolCallResponse(testkit.MockToolCall{Name: "probe_tool", Args: map[string]interface{}{"value": "x"}}),
		testkit.TextResponse("探针完成"),
	)
	if res := agent.Run(context.Background(), "probe", 5, adapter.Func()); !res.Done || res.Error != "" {
		t.Fatalf("run failed: %+v", res)
	}

	log := agent.Journal()
	fresh := &sessionState{}
	replayed, err := journal.NewProjector(fresh, log) // replays the existing log on construction
	if err != nil {
		t.Fatalf("replay projector: %v", err)
	}
	got := replayed.State().(*sessionState)
	want := agent.SessionProjection().(*sessionState)
	if got.Turns != want.Turns || got.Steps != want.Steps {
		t.Fatalf("replayed projection = {Turns:%d Steps:%d}, want {Turns:%d Steps:%d}",
			got.Turns, got.Steps, want.Turns, want.Steps)
	}
}

// TestG15_AnchorThenRecover exercises the full G14 anchor + low-level recovery path
// on the live agent projection: after a run, snapshot the projection via Anchor
// (PureJSON), then Recover (LoadJSON + tail replay) and confirm the projection is
// faithfully restored. This also drives sessionState.PureJSON/LoadJSON.
func TestG15_AnchorThenRecover(t *testing.T) {
	agent := newG15Agent(t)
	gate := testkit.NewGateTool("probe_tool",
		testkit.WithConcurrencySafe(true), testkit.WithReadOnly(true), testkit.WithAutoRelease())
	agent.Registry().Register(gate)
	adapter := testkit.NewMockAdapter(
		testkit.ToolCallResponse(testkit.MockToolCall{Name: "probe_tool", Args: map[string]interface{}{"value": "x"}}),
		testkit.TextResponse("探针完成"),
	)
	if res := agent.Run(context.Background(), "probe", 5, adapter.Func()); !res.Done || res.Error != "" {
		t.Fatalf("run failed: %+v", res)
	}

	if _, err := agent.sessionProj.Anchor(); err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if err := agent.sessionProj.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	proj := agent.SessionProjection().(*sessionState)
	if proj.Turns != 2 || proj.Steps != 1 {
		t.Fatalf("post-recover projection = {Turns:%d Steps:%d}, want {2 1}", proj.Turns, proj.Steps)
	}
	if !proj.LastToolOK {
		t.Fatalf("post-recover LastToolOK=%v want true", proj.LastToolOK)
	}
}
