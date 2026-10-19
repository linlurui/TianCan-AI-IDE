package ai

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSweepZCodeLocksRemovesOnlyStale: a lock left by a killed zcode must be
// cleared (otherwise every later run fails — see the file header), while a lock
// that was just taken must be left alone.
func TestSweepZCodeLocksRemovesOnlyStale(t *testing.T) {
	root := t.TempDir()
	old := zcodeRuntimeRootOverride
	zcodeRuntimeRootOverride = root
	t.Cleanup(func() { zcodeRuntimeRootOverride = old })

	endpoint := filepath.Join(root, "provider", "darwin-aarch64", "0.0.0-dev", "endpoint-abc")
	if err := os.MkdirAll(endpoint, 0o755); err != nil {
		t.Fatal(err)
	}
	staleLock := filepath.Join(endpoint, "zcode-builtin-refresh.json.lock")
	if err := os.Mkdir(staleLock, 0o755); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-time.Hour)
	if err := os.Chtimes(staleLock, aged, aged); err != nil {
		t.Fatal(err)
	}
	freshLock := filepath.Join(endpoint, "zcode-builtin.json.lock")
	if err := os.Mkdir(freshLock, 0o755); err != nil {
		t.Fatal(err)
	}
	// Not-a-lock files must survive even when old.
	state := filepath.Join(endpoint, "zcode-builtin.json")
	if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(state, aged, aged); err != nil {
		t.Fatal(err)
	}

	if got := sweepZCodeLocks(); got != 1 {
		t.Fatalf("expected exactly one stale lock removed, got %d", got)
	}
	if _, err := os.Stat(staleLock); !os.IsNotExist(err) {
		t.Fatal("stale lock dir survived the sweep")
	}
	if _, err := os.Stat(freshLock); err != nil {
		t.Fatal("fresh lock must be left for the running zcode:", err)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatal("non-lock state file was deleted:", err)
	}
}

// TestSweepZCodeLocksNoopWithoutRuntime: no ~/.zcode at all must not panic or
// report anything (fresh machine / zcode never installed).
func TestSweepZCodeLocksNoopWithoutRuntime(t *testing.T) {
	old := zcodeRuntimeRootOverride
	zcodeRuntimeRootOverride = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { zcodeRuntimeRootOverride = old })
	if got := sweepZCodeLocks(); got != 0 {
		t.Fatalf("got %d removals for a missing runtime dir", got)
	}
}

// TestClearZCodeLocksInError: when zcode names the blocker itself we clear
// exactly that path — and refuse paths outside the runtime dir.
func TestClearZCodeLocksInError(t *testing.T) {
	root := t.TempDir()
	old := zcodeRuntimeRootOverride
	zcodeRuntimeRootOverride = root
	t.Cleanup(func() { zcodeRuntimeRootOverride = old })

	lock := filepath.Join(root, "provider", "endpoint-x", "zcode-builtin-refresh.json.lock")
	if err := os.MkdirAll(lock, 0o755); err != nil {
		t.Fatal(err)
	}

	msg := "zcode CLI 执行失败: exit status 1: ZCode Built-in 刷新失败: EEXIST: " +
		"file already exists, mkdir '" + lock + "'"
	if _, err := os.Stat(lock); err != nil {
		t.Fatal(err)
	}
	if cleared := clearZCodeLocksInError(msg); len(cleared) != 1 {
		t.Fatalf("expected the reported lock to be cleared, got %v", cleared)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("reported lock survived")
	}

	// A path outside the runtime dir must never be touched, whatever the error says.
	if paths := extractZCodeLockPaths("EEXIST: mkdir '/etc/passwd.lock'"); len(paths) != 0 {
		t.Fatalf("must not consider paths outside the runtime dir: %v", paths)
	}
	if paths := extractZCodeLockPaths("everything is fine"); len(paths) != 0 {
		t.Fatalf("unrelated errors must yield no paths: %v", paths)
	}
}
