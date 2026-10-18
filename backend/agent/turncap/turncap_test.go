package turncap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/memdb"
)

func openDB(t *testing.T) *memdb.DB {
	t.Helper()
	db, err := memdb.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("open memdb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initGitRepo makes a real git repo with one commit so tree diffs have a HEAD.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@t")
	git(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ── diff ---------------------------------------------------------------------

func TestHunksBasic(t *testing.T) {
	before := "l1\nl2\nl3\nl4\nl5\n"
	after := "l1\nl2\nX\nl4\nl5\n"
	h := Hunks(before, after)
	if len(h) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(h))
	}
	if h[0].BeforeStart != 2 || h[0].BeforeEnd != 3 || h[0].AfterStart != 2 || h[0].AfterEnd != 3 {
		t.Fatalf("wrong range: %+v", h[0])
	}
	if len(h[0].Before) != 1 || h[0].Before[0] != "l3" || h[0].After[0] != "X" {
		t.Fatalf("wrong content: %+v", h[0])
	}
}

func TestHunksInsertShiftsLater(t *testing.T) {
	before := "a\nb\nc\n"
	after := "a\nINS\nb\nc\n"
	h := Hunks(before, after)
	if len(h) != 1 || h[0].Delta() != 1 {
		t.Fatalf("want one insertion hunk, got %+v", h)
	}
	if h[0].BeforeStart != 1 || h[0].BeforeEnd != 1 || h[0].AfterStart != 1 || h[0].AfterEnd != 2 {
		t.Fatalf("wrong insertion range: %+v", h[0])
	}
}

// ── git capture --------------------------------------------------------------

func TestCaptureGitTurnAndApply(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	rec.Begin(10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 2 }\n")
	write(t, dir, "new.go", "package main\n\nfunc N() {}\n")
	if err := rec.Capture(10); err != nil {
		t.Fatalf("capture: %v", err)
	}

	files, err := rec.Files(10)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 changed files, got %d (%+v)", len(files), files)
	}
	byPath := map[string]FileVersion{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if byPath["a.go"].Status != "M" || byPath["new.go"].Status != "A" {
		t.Fatalf("wrong statuses: %+v", byPath)
	}

	d, err := rec.Detail(10, "a.go")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(d.Hunks) != 1 || d.Hunks[0].Superseded {
		t.Fatalf("want one applicable hunk, got %+v", d.Hunks)
	}

	// 应用这一段 → 工作区回到改前
	if err := rec.ApplyHunk(10, "a.go", 0); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := read(t, dir, "a.go"); got != "package main\n\nfunc A() int { return 1 }\n" {
		t.Fatalf("apply did not restore old content:\n%s", got)
	}
	// 应用记录在案
	d2, _ := rec.Detail(10, "a.go")
	if !d2.Hunks[0].Applied {
		t.Fatal("hunk should be marked applied")
	}
}

func TestSupersedeSameRegion(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	// 第一轮改 return 1 → 2
	rec.Begin(10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 2 }\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	// 第二轮改同一行 return 2 → 3
	rec.Begin(20)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 3 }\n")
	if err := rec.Capture(20); err != nil {
		t.Fatal(err)
	}

	d1, _ := rec.Detail(10, "a.go")
	if len(d1.Hunks) != 1 || !d1.Hunks[0].Superseded {
		t.Fatalf("turn 10 hunk should be superseded, got %+v", d1.Hunks)
	}
	if d1.Hunks[0].Reason == "" {
		t.Fatal("superseded hunk must explain why")
	}
	// 服务端同样要挡住
	if err := rec.ApplyHunk(10, "a.go", 0); err == nil {
		t.Fatal("applying a superseded hunk must fail")
	}
	if got := read(t, dir, "a.go"); !strings.Contains(got, "return 3") {
		t.Fatalf("file must be untouched, got:\n%s", got)
	}

	// 第二轮自己可以应用
	d2, _ := rec.Detail(20, "a.go")
	if len(d2.Hunks) != 1 || d2.Hunks[0].Superseded {
		t.Fatalf("turn 20 hunk should be applicable, got %+v", d2.Hunks)
	}
	if err := rec.ApplyHunk(20, "a.go", 0); err != nil {
		t.Fatalf("apply turn 20: %v", err)
	}
	if got := read(t, dir, "a.go"); !strings.Contains(got, "return 2") {
		t.Fatalf("apply should restore turn-20 before-content:\n%s", got)
	}
}

func TestLaterEditElsewhereStillApplies(t *testing.T) {
	dir := initGitRepo(t)
	base := "package main\n\nfunc A() int { return 1 }\n\nfunc B() int { return 10 }\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "two funcs")

	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	// 第一轮改 A
	rec.Begin(10)
	write(t, dir, "a.go", strings.Replace(base, "return 1", "return 2", 1))
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	// 第二轮在文件末尾加一个新函数（与 A 不相干）
	rec.Begin(20)
	write(t, dir, "a.go", strings.Replace(base, "return 1", "return 2", 1)+"\nfunc C() int { return 30 }\n")
	if err := rec.Capture(20); err != nil {
		t.Fatal(err)
	}

	d1, _ := rec.Detail(10, "a.go")
	if len(d1.Hunks) != 1 || d1.Hunks[0].Superseded {
		t.Fatalf("turn 10 hunk should still apply (later edit is elsewhere), got %+v", d1.Hunks)
	}
	if err := rec.ApplyHunk(10, "a.go", 0); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := read(t, dir, "a.go")
	if !strings.Contains(got, "return 1") || !strings.Contains(got, "func C()") {
		t.Fatalf("apply should revert only A's line and keep C:\n%s", got)
	}
}

// ── no-git capture -----------------------------------------------------------

func TestCaptureSnapWithoutGit(t *testing.T) {
	dir := t.TempDir() // 没有 git
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")
	if rec.git {
		t.Skip("temp dir unexpectedly inside a git repo")
	}

	write(t, dir, "a.txt", "one\ntwo\nthree\n")
	rec.Begin(10)
	write(t, dir, "a.txt", "one\nTWO\nthree\n")
	write(t, dir, "b.txt", "brand new\n")
	if err := rec.Capture(10); err != nil {
		t.Fatalf("capture: %v", err)
	}

	files, _ := rec.Files(10)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %+v", files)
	}
	d, err := rec.Detail(10, "a.txt")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(d.Hunks) != 1 || d.Hunks[0].Superseded {
		t.Fatalf("want one applicable hunk, got %+v", d.Hunks)
	}
	if d.BeforeContent != "one\ntwo\nthree\n" {
		t.Fatalf("wrong before content: %q", d.BeforeContent)
	}
	if err := rec.ApplyHunk(10, "a.txt", 0); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := read(t, dir, "a.txt"); got != "one\ntwo\nthree\n" {
		t.Fatalf("apply did not restore:\n%s", got)
	}
}

// ── housekeeping -------------------------------------------------------------

func TestTruncateAfterDropsVersions(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")
	rec.Begin(10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 9 }\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	if n, _ := rec.Files(10); len(n) != 1 {
		t.Fatalf("want 1 file, got %d", len(n))
	}
	rec.TruncateAfter(5)
	files, _ := rec.Files(10)
	if len(files) != 0 {
		t.Fatalf("versions after rollback target must be gone, got %+v", files)
	}
}

func TestCaptureWithoutBaseIsNoop(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")
	// 没有 Begin 过的轮次（功能上线前的历史轮次）：抓不到基点，静默跳过。
	if err := rec.Capture(99); err != nil {
		t.Fatalf("capture should be a no-op, got %v", err)
	}
	files, err := rec.Files(99)
	if err != nil || len(files) != 0 {
		t.Fatalf("want no files, got %+v (%v)", files, err)
	}
}
