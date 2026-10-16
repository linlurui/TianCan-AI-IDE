package testkit

import (
	"context"
	"strings"
	"testing"

	agentpkg "github.com/linlurui/tiancan-ai-ide/backend/agent"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/toolguard"
)

const reminderMarker = "[重复调用提醒]"

// TestAgentRun_RedirectsHammeredDeniedCall G6 核心契约：
// 模型反复用完全相同的参数敲一个被拒的工具调用（bash rm -rf /），超过阈值后，
// 主循环不再重复裸拒绝，而是注入一条「重复调用提醒」把模型重定向到换策略。
func TestAgentRun_RedirectsHammeredDeniedCall(t *testing.T) {
	agent := newIsolatedAgent(t)
	// 低阈值：相同被拒调用出现 2 次后即第 3 次起重定向，便于在少量迭代内观测。
	agent.SetDupDetector(toolguard.NewDetector(10, 2))

	hammer := ToolCallResponse(MockToolCall{Name: "bash", Args: map[string]interface{}{"command": "rm -rf /"}})
	adapter := NewMockAdapter(
		hammer, hammer, hammer, hammer,
		TextResponse("好的，我改用手动逐项清理策略"),
	)
	res := agent.Run(context.Background(), "清理一下磁盘", 10, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if adapter.Pending() != 0 {
		t.Fatalf("脚本未完全消费，剩余 %d（疑似循环次数与预期不符）", adapter.Pending())
	}

	var denials, reminders int
	for _, m := range res.Messages {
		if m.Role != agentpkg.RoleTool {
			continue
		}
		if strings.Contains(m.Content, "Permission denied") {
			denials++
		}
		if strings.Contains(m.Content, reminderMarker) {
			reminders++
		}
	}
	if reminders < 1 {
		t.Fatalf("反复被拒的调用未被重定向：缺少提醒消息。messages=%v", messageDump(res.Messages))
	}
	// 阈值=2 → 仅前 2 次是裸拒绝，其余走提醒；denials 不得超过阈值。
	if denials > 2 {
		t.Fatalf("裸拒绝次数 %d 超过阈值 2，重定向未生效", denials)
	}
	// 提醒文案必须点名工具并给出换策略的指引。
	if !strings.Contains(lastReminder(res.Messages), "bash") {
		t.Fatalf("提醒未点名被敲工具：%q", lastReminder(res.Messages))
	}
}

// TestAgentRun_NoHammer_NoReminder 反证：相同工具、每次参数不同（即便都被拒）→
// 不构成重复敲击，绝不注入提醒。若有人把提醒写成了「按调用次数无差别触发」，本测试必失败。
func TestAgentRun_NoHammer_NoReminder(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetDupDetector(toolguard.NewDetector(10, 2))

	adapter := NewMockAdapter(
		ToolCallResponse(MockToolCall{Name: "bash", Args: map[string]interface{}{"command": "echo 1"}}),
		ToolCallResponse(MockToolCall{Name: "bash", Args: map[string]interface{}{"command": "echo 2"}}),
		ToolCallResponse(MockToolCall{Name: "bash", Args: map[string]interface{}{"command": "echo 3"}}),
		TextResponse("完成"),
	)
	res := agent.Run(context.Background(), "跑几个命令", 10, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}

	for _, m := range res.Messages {
		if m.Role == agentpkg.RoleTool && strings.Contains(m.Content, reminderMarker) {
			t.Fatalf("参数各不相同的调用不应触发提醒，但出现了: %q", m.Content)
		}
	}
}

func lastReminder(msgs []agentpkg.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.Contains(msgs[i].Content, reminderMarker) {
			return msgs[i].Content
		}
	}
	return ""
}

func messageDump(msgs []agentpkg.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role)+":"+m.Content)
	}
	return out
}
