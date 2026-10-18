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


// openTurn records the turn anchor row that conversation.Session would have
// created for this user message — the recorder only captures OPEN turns.
func openTurn(t *testing.T, db *memdb.DB, seq int64) {
	t.Helper()
	if err := db.RecordAnchor("h1", "main", memdb.AnchorRow{
		Seq: seq, Time: seq, Title: "turn", Kind: "turn",
		Summary: "turn", Status: "active", MsgSeq: seq, TurnEndSeq: 0,
	}); err != nil {
		t.Fatalf("record anchor: %v", err)
	}
}

// closeTurns mimics conversation.closeOpenTurns: every still-open turn is
// capped (closed) — in production its capture already ran synchronously by now.
func closeTurns(t *testing.T, db *memdb.DB) {
	t.Helper()
	if err := db.Exec(`UPDATE anchors SET turn_end_seq=(SELECT COALESCE(MAX(seq),0) FROM anchors) WHERE hash=? AND id=? AND kind='turn' AND turn_end_seq=0`, "h1", "main"); err != nil {
		t.Fatalf("close turns: %v", err)
	}
}

// beginTurn = conversation's "new user message" from the recorder's view:
// previous turns get closed, then this turn's anchor opens.
func beginTurn(t *testing.T, db *memdb.DB, rec *Recorder, seq int64) {
	t.Helper()
	closeTurns(t, db)
	openTurn(t, db, seq)
	rec.Begin(seq)
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

	beginTurn(t, db, rec, 10)
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
	beginTurn(t, db, rec, 10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 2 }\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	// 第二轮改同一行 return 2 → 3
	beginTurn(t, db, rec, 20)
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
	// 原因必须是"同一片段被后续修改占用"——"版本链断裂"意味着中间某轮
	// 的记录被污染过（曾经的真实 bug：审查老轮次会用当前工作区刷新它的记录）。
	if !strings.Contains(d1.Hunks[0].Reason, "后续修改已占用") {
		t.Fatalf("reason should be region-occupied, got %q", d1.Hunks[0].Reason)
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
	beginTurn(t, db, rec, 10)
	write(t, dir, "a.go", strings.Replace(base, "return 1", "return 2", 1))
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	// 第二轮在文件末尾加一个新函数（与 A 不相干）
	beginTurn(t, db, rec, 20)
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
	beginTurn(t, db, rec, 10)
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
	beginTurn(t, db, rec, 10)
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

// ── 复查修复的回归测试 ────────────────────────────────────────────────────

// TestApplyTwoHunksSameTurn: 一轮改了两处，先还原前面的段，行数变了，
// 后面的段必须还能定位并还原（坐标要跟着平移，不能报"文件已被其他修改"）。
func TestApplyTwoHunksSameTurn(t *testing.T) {
	dir := initGitRepo(t)
	base := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "ten lines")

	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	beginTurn(t, db, rec, 10)
	// 两处互不相干的改动：第 2 行删掉一行，第 9 行替换
	changed := "l1\nl3\nl4\nl5\nl6\nl7\nl8\nNINE\nl10\n"
	write(t, dir, "a.go", changed)
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}

	d, err := rec.Detail(10, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d (%+v)", len(d.Hunks), d.Hunks)
	}

	// 先应用靠前的片段（它删掉一行 → 后面所有行上移一格）
	if err := rec.ApplyHunk(10, "a.go", 0); err != nil {
		t.Fatalf("apply hunk 0: %v", err)
	}
	// 再应用靠后的片段：必须仍然定位得到
	if err := rec.ApplyHunk(10, "a.go", 1); err != nil {
		t.Fatalf("apply hunk 1 after hunk 0 shifted lines: %v", err)
	}
	if got := read(t, dir, "a.go"); got != base {
		t.Fatalf("both hunks applied should restore the original:\n%s", got)
	}

	// 重复应用同一段要挡住，并且说清楚是"已应用过"
	if err := rec.ApplyHunk(10, "a.go", 0); err == nil {
		t.Fatal("re-applying the same hunk must fail")
	} else if !strings.Contains(err.Error(), "已应用过") {
		t.Fatalf("reason should mention already-applied, got %q", err)
	}
}

// TestApplyFreezesVersionRecord: 还原过一段之后，轮子还开着时的再次捕获
// 不许把"用户还原后"的状态写成新的改后版本——那会换掉 diff 内容，
// 并让应用流水里的片段编号漂到别的段上。
func TestApplyFreezesVersionRecord(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	beginTurn(t, db, rec, 10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 2 }\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	if err := rec.ApplyHunk(10, "a.go", 0); err != nil {
		t.Fatal(err)
	}

	// 模拟用户继续操作：加一个无关文件后轮子仍未关闭，前端刷新列表 → 重新捕获。
	write(t, dir, "other.go", "package main\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}

	d, err := rec.Detail(10, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if d.AfterContent != "package main\n\nfunc A() int { return 2 }\n" {
		t.Fatalf("recorded after-content must stay what the AI wrote, got:\n%s", d.AfterContent)
	}
	if len(d.Hunks) != 1 || !d.Hunks[0].Applied {
		t.Fatalf("hunk 0 must still be the applied one, got %+v", d.Hunks)
	}
	if d.Hunks[0].Superseded && !strings.Contains(d.Hunks[0].Reason, "已应用过") {
		t.Fatalf("applied hunk should explain itself, got %q", d.Hunks[0].Reason)
	}
}

// TestTruncateReleasesBlobs: 回退要释放 blob 引用，否则 blobs 目录只增不减。
func TestTruncateReleasesBlobs(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	beginTurn(t, db, rec, 10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 9 }\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	files, _ := rec.Files(10)
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %+v", files)
	}
	blobsBefore := countBlobFiles(t, db)

	rec.TruncateAfter(5)

	if got := countBlobFiles(t, db); got != 0 {
		t.Fatalf("rollback should release every blob, %d files left (before: %d)", got, blobsBefore)
	}
	if n, _ := db.BlobRows(); n != 0 {
		t.Fatalf("blob rows should be gone, got %d", n)
	}
}

// TestGitAddedFileNotReportedDeleted: 开工时未跟踪、期间被 git add 的文件
// 仍在工作区，不能因为"不在未跟踪列表里"就被记成删除。
func TestGitAddedFileNotReportedDeleted(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	write(t, dir, "fresh.go", "package main\n\nfunc F() {}\n")
	beginTurn(t, db, rec, 10)
	git(t, dir, "add", "fresh.go") // 期间被 add：进了树，但没改内容
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	files, _ := rec.Files(10)
	for _, f := range files {
		if f.Path == "fresh.go" && f.Status == "D" {
			t.Fatalf("a git-added file must not be reported deleted: %+v", files)
		}
	}
}

// TestSnapshotTurnBaseFallback: git 项目取基点树失败时（这里用坏仓库模拟），
// 必须退化成全量 blob，而不是只记未跟踪文件把 tracked 改动丢掉。
func TestSnapshotTurnBaseFallback(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")
	// 伪装成 git 项目但 git 命令必失败（目录不是仓库）
	rec.git = true

	write(t, dir, "a.txt", "one\ntwo\n")
	beginTurn(t, db, rec, 10)
	write(t, dir, "a.txt", "one\nTWO\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}
	files, _ := rec.Files(10)
	if len(files) != 1 || files[0].Path != "a.txt" || files[0].Status != "M" {
		t.Fatalf("fallback must still capture tracked-style changes, got %+v", files)
	}
	d, _ := rec.Detail(10, "a.txt")
	if d.BeforeContent != "one\ntwo\n" {
		t.Fatalf("wrong before content: %q", d.BeforeContent)
	}
}

// TestClosedTurnIsNotRefreshed: 轮次关闭后，审查它不许用当前工作区重新差分——
// 那时文件已经是后续轮次的样子，刷新会把别人的改动写进这一轮的"改后版本"。
func TestClosedTurnIsNotRefreshed(t *testing.T) {
	dir := initGitRepo(t)
	db := openDB(t)
	rec := NewRecorder(dir, db, "h1", "main")

	beginTurn(t, db, rec, 10)
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 2 }\n")
	if err := rec.Capture(10); err != nil {
		t.Fatal(err)
	}

	// 模拟关轮：conversation 在下一轮用户发言时把 turn_end_seq 置上。
	if err := db.Exec(`UPDATE anchors SET turn_end_seq=? WHERE hash=? AND id=? AND seq=?`, 99, "h1", "main", 10); err != nil {
		t.Fatal(err)
	}

	// 之后工作区又被改（后续轮次的编辑）
	write(t, dir, "a.go", "package main\n\nfunc A() int { return 3 }\n\nfunc Later() {}\n")

	d, err := rec.Detail(10, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if d.AfterContent != "package main\n\nfunc A() int { return 2 }\n" {
		t.Fatalf("closed turn's after-version must stay frozen at close time, got:\n%s", d.AfterContent)
	}
	if strings.Contains(d.AfterContent, "Later") {
		t.Fatal("later edits must never leak into an old turn's record")
	}
}
