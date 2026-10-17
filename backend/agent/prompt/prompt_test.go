package prompt

import (
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/conversation"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/recall"
)

func fixture() *conversation.SessionState {
	st := conversation.NewSessionState("s1", "/proj", "演示", 1000)
	st.Redlines = []conversation.Redline{
		{ID: "r1", Text: "不要推送代码到远端", Seq: 1, Severity: "hard"},
		{ID: "r2", Text: "注意上下文超限", Seq: 2, Severity: "soft"},
	}
	st.Requirement = []conversation.Requirement{
		{ID: "q1", Text: "实现项目级会话持久化", Seq: 3, Status: "open"},
		{ID: "q2", Text: "已完成的老需求", Seq: 4, Status: "done"},
	}
	st.Segments = []conversation.Segment{
		{ID: "g1", Title: "登录页", Summary: "登录页完成，JWT 鉴权", Status: "closed", OpenSeq: 10, CloseSeq: 20},
	}
	st.Messages = []conversation.Msg{
		{Seq: 21, Role: conversation.RoleUser, Content: "现在帮我做语义召回", Tokens: 10},
		{Seq: 22, Role: conversation.RoleAssistant, Content: "好的，我用 embedding 实现", Tokens: 10},
	}
	st.Segments = append(st.Segments, conversation.Segment{
		ID: "g2", Title: "语义召回", Status: "active", OpenSeq: 21, MsgSeqs: []int64{21, 22}, Tokens: 20,
	})
	st.TotalTokens = 40
	return st
}

// 反证一：预算再紧，红线也不能被裁掉。这是红线区别于普通上下文的唯一理由。
func TestRedlinesSurviveExtremeBudget(t *testing.T) {
	st := fixture()
	// 塞入大量召回与摘要，把预算彻底挤爆。
	hits := make([]recall.Hit, 0, 50)
	for i := 0; i < 50; i++ {
		hits = append(hits, recall.Hit{Item: recall.Item{Seq: int64(100 + i), Text: strings.Repeat("召回内容", 40)}})
	}
	p := Pack(Input{
		State:  st,
		Query:  "语义召回",
		Recall: hits,
		Budget: Budget{MaxTokens: 1200, RedlineTokens: 800, RecallTokens: 1500, SummaryTokens: 800, RequireTokens: 500, ActiveTokens: 2000, RecentMessages: 8},
	})
	if !strings.Contains(p.System, "不要推送代码到远端") {
		t.Fatal("redline dropped from prompt under budget pressure")
	}
	if !strings.Contains(p.System, "项目红线") {
		t.Fatal("redline section missing")
	}
	for _, s := range p.Sections {
		if s.Name == "redlines" {
			if !strings.HasPrefix(strings.TrimSpace(p.System), "# 项目记忆上下文") {
				t.Fatal("unexpected system prefix")
			}
		}
	}
}

// 反证二：已完成段落只贡献摘要，原文不得进入提示词。
func TestClosedSegmentContributesOnlySummary(t *testing.T) {
	st := fixture()
	// 给已关闭段落塞入一段"原文"，并让召回也命中它之外的东西。
	st.Messages = append([]conversation.Msg{
		{Seq: 11, Role: conversation.RoleUser, Content: "登录页的原始需求原文不该出现在提示词里", Tokens: 10},
	}, st.Messages...)
	st.Segments[0].MsgSeqs = []int64{11}

	p := Pack(Input{State: st, Query: "登录", Budget: DefaultBudget()})
	if strings.Contains(p.System, "登录页的原始需求原文不该出现在提示词里") {
		t.Fatal("closed segment leaked verbatim text into the prompt")
	}
	if !strings.Contains(p.System, "登录页完成，JWT 鉴权") {
		t.Fatal("closed segment summary missing")
	}
	for _, m := range p.Messages {
		if m.Seq == 11 {
			t.Fatal("closed segment message leaked into the verbatim tail")
		}
	}
}

// 反证三：召回内容必须被明确标注为历史，否则模型会把它当成"刚说过的话"。
func TestRecallIsLabelledAsHistory(t *testing.T) {
	st := fixture()
	hits := []recall.Hit{{Item: recall.Item{Seq: 99, Text: "三周前我说过要用 journal 落盘"}, Score: 0.82}}
	p := Pack(Input{State: st, Query: "落盘方案", Recall: hits, Budget: DefaultBudget()})
	for _, want := range []string{"非当前对话", "seq=99", "三周前我说过要用 journal 落盘"} {
		if !strings.Contains(p.System, want) {
			t.Fatalf("recall block missing %q", want)
		}
	}
	// 必须写明冲突时以当前对话/红线为准，避免模型拿历史跟用户抬杠。
	if !strings.Contains(p.System, "以当前对话") {
		t.Fatal("recall block must state precedence over current conversation")
	}
}

func TestOpenRequirementsOnly(t *testing.T) {
	st := fixture()
	p := Pack(Input{State: st, Budget: DefaultBudget()})
	if !strings.Contains(p.System, "实现项目级会话持久化") {
		t.Fatal("open requirement missing")
	}
	if strings.Contains(p.System, "已完成的老需求") {
		t.Fatal("done requirement should not be injected")
	}
}

func TestBudgetIsRespected(t *testing.T) {
	st := fixture()
	hits := make([]recall.Hit, 0, 80)
	for i := 0; i < 80; i++ {
		hits = append(hits, recall.Hit{Item: recall.Item{Seq: int64(200 + i), Text: strings.Repeat("很长的历史内容", 60)}, Score: 0.9})
	}
	b := DefaultBudget()
	b.MaxTokens = 1000
	p := Pack(Input{State: st, Query: "任意", Recall: hits, Budget: b})
	if p.Tokens > b.MaxTokens+200 { // 红线强制注入，允许小幅溢出
		t.Fatalf("budget blown: %d > %d", p.Tokens, b.MaxTokens+200)
	}
}

func TestActiveSegmentTailIsChronological(t *testing.T) {
	st := fixture()
	p := Pack(Input{State: st, Budget: DefaultBudget()})
	var sec string
	for _, s := range p.Sections {
		if s.Name == "active" {
			sec = s.Text
		}
	}
	if sec == "" {
		t.Fatal("no active section")
	}
	iAsk := strings.Index(sec, "现在帮我做语义召回")
	iAns := strings.Index(sec, "好的，我用 embedding 实现")
	if iAsk < 0 || iAns < 0 {
		t.Fatalf("active section missing messages: %q", sec)
	}
	if iAsk > iAns {
		t.Fatal("active section is not in chronological order")
	}
}

func TestPackWithoutStateIsSafe(t *testing.T) {
	if p := Pack(Input{}); p.System != "" || len(p.Messages) != 0 {
		t.Fatal("packing nil state should be a no-op")
	}
}

func TestToolHintAndDroppedReport(t *testing.T) {
	st := fixture()
	hits := make([]recall.Hit, 0, 60)
	for i := 0; i < 60; i++ {
		hits = append(hits, recall.Hit{Item: recall.Item{Seq: int64(300 + i), Text: strings.Repeat("历史", 80)}, Score: 0.9})
	}
	// 预算远小于召回量：装配器必须明确报告"砍掉了什么"，而不是静默丢弃。
	p := Pack(Input{State: st, Query: "历史", Recall: hits, Budget: Budget{MaxTokens: 200, RedlineTokens: 100, RecallTokens: 5000}, ToolHint: "可用工具：read / edit"})
	if !strings.Contains(p.System, "可用工具：read / edit") {
		t.Fatal("tool hint missing")
	}
	if len(p.Dropped) == 0 {
		t.Fatal("expected some sections to be reported as dropped/truncated")
	}
}
