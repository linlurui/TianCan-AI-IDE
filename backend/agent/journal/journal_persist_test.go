package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// testState is a minimal State used by the persistence tests. It folds "turn" /
// "step" events and round-trips via PureJSON/LoadJSON, returning the same pointer
// (satisfying the same-pointer invariant).
type testState struct {
	Turns int `json:"turns"`
	Steps int `json:"steps"`
}

func newTestState() *testState { return &testState{} }

func (s *testState) Apply(e Event) State {
	switch e.Kind {
	case "turn":
		s.Turns++
	case "step":
		s.Steps++
	}
	return s
}

func (s *testState) PureJSON() ([]byte, error) { return json.Marshal(s) }

func (s *testState) LoadJSON(data []byte) error { return json.Unmarshal(data, s) }

// openPersisted creates a temp-file-backed log + projector for testing.
func openPersisted(t *testing.T) (*Projector, *Log) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jlog")
	log, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	proj, err := NewProjector(newTestState(), log)
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	return proj, log
}

// TestFileLogPersistsEventsAndState verifies the on-disk log is a faithful source
// of truth: after close + reopen the event count, anchors, and the folded state
// projection are all identical to the in-memory session.
func TestFileLogPersistsEventsAndState(t *testing.T) {
	proj, log := openPersisted(t)

	if _, err := proj.AppendAndApply("turn", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := proj.AppendAndApply("step", nil); err != nil {
		t.Fatal(err)
	}
	anch, err := proj.Anchor()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proj.AppendAndApply("turn", nil); err != nil {
		t.Fatal(err)
	}

	before, err := proj.State().PureJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := log.Len()
	wantAnchors := len(log.Anchors())
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen the same file and rebuild the projector from scratch.
	log2, err := OpenLog(log.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer log2.Close()
	proj2, err := NewProjector(newTestState(), log2)
	if err != nil {
		t.Fatalf("rebuild projector: %v", err)
	}

	if got := log2.Len(); got != wantEvents {
		t.Fatalf("event count after reopen = %d, want %d", got, wantEvents)
	}
	if got := len(log2.Anchors()); got != wantAnchors {
		t.Fatalf("anchor count after reopen = %d, want %d", got, wantAnchors)
	}
	if got, ok := log2.NewestAnchor(); !ok || got.Seq != anch.Seq {
		t.Fatalf("newest anchor after reopen = %+v (ok=%v), want seq %d", got, ok, anch.Seq)
	}
	after, err := proj2.State().PureJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("state projection diverged after reopen:\n before=%s\n after =%s", before, after)
	}
}

// TestFileLogRecoversAnchorAfterReopen checks that a checkpoint taken before close
// is still recoverable from the on-disk log after reopen.
func TestFileLogRecoversAnchorAfterReopen(t *testing.T) {
	proj, log := openPersisted(t)
	proj.AppendAndApply("turn", nil)
	proj.AppendAndApply("step", nil)
	anch, _ := proj.Anchor()
	proj.AppendAndApply("turn", nil) // one more event after the anchor
	before, _ := proj.State().PureJSON()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	log2, err := OpenLog(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	proj2, err := NewProjector(newTestState(), log2)
	if err != nil {
		t.Fatal(err)
	}
	if err := proj2.RecoverTo(anch.Seq); err != nil {
		t.Fatalf("RecoverTo after reopen: %v", err)
	}
	after, _ := proj2.State().PureJSON()
	if string(before) != string(after) {
		t.Fatalf("RecoverTo(anchor) state diverged:\n before=%s\n after =%s", before, after)
	}
}

// TestFileLogPrunedStillRecoversState proves the file is the source of truth: even
// when the in-memory log is pruned, reopening replays the full event log and the
// folded state is identical (only the raw event slice differs, which is expected
// for an append-only log).
func TestFileLogPrunedStillRecoversState(t *testing.T) {
	proj, log := openPersisted(t)
	proj.AppendAndApply("turn", nil) // e1
	proj.AppendAndApply("step", nil) // e2
	a1, _ := proj.Anchor()           // @e2, covers e1,e2
	proj.AppendAndApply("turn", nil) // e3
	proj.AppendAndApply("step", nil) // e4
	_, _ = proj.Anchor()             // @e4, covers e3,e4
	if err := log.DropAnchor(a1.Seq); err != nil {
		t.Fatal(err)
	}
	if dropped := log.Prune(); dropped == 0 {
		t.Fatal("expected pruned events before close, got 0")
	}
	before, _ := proj.State().PureJSON()
	prunedLen := log.Len()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	log2, err := OpenLog(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	proj2, err := NewProjector(newTestState(), log2)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := proj2.State().PureJSON()
	if string(before) != string(after) {
		t.Fatalf("state diverged after prune+reopen:\n before=%s\n after =%s", before, after)
	}
	// Disk keeps the whole append-only log; the pruned events are still there.
	if log2.Len() <= prunedLen {
		t.Fatalf("disk log should retain pruned events: got len %d, pruned memory len %d", log2.Len(), prunedLen)
	}
}

// TestFileLogToleratesTornTail writes a valid event line followed by a truncated,
// newline-less final line (simulating an interrupted write) and asserts the log
// loads the complete record and silently skips the torn tail.
func TestFileLogToleratesTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.jlog")
	valid := `{"t":"ev","e":{"seq":1,"kind":"turn","t":1,"refs":0}}` + "\n"
	torn := `{"t":"ev","e":{"seq":2,"kind":"step"` // no closing brace, no newline
	if err := os.WriteFile(path, []byte(valid+torn), 0o644); err != nil {
		t.Fatal(err)
	}
	log, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog on torn file: %v", err)
	}
	defer log.Close()
	if got := log.Len(); got != 1 {
		t.Fatalf("expected 1 event loaded from torn file, got %d", got)
	}
	if got := log.HeadSeq(); got != 1 {
		t.Fatalf("expected head seq 1, got %d", got)
	}
}

// TestInMemoryLogUntouched ensures NewLog (no file) keeps its prior behavior and
// the new persistence methods are safe no-ops.
func TestInMemoryLogUntouched(t *testing.T) {
	log := NewLog()
	if log.Path() != "" {
		t.Fatalf("in-memory log Path() = %q, want empty", log.Path())
	}
	if err := log.Sync(); err != nil {
		t.Fatalf("Sync on in-memory log: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close on in-memory log: %v", err)
	}
	if _, err := log.Append("turn", nil); err != nil {
		t.Fatal(err)
	}
	if log.Len() != 1 {
		t.Fatalf("in-memory append broken: len=%d", log.Len())
	}
}

// TestFileLogConcurrentAppendNoRace hammers a file-backed log from many goroutines
// to confirm writes are serialized (no interleaved / corrupt lines). Run under
// -race.
func TestFileLogConcurrentAppendNoRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conc.jlog")
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := log.Append("step", nil); err != nil {
				t.Errorf("concurrent append: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	log2, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	if got := log2.Len(); got != n {
		t.Fatalf("after concurrency + reopen: len=%d, want %d", got, n)
	}
}
