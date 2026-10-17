package conversation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- P0: 持久化 -------------------------------------------------------------

func TestSessionPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sess, err := st.Open("/proj/demo")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := sess.AppendMessage(RoleUser, "你好，帮我做会话持久化"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := sess.AppendMessage(RoleAssistant, "好的，已经落到后端了"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 重新打开：消息必须还在（这就是"消息只在前端内存可是不行的"的反证）。
	st2 := NewStore(dir)
	defer st2.Close()
	sess2, err := st2.Open("/proj/demo")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	msgs := sess2.State().Messages
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages after reopen, got %d", len(msgs))
	}
	if msgs[0].Content != "你好，帮我做会话持久化" || msgs[0].Role != RoleUser {
		t.Fatalf("message 0 mismatch: %+v", msgs[0])
	}
	if msgs[1].Role != RoleAssistant {
		t.Fatalf("message 1 role mismatch: %+v", msgs[1])
	}
	if msgs[0].Seq >= msgs[1].Seq {
		t.Fatalf("seq must be monotonic: %d >= %d", msgs[0].Seq, msgs[1].Seq)
	}
}

func TestProjectScopedSessionsAreIsolated(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	a, _ := st.Open("/proj/a")
	b, _ := st.Open("/proj/b")
	if _, err := a.AppendMessage(RoleUser, "A 项目的需求"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendMessage(RoleUser, "B 项目的需求"); err != nil {
		t.Fatal(err)
	}
	if got := b.State().Messages; len(got) != 1 || got[0].Content != "B 项目的需求" {
		t.Fatalf("project B leaked/duplicated: %+v", got)
	}
	if ProjectHash("/proj/a") == ProjectHash("/proj/b") {
		t.Fatal("project hash collision")
	}
	// 目录布局：sessions/<hash>/<id>/journal.jsonl
	if _, err := os.Stat(filepath.Join(dir, ProjectHash("/proj/a"), MainSessionID, "journal.jsonl")); err != nil {
		t.Fatalf("journal file missing: %v", err)
	}
}

func TestRollbackTruncatesStateButKeepsAuditTrail(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/rb")
	seq1, _ := sess.AppendMessage(RoleUser, "第一条")
	_, _ = sess.AppendMessage(RoleAssistant, "第二条")
	_, _ = sess.AppendMessage(RoleUser, "第三条，说错了")

	if err := sess.Rollback(seq1, "用户撤销"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := len(sess.State().Messages); got != 1 {
		t.Fatalf("after rollback want 1 message, got %d", got)
	}
	// 反证：审计流水必须仍然看得到被撤销的两条，历史不可被悄悄抹掉。
	audit := sess.Audit(0, 0)
	found := 0
	for _, a := range audit {
		if a.Kind == KindMessage && strings.Contains(a.Preview, "第三条") {
			found++
		}
	}
	if found == 0 {
		t.Fatalf("audit trail lost the rolled-back message: %+v", audit)
	}
	hasRollback := false
	for _, a := range audit {
		if a.Kind == KindRollback {
			hasRollback = true
		}
	}
	if !hasRollback {
		t.Fatal("audit trail missing the rollback event itself")
	}
}

func TestRollbackRejectsFutureTarget(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/rb2")
	seq, _ := sess.AppendMessage(RoleUser, "hi")
	if err := sess.Rollback(seq+999, ""); err == nil {
		t.Fatal("expected error rolling back past head")
	}
}

func TestAnchorThenReopenRecovers(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sess, _ := st.Open("/proj/anchor")
	for i := 0; i < 5; i++ {
		if _, err := sess.AppendMessage(RoleUser, "消息 "+string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
	}
	info, err := sess.DropAnchor()
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if info.Messages != 5 {
		t.Fatalf("anchor should cover 5 messages, got %d", info.Messages)
	}
	if len(sess.Anchors()) != 1 {
		t.Fatalf("want 1 anchor, got %d", len(sess.Anchors()))
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2 := NewStore(dir)
	defer st2.Close()
	sess2, err := st2.Open("/proj/anchor")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sess2.State().Messages); got != 5 {
		t.Fatalf("recovery lost messages: %d", got)
	}
}

func TestStateJSONRoundTripIsStable(t *testing.T) {
	st := NewSessionState("s1", "/p", "标题", 1000)
	st.Messages = append(st.Messages, Msg{Seq: 1, Role: RoleUser, Content: "内容", Tokens: 2})
	st.Redlines = append(st.Redlines, Redline{ID: "r1", Text: "不要推送", Severity: "hard"})
	a, err := st.PureJSON()
	if err != nil {
		t.Fatal(err)
	}
	var st2 SessionState
	if err := st2.LoadJSON(a); err != nil {
		t.Fatal(err)
	}
	b, err := st2.PureJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("round trip not stable:\n%s\n%s", a, b)
	}
}

// --- P1: 段落 / 需求 / 红线 -------------------------------------------------

func TestSegmentCloseSummarisesAndDropsVerbatim(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/seg")
	if _, err := sess.OpenSegment("实现登录"); err != nil {
		t.Fatal(err)
	}
	_, _ = sess.AppendMessage(RoleUser, "帮我实现登录页面")
	_, _ = sess.AppendMessage(RoleAssistant, "已完成，使用 JWT 鉴权")
	if err := sess.CloseSegment("登录页完成，JWT 鉴权"); err != nil {
		t.Fatal(err)
	}
	state := sess.State()
	if len(state.Segments) != 1 {
		t.Fatalf("want 1 segment, got %d", len(state.Segments))
	}
	if state.Segments[0].Status != "closed" {
		t.Fatalf("segment should be closed: %+v", state.Segments[0])
	}
	if state.Segments[0].Summary != "登录页完成，JWT 鉴权" {
		t.Fatalf("summary mismatch: %q", state.Segments[0].Summary)
	}
	if _, ok := state.ActiveSegment(); ok {
		t.Fatal("no segment should be active after closing")
	}
}

func TestAutoSegmentKeepsActiveSliceBounded(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/auto")
	sess.SetAutoSegmentTokens(200)
	if _, err := sess.OpenSegment("第一段"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("这是一句用于撑爆预算的中文内容。", 20)
	for i := 0; i < 6; i++ {
		if _, err := sess.AppendMessage(RoleUser, long); err != nil {
			t.Fatal(err)
		}
	}
	state := sess.State()
	if len(state.Segments) < 2 {
		t.Fatalf("auto segmentation did not kick in: %d segments", len(state.Segments))
	}
	active, ok := state.ActiveSegment()
	if !ok {
		t.Fatal("expected an active segment after auto split")
	}
	if active.Tokens >= 200 {
		t.Fatalf("active segment should be under budget, got %d", active.Tokens)
	}
}

func TestRedlineDedupAndOrder(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/red")
	if _, err := sess.AddRedline("不要推送代码", "user", "hard"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AddRedline("不要推送代码", "extracted", "hard"); err != nil {
		t.Fatal(err)
	}
	if got := len(sess.State().Redlines); got != 1 {
		t.Fatalf("duplicate redline stored: %d", got)
	}
	if _, err := sess.AddRequirement("实现会话持久化", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AddRequirement("实现会话持久化", "user"); err != nil {
		t.Fatal(err)
	}
	if got := len(sess.State().Requirement); got != 1 {
		t.Fatalf("duplicate requirement stored: %d", got)
	}
}

// --- P1: 红线自动抽取 -------------------------------------------------------

func TestExtractRedlines(t *testing.T) {
	cases := []struct {
		in   string
		want string
		hard bool
	}{
		{"记住：永远不要推送代码到远端", "永远不要推送代码到远端", true},
		{"不要删我的配置", "不要删我的配置", true},
		{"必须先跑测试再提交", "必须先跑测试再提交", true},
		{"注意上下文超限的问题", "注意上下文超限的问题", false},
		{"never push to remote", "never push to remote", true},
		{"you must run the tests first", "you must run the tests first", true},
	}
	for _, c := range cases {
		ex := Extract(c.in)
		if len(ex.Redlines) == 0 {
			t.Fatalf("no redline extracted from %q", c.in)
		}
		got := ex.Redlines[0]
		if got.Text != c.want {
			t.Errorf("extract %q: got %q want %q", c.in, got.Text, c.want)
		}
		if (got.Severity == "hard") != c.hard {
			t.Errorf("extract %q: severity %q", c.in, got.Severity)
		}
	}
}

func TestExtractIgnoresNoise(t *testing.T) {
	noise := []string{
		"func main() { fmt.Println(\"hi\") }  // must not be a redline",
		"这是什么？",
		"好的",
	}
	for _, n := range noise {
		if ex := Extract(n); len(ex.Redlines) != 0 {
			t.Errorf("noise %q produced redline %+v", n, ex.Redlines)
		}
	}
}

func TestExtractRequirements(t *testing.T) {
	ex := Extract("我要一个项目一个会话，另外帮我加上语义召回")
	if len(ex.Requirements) == 0 {
		t.Fatalf("no requirement extracted: %+v", ex)
	}
	found := false
	for _, r := range ex.Requirements {
		if strings.Contains(r.Text, "会话") {
			found = true
		}
	}
	if !found {
		t.Fatalf("requirement not found in %+v", ex.Requirements)
	}
}

func TestAutoIngestPersistsOnlyConfident(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/ingest")
	applied, err := sess.AutoIngest("记住：不要推送。另外最好用中文回复。我想加个按钮", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Redlines) != 1 {
		t.Fatalf("want 1 hard redline applied, got %+v", applied.Redlines)
	}
	if applied.Redlines[0].Text != "不要推送" {
		t.Fatalf("redline text: %q", applied.Redlines[0].Text)
	}
	// 红线必须真的进了状态，而不只是返回给调用方。
	if len(sess.State().Redlines) != 1 {
		t.Fatalf("redline not persisted: %+v", sess.State().Redlines)
	}
}

// --- 审计 -------------------------------------------------------------------

func TestAuditRendersEveryKind(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/audit")
	_, _ = sess.AppendMessage(RoleUser, "做会话")
	_, _ = sess.OpenSegment("段落一")
	_, _ = sess.AddRedline("不要推送", "user", "hard")
	_, _ = sess.AddRequirement("持久化", "user")
	_ = sess.CloseSegment("完成")
	_ = sess.RecordRecall("会话", []int64{1}, []float64{0.9})

	audit := sess.Audit(0, 0)
	kinds := map[string]bool{}
	for _, a := range audit {
		kinds[a.Kind] = true
		if a.Preview == "" && a.Kind != KindSegOpen {
			t.Errorf("empty preview for %s", a.Kind)
		}
	}
	for _, want := range []string{KindMessage, KindSegOpen, KindSegClose, KindRedAdd, KindReqAdd, KindRecallHit} {
		if !kinds[want] {
			t.Errorf("audit missing kind %s", want)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("empty should be 0 tokens")
	}
	cn := EstimateTokens("中文")
	if cn < 2 || cn > 3 {
		t.Fatalf("chinese estimate off: %d", cn)
	}
	if EstimateTokens("hello world") < 2 {
		t.Fatal("latin estimate too low")
	}
}

func TestListSessionsIncludesOnDiskOnly(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sess, _ := st.Open("/proj/list")
	_, _ = sess.AppendMessage(RoleUser, "第一条")
	if err := sess.SetTitle("我的会话"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2 := NewStore(dir)
	defer st2.Close()
	list, err := st2.ListSessions("/proj/list")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 session, got %d", len(list))
	}
	if list[0].Title != "我的会话" {
		t.Fatalf("title lost: %q", list[0].Title)
	}
	if list[0].Messages != 1 {
		t.Fatalf("message count lost: %d", list[0].Messages)
	}
}
