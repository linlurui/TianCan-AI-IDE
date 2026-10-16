package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
)

// sessionState is the G15 projection folded from the session event log (G14). It is
// a pure-JSON, in-place-mutating journal.State: Apply must return the SAME pointer
// and PureJSON/LoadJSON must round-trip losslessly — both enforced by the journal
// package's invariants.
type sessionState struct {
	Turns         int    `json:"turns"`
	Steps         int    `json:"steps"`
	LastAssistant string `json:"lastAssistant"`
	LastTool      string `json:"lastTool"`
	LastToolOK    bool   `json:"lastToolOK"`
}

func (s *sessionState) Apply(e journal.Event) journal.State {
	switch e.Kind {
	case "turn":
		s.Turns++
		var p struct {
			Text string `json:"text"`
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &p)
		}
		s.LastAssistant = p.Text
	case "step":
		s.Steps++
		var p struct {
			Name    string `json:"name"`
			Success bool   `json:"success"`
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &p)
		}
		s.LastTool = p.Name
		s.LastToolOK = p.Success
	}
	return s
}

func (s *sessionState) PureJSON() ([]byte, error) { return json.Marshal(s) }
func (s *sessionState) LoadJSON(b []byte) error   { return json.Unmarshal(b, s) }

// Journal returns the session event log (G14/G15). Each model turn and tool step
// is recorded here; the live state is its pure fold (see SessionProjection).
func (a *Agent) Journal() *journal.Log { return a.journalLog }

// SessionProjection returns the current folded projection of the session log.
func (a *Agent) SessionProjection() journal.State { return a.sessionProj.State() }

// emitTurn records one model turn into the session log and folds it into the
// projection. Called once per model interaction (the TURN half of turn/step).
func (a *Agent) emitTurn(response string, nToolCalls int) {
	_, _ = a.sessionProj.AppendAndApply("turn", map[string]interface{}{
		"text":      response,
		"toolCalls": nToolCalls,
	})
}

// emitStep records one tool step (a requested tool call + its result) into the
// session log and folds it into the projection (the STEP half of turn/step).
func (a *Agent) emitStep(tc ToolCall, res ToolResult) {
	_, _ = a.sessionProj.AppendAndApply("step", map[string]interface{}{
		"name":    tc.Name,
		"success": res.Success,
	})
}

// stepOutcome is the result of executing a single tool step.
type stepOutcome struct {
	ToolName string
	Content  string
	Success  bool
	Executed bool
	ErrStr   string
}

// runSteps executes a batch of tool calls requested by one model turn — the
// REQUEST QUEUE (inbox) — and returns their outcomes in MODEL ORDER (the order the
// model requested them), regardless of completion order. Concurrency-safe tools
// run in parallel; everything else runs sequentially. This is the G15
// "双队列 inbox" + "工具结果按模型顺序提交": the result queue is re-sequenced to the
// request order before the caller commits results into the conversation.
func (a *Agent) runSteps(ctx context.Context, calls []ToolCall) []stepOutcome {
	out := make([]stepOutcome, len(calls))

	// Only parallelize when every call resolves to a concurrency-safe registered
	// tool. MCP/unknown tools and non-safe tools run sequentially.
	parallel := len(calls) > 1
	if parallel {
		for _, tc := range calls {
			tool, found := a.registry.Get(tc.Name)
			if !found || !tool.IsConcurrencySafe() {
				parallel = false
				break
			}
		}
	}

	if !parallel {
		for i := range calls {
			out[i] = a.execStep(ctx, calls[i])
		}
		return out
	}

	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(idx int, tc ToolCall) {
			defer wg.Done()
			out[idx] = a.execStep(ctx, tc)
		}(i, calls[i])
	}
	wg.Wait()
	return out
}

// execStep runs one tool call through the safety/approval/dup pipeline (G6/G8) and
// returns its outcome. Unknown tools fall through to the MCP client.
func (a *Agent) execStep(ctx context.Context, tc ToolCall) stepOutcome {
	tool, found := a.registry.Get(tc.Name)
	if !found {
		mcpResult, mcpErr := a.mcpClient.CallTool(ctx, tc.Name, tc.Args)
		if mcpErr != nil {
			return stepOutcome{
				ToolName: tc.Name,
				Content:  fmt.Sprintf("Tool not found: %s", tc.Name),
				Success:  false,
			}
		}
		return stepOutcome{
			ToolName: tc.Name,
			Content:  mcpResult.Content,
			Success:  mcpResult.Success,
			Executed: true,
		}
	}
	content, success, executed, errStr := a.executeRegisteredTool(ctx, tool, tc)
	return stepOutcome{
		ToolName: tc.Name,
		Content:  content,
		Success:  success,
		Executed: executed,
		ErrStr:   errStr,
	}
}
