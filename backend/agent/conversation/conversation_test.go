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
	// 统一存储：所有会话落到同一个 store.sqlite（不再是按会话拆分的 journal.jsonl）。
	if _, err := os.Stat(filepath.Join(dir, "store.sqlite")); err != nil {
		t.Fatalf("unified store file missing: %v", err)
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
	// 每轮 user 消息都会自动打 turn 锚点（5 条 user + 1 个手动），但手动那个必须在。
	manual := 0
	for _, a := range sess.Anchors() {
		if a.Kind == "fold" {
			manual++
		}
	}
	if manual != 1 {
		t.Fatalf("want exactly 1 manual fold anchor, got %d", manual)
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

// 侧边「刻度时间线」要能列出多个可跳转的点。tail_json 是覆盖式的，所以这里
// 守住 anchors 履历不被每次折叠冲掉 —— 退化回单点时这个用例必须失败。
func TestAnchorsHistoryAccumulates(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/anchors")

	var seqs []int64
	for round := 0; round < 3; round++ {
		s, err := sess.AppendMessage(RoleUser, "第 "+string(rune('A'+round))+" 轮")
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
		if _, err := sess.DropAnchor(); err != nil {
			t.Fatalf("drop anchor %d: %v", round, err)
		}
	}
	got := sess.Anchors()
	if len(got) != 3 {
		t.Fatalf("want 3 anchors accumulated, got %d", len(got))
	}
	for i, a := range got {
		if a.Seq != seqs[i] {
			t.Fatalf("anchor %d seq = %d, want %d", i, a.Seq, seqs[i])
		}
		if a.Messages != i+1 {
			t.Fatalf("anchor %d messages = %d, want %d", i, a.Messages, i+1)
		}
	}

	// 回退到第一个锚点：比它新的锚点描述的未来已经不存在了，必须一并清掉，
	// 否则 UI 会给出一个「跳不回去」的点。
	if err := sess.Rollback(seqs[0], ""); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := sess.Anchors(); len(got) != 1 || got[0].Seq != seqs[0] {
		t.Fatalf("after rollback want only anchor #%d, got %+v", seqs[0], got)
	}
}

// 锚点履历必须持久化，而不是只活在内存里。
func TestAnchorsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sess, _ := st.Open("/proj/anchors2")
	want := map[int64]bool{}
	for i := 0; i < 3; i++ {
		s, err := sess.AppendMessage(RoleUser, "消息 "+string(rune('A'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sess.DropAnchor(); err != nil {
			t.Fatal(err)
		}
		want[s] = true
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2 := NewStore(dir)
	defer st2.Close()
	sess2, err := st2.Open("/proj/anchors2")
	if err != nil {
		t.Fatal(err)
	}
	got := sess2.Anchors()
	if len(got) != len(want) {
		t.Fatalf("want %d anchors after reopen, got %d", len(want), len(got))
	}
	for _, a := range got {
		if !want[a.Seq] {
			t.Fatalf("unexpected anchor seq %d after reopen", a.Seq)
		}
	}
}

// 每轮用户发言应自动生成一个 turn 锚点（零手打），下一轮发言时闭合上一轮区间，
// 归档可翻转、检索能按内容映射回工作单元。
func TestTurnAnchorsAutoCreatedAndSearchable(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/turns")

	if _, err := sess.AppendMessage(RoleUser, "把会话消息持久化到后端磁盘"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(RoleAssistant, "已实现：统一 SQLite 存储"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(RoleUser, "把部署脚本改成幂等的"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(RoleAssistant, "部署脚本已加锁与检查"); err != nil {
		t.Fatal(err)
	}

	anchors := sess.Anchors()
	var turns []AnchorInfo
	for _, a := range anchors {
		if a.Kind == "turn" {
			turns = append(turns, a)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 turn anchors (one per user message), got %d (all=%+v)", len(turns), anchors)
	}
	if turns[0].Summary == "" || !strings.Contains(turns[0].Summary, "持久化") {
		t.Fatalf("turn 0 summary should distill the user ask, got %q", turns[0].Summary)
	}
	// 第一轮已被第二条用户消息闭合；第二轮仍开放（turn_end_seq=0）。
	if turns[0].TurnEndSeq == 0 {
		t.Fatal("turn 0 should have been closed by the next user message")
	}
	if turns[1].TurnEndSeq != 0 {
		t.Fatalf("turn 1 should still be open, got end=%d", turns[1].TurnEndSeq)
	}

	// 归档第一轮：状态翻转，其余不动。
	if err := sess.ArchiveAnchor(turns[0].Seq, true); err != nil {
		t.Fatal(err)
	}
	after := sess.Anchors()
	if after[0].Status != "archived" || after[1].Status != "active" {
		t.Fatalf("archive flip failed: %+v", after)
	}
	if err := sess.ArchiveAnchor(turns[0].Seq, false); err != nil {
		t.Fatal(err)
	}
	if got := sess.Anchors()[0].Status; got != "active" {
		t.Fatalf("unarchive failed, got %s", got)
	}

	// 内容检索："部署"应命中第二轮而不是第一轮。
	hits := sess.SearchAnchors("部署脚本", 5)
	if len(hits) == 0 {
		t.Fatal("search should find the deployment turn")
	}
	if hits[0].Anchor.Seq != turns[1].Seq {
		t.Fatalf("search hit wrong anchor: got #%d want #%d", hits[0].Anchor.Seq, turns[1].Seq)
	}
	if hits[0].Anchor.Summary == "" {
		t.Fatal("hit anchor should carry a summary")
	}
}

// 折叠快照与用户轮次可能落在同一个事件上。折叠行绝不能把轮次行连同它的
// 摘要一起覆盖掉，否则时间轴上这一轮会变成一个没有摘要的灰色点。
func TestFoldNeverErasesATurnOnTheSameEvent(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/fold-vs-turn")
	if _, err := sess.AppendMessage(RoleUser, "把回退做成跨重启可恢复的"); err != nil {
		t.Fatal(err)
	}
	// 关闭会话会保存折叠快照，落点正好是上面那条用户消息的 seq。
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	anchors := sess.Anchors()
	var turn *AnchorInfo
	for i := range anchors {
		if anchors[i].Kind == "turn" {
			turn = &anchors[i]
		}
	}
	if turn == nil {
		t.Fatalf("turn anchor was erased by the fold at the same seq: %+v", anchors)
	}
	if turn.MsgSeq != 1 {
		t.Fatalf("turn should still sit on the user message, got msg_seq=%d", turn.MsgSeq)
	}
	if !strings.Contains(turn.Summary, "回退") {
		t.Fatalf("turn summary lost: %q", turn.Summary)
	}
}

// 老会话（自动打点上线前就存在的）打开时必须补出历史轮次，否则刻度尺一片空白，
// 看起来就像功能没做。且回填不能覆盖已有行。
func TestBackfillTurnAnchorsForExistingSession(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sess, _ := st.Open("/proj/backfill")
	_, _ = sess.AppendMessage(RoleUser, "第一条：把会话落盘")
	_, _ = sess.AppendMessage(RoleAssistant, "好")
	_, _ = sess.AppendMessage(RoleUser, "第二条：加刻度尺")
	// 模拟老会话：一行锚点都没有。
	if err := sess.db.Exec(`DELETE FROM anchors WHERE hash=? AND id=?`, sess.hash, sess.id); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2 := NewStore(dir)
	defer st2.Close()
	sess2, err := st2.Open("/proj/backfill")
	if err != nil {
		t.Fatal(err)
	}
	var turns []AnchorInfo
	for _, a := range sess2.Anchors() {
		if a.Kind == "turn" {
			turns = append(turns, a)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 backfilled turns, got %d (%+v)", len(turns), sess2.Anchors())
	}
	if !strings.Contains(turns[0].Summary, "落盘") || !strings.Contains(turns[1].Summary, "刻度尺") {
		t.Fatalf("backfill summaries wrong: %q / %q", turns[0].Summary, turns[1].Summary)
	}
	if turns[0].TurnEndSeq == 0 {
		t.Fatal("first turn should be closed by the next user message")
	}
	if turns[1].TurnEndSeq != 0 {
		t.Fatal("last turn should stay open")
	}
	// 重开一次：已有行不能被重复写，数量必须稳定。
	if err := st2.Close(); err != nil {
		t.Fatal(err)
	}
	st3 := NewStore(dir)
	defer st3.Close()
	sess3, _ := st3.Open("/proj/backfill")
	if got := len(sess3.Anchors()); got != 2 {
		t.Fatalf("reopen should stay at 2 anchors, got %d", got)
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

// fakeCap records the hook calls without touching git or the filesystem.
type fakeCap struct {
	begun    []int64
	captured []int64
	opened   int
}

func (f *fakeCap) Begin(seq int64)              { f.begun = append(f.begun, seq) }
func (f *fakeCap) Capture(seq int64) error      { f.captured = append(f.captured, seq); return nil }
func (f *fakeCap) CaptureOpen()                 { f.opened++ }

func TestTurnCaptureHooksFireAtTurnBoundaries(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	defer st.Close()
	sess, _ := st.Open("/proj/capture")

	cap := &fakeCap{}
	sess.SetCapture(cap)

	s1, _ := sess.AppendMessage(RoleUser, "第一轮")
	sess.AppendMessage(RoleAssistant, "回复")
	s2, _ := sess.AppendMessage(RoleUser, "第二轮")

	// 每个用户消息各 Begin 一次；上一轮在下一条用户消息到来时闭合并 Capture。
	if len(cap.begun) != 2 || cap.begun[0] != s1 || cap.begun[1] != s2 {
		t.Fatalf("want Begin at both user messages, got %+v", cap.begun)
	}
	if len(cap.captured) != 1 || cap.captured[0] != s1 {
		t.Fatalf("want the closed turn captured once, got %+v", cap.captured)
	}

	// 归档也补抓一次（幂等，审查面板要能立刻看到版本）。
	if err := sess.ArchiveAnchor(s2, true); err != nil {
		t.Fatal(err)
	}
	if len(cap.captured) != 2 || cap.captured[1] != s2 {
		t.Fatalf("archive should capture the turn, got %+v", cap.captured)
	}

	// 关会话把仍开着的轮次全部落库。
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if cap.opened != 1 {
		t.Fatalf("Close should flush open turns once, got %d", cap.opened)
	}
}
