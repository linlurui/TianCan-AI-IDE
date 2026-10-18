package memdb

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func openTest(tb testing.TB) *DB {
	tb.Helper()
	db, err := Open(filepath.Join(tb.TempDir(), "mem.db"))
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	return db
}

func TestBigramSearchChinese(t *testing.T) {
	db := openTest(t)
	defer db.Close()
	sid := "s1"
	mustWrite(db, sid, "项目使用 gin 框架开发后端接口")
	mustWrite(db, sid, "记得不要推送代码到远程仓库")
	mustWrite(db, sid, "下一步要实现语义召回功能")

	for _, q := range []string{"推送", "语义召回", "gin", "框架"} {
		hits, err := db.Search(sid, q, 10, 50, nil)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(hits) == 0 {
			t.Errorf("query %q: expected >=1 hit, got 0", q)
		}
	}
	// A phrase NOT in the corpus must not falsely match.
	hits, _ := db.Search(sid, "量子计算", 10, 50, nil)
	if len(hits) != 0 {
		t.Errorf("query 量子计算: expected 0 hits (corpus has none), got %d", len(hits))
	}
}

func TestTwoPhaseRerank(t *testing.T) {
	db := openTest(t)
	defer db.Close()
	sid := "s2"
	// Two docs share vocabulary but differ in meaning.
	mustWrite(db, sid, "会话消息要持久化到磁盘不能只放内存")
	mustWrite(db, sid, "前端组件的状态要持久化保存")

	// Query vector is closer to doc 1 ("持久化到磁盘"/"内存").
	q := []float32{ // 8-dim pseudo-embedding
		0.9, 0.1, 0.8, 0.2, 0.7, 0.0, 0.6, 0.3,
	}
	// Pin doc1's vector near q, doc2 far.
	_ = db.StoreVector(sid, 1, q)
	_ = db.StoreVector(sid, 2, []float32{0.0, 0.9, 0.1, 0.8, 0.0, 0.7, 0.2, 0.6})

	hits, err := db.Search(sid, "持久化", 1, 50, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Seq != 1 {
		t.Errorf("expected doc1 (seq=1) top after rerank, got %+v", hits)
	}
}

func TestSnapshotCheckpointIsolation(t *testing.T) {
	db := openTest(t)
	defer db.Close()
	ph := "proj"
	p := filepath.Join(t.TempDir(), "file.txt")

	// cp1 captures X; later cp2 captures Y. Rolling back to cp1 must restore X,
	// not the (buggy) shared/copied Y.
	mustWriteFile(t, p, "X")
	if _, err := db.SnapshotFile(ph, "cp1", p, []byte("X"), true); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCheckpoint(ph, "cp1", "before", ""); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, p, "Y")
	if _, err := db.SnapshotFile(ph, "cp2", p, []byte("Y"), true); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCheckpoint(ph, "cp2", "after", ""); err != nil {
		t.Fatal(err)
	}

	if err := db.RollbackToCheckpoint(ph, "cp1"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "X" {
		t.Errorf("rollback to cp1: expected file content 'X', got %q", string(got))
	}
}

func TestRedlinesAndLinks(t *testing.T) {
	db := openTest(t)
	defer db.Close()
	sid := "s3"
	_ = db.AddRedline(sid, "r1", "不要推送代码", "hard", 1)
	_ = db.AddRedline(sid, "r2", "必须写测试", "hard", 2)
	// Re-stating the same text must not duplicate (UNIQUE constraint).
	_ = db.AddRedline(sid, "r3", "不要推送代码", "soft", 3)
	reds, _ := db.Redlines(sid)
	if len(reds) != 2 {
		t.Errorf("expected 2 unique redlines, got %d", len(reds))
	}
	_ = db.AddLink("segA", "segB", LinkConflicts, 0.9, "contradicts earlier auth decision")
	links, _ := db.LinksTo("segB")
	if len(links) != 1 || links[0].Kind != LinkConflicts {
		t.Errorf("expected 1 conflict link to segB, got %+v", links)
	}
}

func TestTailSnapshotRoundTrip(t *testing.T) {
	db := openTest(t)
	defer db.Close()
	_, _ = db.OpenSession("ph", "s1", "title")
	if err := db.SetTail("ph", "s1", `{"folded":true}`, 42); err != nil {
		t.Fatal(err)
	}
	tj, seq, ok, err := db.GetTail("ph", "s1")
	if err != nil || !ok {
		t.Fatalf("GetTail: ok=%v err=%v", ok, err)
	}
	if tj != `{"folded":true}` || seq != 42 {
		t.Errorf("tail mismatch: %q / %d", tj, seq)
	}
}

// --- helpers ---

func mustWrite(db *DB, sid, content string) {
	seq, err := db.AppendEvent("ph", sid, "msg", `{"role":"user","content":`+quote(content)+`}`)
	if err != nil {
		panic(err)
	}
	if err := db.UpsertMessage(sid, seq, "user", content, "seg1", 0); err != nil {
		panic(err)
	}
}

func mustWriteFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func quote(s string) string {
	// minimal JSON string quoting for ASCII/plain text used in tests
	out := `"`
	for _, r := range s {
		if r == '\n' {
			out += `\n"`
		} else if r == '"' {
			out += `\"`
		} else {
			out += string(r)
		}
	}
	return out + `"`
}

var _ = math.Sqrt
