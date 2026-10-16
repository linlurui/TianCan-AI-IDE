package testkit

import (
	"context"
	"strings"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/clarify"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// TestAgentRun_ClarificationPending_NotError G9 核心：非交互默认（SilentClarifier）
// 超时/无应答 → 返回 {pending:true}，且 Error 必须为空（不是错误）。模型只被调用一次。
func TestAgentRun_ClarificationPending_NotError(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetClarifier(clarify.SilentClarifier{})

	adapter := NewMockAdapter(
		TextResponse(clarify.Marker + "which file should I edit?"),
	)
	res := agent.Run(context.Background(), "帮我改代码", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if res.Error != "" {
		t.Fatalf("G9：澄清超时/无应答必须不是错误，但 Error=%q", res.Error)
	}
	if !res.Pending {
		t.Fatalf("G9：澄清未决必须返回 Pending=true，但为 false")
	}
	if adapter.Pending() != 0 {
		t.Fatalf("澄清未决应立即停止，模型不应被二次调用（剩余 %d）", adapter.Pending())
	}
}

// TestAgentRun_ClarificationAnswer_FedBackAsUserMessage G9 核心：clarifier 给出答案后，
// 答案必须以 user message 回灌主循环，循环据此继续并最终正常收尾（Pending=false）。
func TestAgentRun_ClarificationAnswer_FedBackAsUserMessage(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetClarifier(clarify.EchoClarifier{Answer: "edit main.go"})

	// 第一次：模型要求澄清；第二次（已带答案）：正常收尾
	adapter := NewMockAdapter(
		TextResponse(clarify.Marker+"which file should I edit?"),
		TextResponse("ok, proceeding"),
	)
	res := agent.Run(context.Background(), "帮我改代码", 5, adapter.Func())
	if !res.Done {
		t.Fatalf("run 未完成: %v", res.Error)
	}
	if res.Pending {
		t.Fatalf("G9：已获答案不应再 Pending")
	}
	if res.Error != "" {
		t.Fatalf("正常收敛不应报错: %v", res.Error)
	}

	// 答案必须作为 user message 回灌：断言某次模型调用携带 "user:edit main.go"
	fed := false
	for _, c := range adapter.Calls() {
		for _, content := range c.Contents {
			if content == "user:edit main.go" {
				fed = true
			}
		}
	}
	if !fed {
		t.Fatalf("G9：clarifier 答案未以 user message 回灌；记录=%v", adapter.Calls())
	}
	if adapter.Pending() != 0 {
		t.Fatalf("模型调用次数与脚本不符（剩余 %d）", adapter.Pending())
	}
}

// TestAgentRun_NoClarification_NotPending 反证：普通最终答案（无 marker）不应触发澄清，
// 正常收尾且 Pending=false，clarifier 绝不被调用。
func TestAgentRun_NoClarification_NotPending(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetClarifier(clarify.SilentClarifier{})

	adapter := NewMockAdapter(TextResponse("all done"))
	res := agent.Run(context.Background(), "hi", 5, adapter.Func())
	if !res.Done || res.Error != "" {
		t.Fatalf("普通收尾异常: %v", res.Error)
	}
	if res.Pending {
		t.Fatalf("无澄清时 Pending 必须为 false")
	}
	if adapter.Pending() != 0 {
		t.Fatalf("模型调用次数与脚本不符（剩余 %d）", adapter.Pending())
	}
}

// TestAgentRunStream_ClarificationPending_EmitsPendingEvent G9 核心：RunStream 路径在
// 澄清未决时发出 {type:"pending"} 事件，而非 error 事件。
func TestAgentRunStream_ClarificationPending_EmitsPendingEvent(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetClarifier(clarify.SilentClarifier{})

	streamFn := func(systemPrompt string, messages []types.Message, onToken func(string)) (string, error) {
		return clarify.Marker + "which file?", nil
	}
	ch, err := agent.RunStream(context.Background(), "帮我改代码", 5, streamFn)
	if err != nil {
		t.Fatal(err)
	}
	var sawPending, sawError bool
	for ev := range ch {
		if ev.Type == "pending" {
			sawPending = true
		}
		if ev.Type == "error" {
			sawError = true
		}
	}
	if !sawPending {
		t.Fatalf("RunStream 澄清未决必须发出 pending 事件")
	}
	if sawError {
		t.Fatalf("G9：澄清未决不能是 error 事件")
	}
}

// TestAgentRunStream_ClarificationAnswer_FedBackAsUserMessage 验证 RunStream 路径同样把
// clarifier 答案以 user message 回灌，并据此正常收尾。
func TestAgentRunStream_ClarificationAnswer_FedBackAsUserMessage(t *testing.T) {
	agent := newIsolatedAgent(t)
	agent.SetClarifier(clarify.EchoClarifier{Answer: "edit main.go"})

	streamFn := func(systemPrompt string, messages []types.Message, onToken func(string)) (string, error) {
		// 若已收到答案（user message 回灌），说明澄清已解决，正常收尾
		for _, m := range messages {
			if m.Role == types.RoleUser && strings.Contains(m.Content, "edit main.go") {
				onToken("ok")
				return "ok", nil
			}
		}
		return clarify.Marker + "which file?", nil
	}
	ch, err := agent.RunStream(context.Background(), "帮我改代码", 5, streamFn)
	if err != nil {
		t.Fatal(err)
	}
	var sawPending, sawError, sawDone bool
	for ev := range ch {
		switch ev.Type {
		case "pending":
			sawPending = true
		case "error":
			sawError = true
		case "done":
			sawDone = true
		}
	}
	if sawPending {
		t.Fatalf("已获答案不应再 pending")
	}
	if sawError {
		t.Fatalf("正常收敛不应有 error 事件")
	}
	if !sawDone {
		t.Fatalf("RunStream 应正常 done")
	}
}
