package ctxsvc

import (
	"os"
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/conversation"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/recall"
)

// newTestService builds a service whose store points at a temp dir, so tests never
// touch the real ~/.tiancan-ai-ide/sessions.
func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	svc := NewService()
	svc.store = conversation.NewStore(dir)
	return svc
}

func TestAppendThenRecall(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/ctx"
	if _, err := svc.SessionAppend(root, "", "user", "把会话消息持久化到后端磁盘"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionAppend(root, "", "assistant", "已实现：使用 journal 追加式事件日志落盘"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionAppend(root, "", "user", "今天中午吃什么"); err != nil {
		t.Fatal(err)
	}

	hits, err := svc.SessionRecall(root, "", "历史记录要保存到硬盘上", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("recall returned nothing")
	}
	// 反证：语义召回必须命中"持久化"那条，而不是"吃什么"。
	if strings.Contains(hits[0].Text, "吃什么") {
		t.Fatalf("recall hit the irrelevant message: %+v", hits[0])
	}
	if !strings.Contains(hits[0].Text, "持久化") && !strings.Contains(hits[0].Text, "journal") {
		t.Fatalf("recall missed the relevant message: %+v", hits[0])
	}
}

func TestUserMessageAutoExtractsRedline(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/red"
	if _, err := svc.SessionAppend(root, "", "user", "记住：永远不要推送代码到远端"); err != nil {
		t.Fatal(err)
	}
	reds, err := svc.SessionRedlines(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reds) != 1 {
		t.Fatalf("red line not auto-captured: %+v", reds)
	}
	if reds[0].Text != "永远不要推送代码到远端" {
		t.Fatalf("red line text: %q", reds[0].Text)
	}
}

func TestRollbackTruncatesIndexToo(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/rb"
	seq1, _ := svc.SessionAppend(root, "", "user", "第一条消息关于持久化")
	_, _ = svc.SessionAppend(root, "", "user", "第二条消息关于部署上线")
	if err := svc.SessionRollback(root, "", seq1, "撤销"); err != nil {
		t.Fatal(err)
	}
	msgs, err := svc.SessionMessages(root, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages not truncated: %d", len(msgs))
	}
	// 反证：被撤销的内容不能还能被召回出来。
	hits, err := svc.SessionRecall(root, "", "部署上线", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if strings.Contains(h.Text, "部署上线") {
			t.Fatalf("rolled-back content still recallable: %+v", h)
		}
	}
	// 审计仍然保留
	audit, err := svc.SessionAudit(root, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range audit {
		if strings.Contains(a.Preview, "部署上线") {
			found = true
		}
	}
	if !found {
		t.Fatal("audit lost the rolled-back message")
	}
}

func TestPromptPreviewContainsRedlineAndLabelsRecall(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/pe"
	if _, err := svc.SessionAppend(root, "", "user", "记住：不要推送代码"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionAppend(root, "", "user", "我们要把聊天历史保存到磁盘"); err != nil {
		t.Fatal(err)
	}
	p, err := svc.PromptPreview(root, "", "聊天历史怎么存")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.System, "不要推送代码") {
		t.Fatalf("red line missing from assembled prompt:\n%s", p.System)
	}
	if p.System == "" {
		t.Fatal("empty system block")
	}
	if len(p.Messages) == 0 {
		t.Fatal("no verbatim messages in the assembled prompt")
	}
	if strings.Contains(p.System, "非当前对话") && !strings.Contains(p.System, "seq=") {
		t.Fatal("recall block must carry source seqs")
	}
}

func TestSessionInfoAndStats(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/info"
	// 过短的消息（"你好"）不进索引，这是有意的设计：它们没有检索价值。
	_, _ = svc.SessionAppend(root, "", "user", "帮我看一下这个项目的结构")
	info, err := svc.SessionOpen(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Messages != 1 {
		t.Fatalf("info message count: %d", info.Messages)
	}
	stats, err := svc.SessionStats(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if stats["indexed"] == 0 {
		t.Fatalf("nothing indexed: %+v", stats)
	}
}

func TestMultipleSessionsOfSameProject(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/multi"
	if _, err := svc.SessionAppend(root, "main", "user", "主线会话"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionAppend(root, "exp", "user", "实验分支会话"); err != nil {
		t.Fatal(err)
	}
	a, _ := svc.SessionMessages(root, "main", 0)
	b, _ := svc.SessionMessages(root, "exp", 0)
	if len(a) != 1 || a[0].Content != "主线会话" {
		t.Fatalf("main session polluted: %+v", a)
	}
	if len(b) != 1 || b[0].Content != "实验分支会话" {
		t.Fatalf("exp session polluted: %+v", b)
	}
}

func TestSegmentsAndRequirementsRoundTrip(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/seg"
	if _, err := svc.SessionOpenSegment(root, "", "实现登录"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionAddRequirement(root, "", "支持 JWT"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SessionCloseSegment(root, "", "登录完成"); err != nil {
		t.Fatal(err)
	}
	segs, err := svc.SessionSegments(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Status != "closed" || segs[0].Summary != "登录完成" {
		t.Fatalf("segment round trip failed: %+v", segs)
	}
	reqs, err := svc.SessionRequirements(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].Text != "支持 JWT" {
		t.Fatalf("requirement round trip failed: %+v", reqs)
	}
	if err := svc.SessionDoneRequirement(root, "", reqs[0].ID); err != nil {
		t.Fatal(err)
	}
	reqs, _ = svc.SessionRequirements(root, "")
	if reqs[0].Status != "done" {
		t.Fatal("requirement not marked done")
	}
}

func TestAddRedlineManually(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	root := "/proj/manual"
	if _, err := svc.SessionAddRedline(root, "", "不要删除配置文件"); err != nil {
		t.Fatal(err)
	}
	reds, _ := svc.SessionRedlines(root, "")
	if len(reds) != 1 || reds[0].Text != "不要删除配置文件" {
		t.Fatalf("manual red line failed: %+v", reds)
	}
}

func TestEmbeddingConfigRedactsKey(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	svc.SetEmbedding(embedCfg("https://api.example.com/v1", "text-embedding-3-small", "sk-secret"))
	cfg := svc.EmbeddingConfig()
	if cfg["apiKey"] != "***" {
		t.Fatalf("api key not redacted: %+v", cfg)
	}
	if cfg["remote"] != true {
		t.Fatalf("remote flag wrong: %+v", cfg)
	}
}

func TestInvalidEmbeddingConfigFallsBackToLocal(t *testing.T) {
	svc := newTestService(t)
	defer svc.Shutdown()
	svc.SetEmbedding(embedCfg("", "", ""))
	if cfg := svc.EmbeddingConfig(); cfg["remote"] != false {
		t.Fatalf("should stay local: %+v", cfg)
	}
	// 本地兜底仍必须能召回。
	if _, err := svc.SessionAppend(root(), "", "user", "会话持久化方案"); err != nil {
		t.Fatal(err)
	}
	hits, err := svc.SessionRecall(root(), "", "持久化", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("local fallback cannot recall")
	}
}

func TestShutdownFlushesIndex(t *testing.T) {
	dir := t.TempDir()
	svc := NewService()
	svc.store = conversation.NewStore(dir)
	root := "/proj/flush"
	if _, err := svc.SessionAppend(root, "", "user", "需要被持久化到索引的内容"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/" + conversation.ProjectHash(root) + "/main/vectors.json"); err != nil {
		t.Fatalf("index file not written on shutdown: %v", err)
	}
}

// --- helpers ---

func root() string { return "/proj/fallback" }

func embedCfg(base, model, key string) recall.RemoteConfig {
	return recall.RemoteConfig{BaseURL: base, Model: model, APIKey: key}
}
