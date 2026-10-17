// Command smoke is a headless, zero-network integration run used to verify the
// agent main loop (G14/G15) plus the journal persistence (落盘 / 崩溃恢复) end to
// end. It drives the agent with a scripted MockAdapter and reports:
//  1. the run result (done / error / model-call count),
//  2. the in-memory session journal (event kinds + seqs),
//  3. a reopen of the SAME journal file and a fresh replay, proving the on-disk
//     log is a faithful source of truth (disk == memory).
//
// Run: TIANCAN_JOURNAL_PATH=/tmp/smoke.log go run ./backend/cmd/smoke
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/testkit"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// smokeState is a minimal journal.State that folds "turn"/"step" events so we can
// independently replay the on-disk log and compare against the live projection.
type smokeState struct {
	Turns int `json:"turns"`
	Steps int `json:"steps"`
}

func (s *smokeState) Apply(e journal.Event) journal.State {
	switch e.Kind {
	case "turn":
		s.Turns++
	case "step":
		s.Steps++
	}
	return s
}
func (s *smokeState) PureJSON() ([]byte, error)  { return json.Marshal(s) }
func (s *smokeState) LoadJSON(data []byte) error { return json.Unmarshal(data, s) }

func main() {
	tmp := os.TempDir()
	home := filepath.Join(tmp, "smoke-home")
	root := filepath.Join(tmp, "smoke-root")
	journalPath := filepath.Join(tmp, "smoke-session.log")
	_ = os.Remove(journalPath)

	// Isolate: temp HOME, Ralph off (so the scripted task isn't hijacked by the
	// self-correction loop), and a real on-disk journal.
	os.Setenv("HOME", home)
	os.Setenv("TIANCAN_DISABLE_RALPH", "1")
	os.Setenv("TIANCAN_JOURNAL_PATH", journalPath)

	a := agent.NewAgent(root)

	// A controllable tool so the loop has something to execute.
	demo := testkit.NewGateTool("demo_tool",
		testkit.WithConcurrencySafe(true),
		testkit.WithReadOnly(true),
		testkit.WithAutoRelease())
	a.Registry().Register(demo)

	// Script: one turn that calls the tool, then a final answer.
	adapter := testkit.NewMockAdapter(
		testkit.ToolCallResponse(testkit.MockToolCall{Name: "demo_tool", Args: map[string]interface{}{"q": "ping"}}),
		testkit.TextResponse("work complete"),
	)

	fmt.Println("=== 1) agent.Run (G15 main loop, scripted model) ===")
	res := a.Run(context.Background(), "do the demo task", 8, adapter.Func())
	fmt.Printf("  done=%v error=%q model_calls=%d final_text=%q\n",
		res.Done, res.Error, len(adapter.Calls()), lastAssistant(res))

	liveLog := a.Journal()
	liveEvents := liveLog.Events()
	fmt.Printf("  live journal path=%q events=%d\n", liveLog.Path(), len(liveEvents))
	for _, e := range liveEvents {
		fmt.Printf("    seq=%d kind=%q\n", e.Seq, e.Kind)
	}

	// Independent projection of the LIVE log.
	liveProj, err := journal.NewProjector(&smokeState{}, liveLog)
	if err != nil {
		fmt.Printf("  live replay error: %v\n", err)
		return
	}
	lp := liveProj.State().(*smokeState)

	fmt.Println("=== 2) reopen journal file + fresh replay (crash-recovery path) ===")
	reopened, err := journal.OpenLog(journalPath)
	if err != nil {
		fmt.Printf("  OpenLog error: %v\n", err)
		return
	}
	reEvents := reopened.Events()
	fmt.Printf("  reopened events=%d (live=%d)\n", len(reEvents), len(liveEvents))
	if len(reEvents) != len(liveEvents) {
		fmt.Println("  !! event count mismatch between disk and memory — persistence NOT faithful")
		return
	}
	for i := range reEvents {
		if reEvents[i].Kind != liveEvents[i].Kind || reEvents[i].Seq != liveEvents[i].Seq {
			fmt.Printf("  !! event %d mismatch: disk=%q/%d live=%q/%d\n",
				i, reEvents[i].Kind, reEvents[i].Seq, liveEvents[i].Kind, liveEvents[i].Seq)
			return
		}
	}
	reProj, err := journal.NewProjector(&smokeState{}, reopened)
	if err != nil {
		fmt.Printf("  reopened replay error: %v\n", err)
		return
	}
	rp := reProj.State().(*smokeState)

	fmt.Printf("  live     projection: turns=%d steps=%d\n", lp.Turns, lp.Steps)
	fmt.Printf("  reopened projection: turns=%d steps=%d\n", rp.Turns, rp.Steps)
	if lp.Turns == rp.Turns && lp.Steps == rp.Steps {
		fmt.Println("  OK: on-disk journal replays to an identical projection — 落盘/崩溃恢复验证通过")
	} else {
		fmt.Println("  !! projection mismatch after reopen — persistence broken")
	}
}

func lastAssistant(res agent.AgentRunResult) string {
	for i := len(res.Messages) - 1; i >= 0; i-- {
		if res.Messages[i].Role == types.RoleAssistant {
			return res.Messages[i].Content
		}
	}
	return ""
}
