package ralph

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRollbackSurvivesRestart proves the central fix: file snapshots and
// checkpoints now live in the unified memdb, so a user who has NOT installed
// git can quit the app and still roll back after reopening it. Previously the
// state was a pure in-memory map and was discarded on every restart.
func TestRollbackSurvivesRestart(t *testing.T) {
	orig := memdbPath
	t.Cleanup(func() { memdbPath = orig })

	dbDir := t.TempDir()
	memdbPath = func() string { return filepath.Join(dbDir, "store.sqlite") }

	root := t.TempDir()
	target := "main.go"
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(root, target), []byte(original), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Session #1: agent takes a snapshot, then the file is mutated on disk.
	rm1 := NewRollbackManager(root)
	if !rm1.enabled {
		t.Skip("rollback disabled via env")
	}
	if cp := rm1.CreateCheckpoint("init", "before first edit"); cp == "" {
		t.Fatal("CreateCheckpoint returned empty id")
	}
	if err := rm1.Snapshot(target); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// Simulate the agent editing the file.
	edited := "package main\n\nfunc main() { println(\"edited\") }\n"
	if err := os.WriteFile(filepath.Join(root, target), []byte(edited), 0o644); err != nil {
		t.Fatalf("edit file: %v", err)
	}

	// Close session #1 completely (free its DB handle) — like quitting the app.
	if rm1.db != nil {
		_ = rm1.db.Close()
		rm1.db = nil
	}

	// Session #2: a fresh manager opens the SAME store — the restart.
	rm2 := NewRollbackManager(root)
	// Restore state from disk by re-snapshotting the baseline? No — the whole
	// point is that the snapshot taken in session #1 is still readable here.
	if rm2.db != nil {
		defer rm2.db.Close()
	}

	cps := rm2.GetCheckpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint after restart, got %d", len(cps))
	}
	if len(cps[0].Snapshots) != 1 {
		t.Fatalf("expected 1 snapshot carried over, got %d", len(cps[0].Snapshots))
	}
	if got := cps[0].Snapshots[0].Content; got != original {
		t.Fatalf("snapshot content not persisted across restart:\n got: %q\nwant: %q", got, original)
	}

	// The on-disk file differs from the snapshot → must be reported as modified.
	mods := rm2.GetModifiedFiles()
	if len(mods) != 1 || !mods[0].Modified {
		t.Fatalf("expected the edited file to be reported modified, got %+v", mods)
	}

	// Roll back — this must restore the ORIGINAL content.
	if err := rm2.Rollback(); err != nil {
		t.Fatalf("Rollback after restart: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		t.Fatalf("read after rollback: %v", err)
	}
	if string(got) != original {
		t.Fatalf("rollback did not restore original content:\n got: %q\nwant: %q", string(got), original)
	}
}
