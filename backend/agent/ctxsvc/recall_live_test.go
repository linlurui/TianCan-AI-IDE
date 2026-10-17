package ctxsvc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/conversation"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/recall"
)

// TestRecallOnRealSession 跑在真实落盘会话上，用来回答"召回到底好不好用"。
// 默认 SKIP，需显式开启：TIANCAN_LIVE_RECALL=1 go test ./backend/agent/ctxsvc/ -run TestRecallOnRealSession -v
func TestRecallOnRealSession(t *testing.T) {
	if os.Getenv("TIANCAN_LIVE_RECALL") != "1" {
		t.Skip("set TIANCAN_LIVE_RECALL=1 to run against the real session store")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	base := filepath.Join(home, ".tiancan-ai-ide", "sessions")
	projects, err := os.ReadDir(base)
	if err != nil || len(projects) == 0 {
		t.Skip("no persisted sessions yet")
	}
	sessDir := filepath.Join(base, projects[0].Name(), "main")
	if _, err := os.Stat(filepath.Join(sessDir, "journal.jsonl")); err != nil {
		t.Skip("no journal in " + sessDir)
	}

	idx := recall.NewIndex(recall.NewHashingEmbedder(), filepath.Join(sessDir, "vectors.json"))
	if err := idx.Load(); err != nil {
		t.Fatalf("load index: %v", err)
	}
	if idx.Len() == 0 {
		// 索引丢失时从 journal 重算：这正是"向量可以从日志重建"的设计验证。
		lg, err := journal.OpenLog(filepath.Join(sessDir, "journal.jsonl"))
		if err != nil {
			t.Fatalf("open journal: %v", err)
		}
		proj, err := journal.NewProjector(conversation.NewSessionState("main", "", "", 0), lg)
		if err != nil {
			lg.Close()
			t.Fatalf("replay: %v", err)
		}
		st, _ := proj.State().(*conversation.SessionState)
		if len(st.Messages) == 0 {
			lg.Close()
			t.Skip("empty session")
		}
		if err := idx.AddBatch(indexItems(st)); err != nil {
			lg.Close()
			t.Fatal(err)
		}
		lg.Close()
	}
	t.Logf("真实会话索引：%d 条，目录=%s", idx.Len(), sessDir)

	queries := []string{
		"这个项目的 README 里写了什么",
		"会话消息怎么保存到磁盘",
		"之前说过不要做什么",
	}
	hitAny := false
	for _, q := range queries {
		hits, err := idx.Search(q, 3, nil)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		t.Logf("\n查询：%s", q)
		for _, h := range hits {
			t.Logf("   #%d %.0f%% (vec %.3f / kw %.3f) %s", h.Seq, h.Score*100, h.VecScore, h.KWScore, truncateForLog(h.Text, 90))
			hitAny = true
		}
		if len(hits) == 0 {
			t.Logf("   （无命中）")
		}
	}
	if !hitAny {
		t.Fatalf("recall found nothing on a %d-item real index", idx.Len())
	}
}

func truncateForLog(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
