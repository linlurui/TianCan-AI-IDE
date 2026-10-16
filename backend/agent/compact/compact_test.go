package compact

import (
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// buildSeq 构造一条对话：U=用户，A=助手纯文本，AT=助手带工具调用，T=工具结果。
// 用于验证 tool-call/result 对的切点不变量。
func buildSeq() []types.Message {
	return []types.Message{
		{Role: types.RoleUser, Content: "u0"},       // 0
		{Role: types.RoleAssistant, Content: "a1"},  // 1
		{Role: types.RoleUser, Content: "u2"},       // 2
		{Role: types.RoleAssistant, Content: "call bash"}, // 3 AT
		{Role: types.RoleTool, Content: "t4"},       // 4 T
		{Role: types.RoleTool, Content: "t5"},       // 5 T
		{Role: types.RoleAssistant, Content: "a6"},  // 6
		{Role: types.RoleUser, Content: "u7"},       // 7
		{Role: types.RoleAssistant, Content: "call read"}, // 8 AT
		{Role: types.RoleTool, Content: "t9"},       // 9 T
	}
}

// TestFindBalancedCut_BacksUpFromToolResult 核心不变量：当期望切点落在工具结果上，
// 必须"向前退"到发起该结果的助手消息，绝不切在 tool-call/result 对中间。
func TestFindBalancedCut_BacksUpFromToolResult(t *testing.T) {
	m := buildSeq()
	// 期望切点落在 t4(idx4) / t5(idx5) / t9(idx9) 都应回退到其前面的助手消息。
	cases := []struct {
		desired int
		want    int // 期望回退后的切点
	}{
		{4, 3}, // t4 -> AT(idx3)
		{5, 3}, // t5 -> AT(idx3)（整段结果回退到发起者）
		{9, 8}, // t9 -> AT(idx8)
	}
	for _, c := range cases {
		got := findBalancedCut(m, c.desired)
		if got != c.want {
			t.Fatalf("findBalancedCut(%d) = %d, want %d (messages[%d].Role=%s)", c.desired, got, c.want, got, m[got].Role)
		}
		if m[got].Role == types.RoleTool {
			t.Fatalf("切到了工具结果上 idx=%d，违反 tool-call/result 对不变量", got)
		}
	}
}

// TestFindBalancedCut_NoToolsUnchanged 纯用户/助手文本消息：切点不被无谓前移。
func TestFindBalancedCut_NoToolsUnchanged(t *testing.T) {
	m := []types.Message{
		{Role: types.RoleUser, Content: "u0"},
		{Role: types.RoleAssistant, Content: "a1"},
		{Role: types.RoleUser, Content: "u2"},
		{Role: types.RoleAssistant, Content: "a3"},
	}
	for cut := 0; cut < len(m); cut++ {
		if got := findBalancedCut(m, cut); got != cut {
			t.Fatalf("无工具时切点不应前移: desired=%d got=%d", cut, got)
		}
	}
}

// TestFindBalancedCut_InvariantAcrossAllCuts 不变量在任意期望切点上都成立：
// 回退后的切点首条消息绝不可能是 RoleTool（否则即切在结果中段）。
func TestFindBalancedCut_InvariantAcrossAllCuts(t *testing.T) {
	m := buildSeq()
	for dc := 0; dc <= len(m); dc++ {
		cut := findBalancedCut(m, dc)
		if cut < 0 || cut > len(m) {
			t.Fatalf("cut 越界: %d (len=%d)", cut, len(m))
		}
		if cut < len(m) && m[cut].Role == types.RoleTool {
			t.Fatalf("dc=%d -> cut=%d 落在工具结果上，违反不变量", dc, cut)
		}
	}
}

// TestFindBalancedCut_Counterexample 反证：若有人把"回退"写成"前进"(cut++)，
// 则期望切点在 t4 时会变成 t5，仍落在工具结果上 -> 本测试断言回退后不再是 RoleTool。
func TestFindBalancedCut_Counterexample(t *testing.T) {
	m := buildSeq()
	if got := findBalancedCut(m, 5); got == 5 {
		t.Fatalf("切点在 t5(idx5, RoleTool) 时必须回退，不应停留")
	}
}

// TestFindBalancedCut_Clamps 越界期望切点被夹紧到 [0, len]。
func TestFindBalancedCut_Clamps(t *testing.T) {
	m := buildSeq()
	if got := findBalancedCut(m, -5); got != 0 {
		t.Fatalf("负数应夹紧到 0, got %d", got)
	}
	if got := findBalancedCut(m, 999); got != len(m) {
		t.Fatalf("超长应夹紧到 len, got %d", got)
	}
}

// TestCompactConversation_SetsSourceAuto 集成：auto 压缩的 checkpoint 必须带 source.kind=auto。
func TestCompactConversation_SetsSourceAuto(t *testing.T) {
	svc := NewCompactService()
	// 每条消息放大到足以触发 calculateMessagesToKeep 的 minTokens 阈值（estimateMessageTokens
	// = len/4+50，约 10000 字符/条 × 4 条 ≈ 10000 token），从而 keepCount 收敛在尾部、
	// 不会一路走到头部被判 "nothing to compact"。
	msgs := make([]types.Message, 0, 12)
	for i := 0; i < 12; i++ {
		r := types.RoleUser
		if i%2 == 1 {
			r = types.RoleAssistant
		}
		msgs = append(msgs, types.Message{Role: r, Content: strings.Repeat("x", 10000)})
	}
	var captured string
	chatFn := func(prompt string) (string, error) {
		captured = prompt
		return "<analysis>ok</analysis>\n<summary>summary</summary>", nil
	}
	res, err := svc.CompactConversation(msgs, chatFn)
	if err != nil {
		t.Fatalf("CompactConversation 失败: %v", err)
	}
	if res.Source.Kind != types.CompactSourceAuto {
		t.Fatalf("Source.Kind 应为 auto, got %q", res.Source.Kind)
	}
	if len(res.SummaryMessages) == 0 {
		t.Fatalf("SummaryMessages 不应为空")
	}
	if captured == "" {
		t.Fatalf("chatFn 未收到摘要 prompt")
	}
	if !strings.Contains(captured, "summary") { // 摘要 prompt 必然含 "summary" 字样
		t.Fatalf("chatFn 收到的内容不像摘要 prompt: %q", captured[:min(len(captured), 80)])
	}
}

// TestPartialCompactConversation_SetsSourcePartial 集成：partial 压缩的 checkpoint
// 必须带 source.kind=partial，且切点对 tool-call/result 对保持平衡。
func TestPartialCompactConversation_SetsSourcePartial(t *testing.T) {
	svc := NewCompactService()
	m := buildSeq()
	// 故意把 pivot 落在工具结果 t9(idx9) 上，验证会回退到 AT(idx8)。
	pivot := findBalancedCut(m, 9)
	if pivot != 8 {
		t.Fatalf("precondition: 期望 pivot 回退到 8, got %d", pivot)
	}
	chatFn := func(prompt string) (string, error) {
		return "<analysis>ok</analysis>\n<summary>partial</summary>", nil
	}
	res, err := svc.PartialCompactConversation(m, 9, CompactFrom, chatFn, "")
	if err != nil {
		t.Fatalf("PartialCompactConversation 失败: %v", err)
	}
	if res.Source.Kind != types.CompactSourcePartial {
		t.Fatalf("Source.Kind 应为 partial, got %q", res.Source.Kind)
	}
	// toKeep = m[:pivot]，其首条消息绝不可能是 RoleTool（平衡切点）。
	if pivot > 0 && m[pivot].Role == types.RoleTool {
		t.Fatalf("partial 切点对落在工具结果上 idx=%d", pivot)
	}
}
