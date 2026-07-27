package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardFileRestoresTrackedAndRemovesNewFiles(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "TianCan Test")
	runGit(t, repo, "config", "user.email", "test@tiancan.local")
	tracked := filepath.Join(repo, "tracked.txt")
	writeTestFile(t, tracked, "original\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "initial")

	service := &Service{}
	for _, staged := range []bool{false, true} {
		writeTestFile(t, tracked, "changed\n")
		if staged {
			runGit(t, repo, "add", "tracked.txt")
		}
		if err := service.DiscardFile(repo, "tracked.txt"); err != nil {
			t.Fatalf("discard tracked file (staged=%v): %v", staged, err)
		}
		content, err := os.ReadFile(tracked)
		if err != nil || string(content) != "original\n" {
			t.Fatalf("tracked file was not restored (staged=%v): %q, %v", staged, content, err)
		}
	}

	newFile := filepath.Join(repo, "new.txt")
	writeTestFile(t, newFile, "new\n")
	runGit(t, repo, "add", "new.txt")
	if err := service.DiscardFile(repo, "new.txt"); err != nil {
		t.Fatalf("discard staged new file: %v", err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatalf("expected staged new file to be removed, stat error: %v", err)
	}

	if err := os.Remove(tracked); err != nil {
		t.Fatal(err)
	}
	if err := service.DiscardFile(repo, "tracked.txt"); err != nil {
		t.Fatalf("restore deleted tracked file: %v", err)
	}
	if content, err := os.ReadFile(tracked); err != nil || string(content) != "original\n" {
		t.Fatalf("deleted tracked file was not restored: %q, %v", content, err)
	}
}

func TestDiscardFileRejectsPathsOutsideRepository(t *testing.T) {
	service := &Service{}
	for _, path := range []string{"", "../outside.txt", "/tmp/outside.txt"} {
		if err := service.DiscardFile(t.TempDir(), path); err == nil {
			t.Fatalf("expected unsafe path %q to be rejected", path)
		}
	}
}
