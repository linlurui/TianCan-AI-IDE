package testkit

import (
	"context"
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
	"github.com/linlurui/tiancan-ai-ide/backend/skills"
)

// fakeSkillMatcher 是 agent.SkillMatcher 的测试替身；直接返回预设命中，不触碰磁盘。
type fakeSkillMatcher struct {
	matched []*skills.Skill
}

func (f *fakeSkillMatcher) MatchTrigger(input string) []*skills.Skill {
	return f.matched
}

const skillBodyMarker = "G5_SKILL_BODY_MARKER_9f3c"

// findUserIndices 返回 contents 中 user 消息的下标集合（按出现顺序）。
func userMessageIndices(contents []string) []int {
	var idx []int
	for i, c := range contents {
		if strings.HasPrefix(c, "user:") {
			idx = append(idx, i)
		}
	}
	return idx
}

// TestAgentRun_InjectsSkillPreStepAsUserMessage G5 核心契约：
// 命中技能时，技能正文以 user 消息形式注入，且位于真实用户请求之前。
func TestAgentRun_InjectsSkillPreStepAsUserMessage(t *testing.T) {
	agent := newIsolatedAgent(t)
	skill := &skills.Skill{Name: "code-review", Body: skillBodyMarker}
	agent.SetSkillMatcher(&fakeSkillMatcher{matched: []*skills.Skill{skill}})

	adapter := NewMockAdapter(TextResponse("审查完成"))
	res := agent.Run(context.Background(), "请审查这段代码", 5, adapter.Func())

	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	calls := adapter.Calls()
	if len(calls) != 1 {
		t.Fatalf("期望模型被调用 1 次，实际 %d", len(calls))
	}
	c := calls[0]

	// KV 缓存契约：技能正文绝不可进入 system prompt（否则每命中一次都破缓存）。
	if strings.Contains(c.SystemPrompt, skillBodyMarker) {
		t.Fatalf("技能正文不得进入 system prompt（KV 缓存契约），但它出现了")
	}

	preIdx, realIdx := -1, -1
	for i, content := range c.Contents {
		if strings.HasPrefix(content, "user:") && strings.Contains(content, skillBodyMarker) {
			preIdx = i
		}
		if strings.HasPrefix(content, "user:") && strings.Contains(content, "请审查这段代码") {
			realIdx = i
		}
	}
	if preIdx < 0 {
		t.Fatalf("未找到技能 pre-step 的 user 消息: %v", c.Contents)
	}
	if realIdx < 0 {
		t.Fatalf("未找到真实用户请求: %v", c.Contents)
	}
	if preIdx >= realIdx {
		t.Fatalf("技能 pre-step(idx %d) 必须在真实请求(idx %d)之前: %v", preIdx, realIdx, c.Contents)
	}
}

// TestAgentRun_NoSkill_NoInjection 反证：无命中时不得注入任何额外 user 消息，
// 且 system prompt 保持静态。若有人把注入逻辑写成「无条件注入」，本测试必失败。
func TestAgentRun_NoSkill_NoInjection(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetSkillMatcher(&fakeSkillMatcher{matched: nil})

	adapter := NewMockAdapter(TextResponse("好的"))
	res := agent.Run(context.Background(), "今天天气如何", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	c := adapter.Calls()[0]

	for _, content := range c.Contents {
		if strings.HasPrefix(content, "user:") && strings.Contains(content, skillBodyMarker) {
			t.Fatalf("无技能时不应注入 pre-step，但出现: %q", content)
		}
	}

	idx := userMessageIndices(c.Contents)
	if len(idx) != 1 {
		t.Fatalf("期望恰好 1 条 user 消息（真实请求），实际 %d 条: %v", len(idx), c.Contents)
	}
	if !strings.Contains(c.Contents[idx[0]], "今天天气如何") {
		t.Fatalf("唯一的 user 消息应是真实请求: %v", c.Contents[idx[0]])
	}
}

// TestAgentRunStream_InjectsSkillPreStep RunStream 同样走共享的 injectSkillPreSteps，
// 验证流式路径也满足「user 消息注入 + system prompt 不含正文」契约。
func TestAgentRunStream_InjectsSkillPreStep(t *testing.T) {
	agent := newIsolatedAgent(t)
	skill := &skills.Skill{Name: "code-review", Body: skillBodyMarker}
	agent.SetSkillMatcher(&fakeSkillMatcher{matched: []*skills.Skill{skill}})

	var recordedSystem string
	var recordedContents []string
	streamFn := func(systemPrompt string, messages []types.Message, onToken func(string)) (string, error) {
		recordedSystem = systemPrompt
		for _, m := range messages {
			recordedContents = append(recordedContents, string(m.Role)+":"+m.Content)
		}
		onToken("审查完成")
		return "审查完成", nil
	}

	ch, err := agent.RunStream(context.Background(), "请审查这段代码", 5, streamFn)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	} // drain

	if strings.Contains(recordedSystem, skillBodyMarker) {
		t.Fatalf("RunStream 下技能正文也不得进入 system prompt")
	}
	preIdx, realIdx := -1, -1
	for i, content := range recordedContents {
		if strings.HasPrefix(content, "user:") && strings.Contains(content, skillBodyMarker) {
			preIdx = i
		}
		if strings.HasPrefix(content, "user:") && strings.Contains(content, "请审查这段代码") {
			realIdx = i
		}
	}
	if preIdx < 0 || realIdx < 0 || preIdx >= realIdx {
		t.Fatalf("RunStream pre-step 顺序错误: %v", recordedContents)
	}
}
