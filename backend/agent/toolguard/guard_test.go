package toolguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

func okResult(s string) types.ToolResult { return types.ToolResult{Content: s, Success: true} }

// TestGuard_SuccessPassthrough 正常完成的工具，结果原样透传，不报任何错误。
func TestGuard_SuccessPassthrough(t *testing.T) {
	g := New(Config{Timeout: 200 * time.Millisecond})
	res, err := g.Execute(context.Background(), func(ctx context.Context) (types.ToolResult, error) {
		return okResult("hello"), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "hello" || !res.Success {
		t.Fatalf("result not passed through: %+v", res)
	}
}

// TestGuard_DeadlineFires 工具超过自身 deadline 仍在跑 → 返回 *ErrDeadline。
func TestGuard_DeadlineFires(t *testing.T) {
	g := New(Config{Timeout: 40 * time.Millisecond})
	start := time.Now()
	_, err := g.Execute(context.Background(), func(ctx context.Context) (types.ToolResult, error) {
		time.Sleep(300 * time.Millisecond) // 故意跑超 deadline
		return okResult("late"), nil
	})
	elapsed := time.Since(start)
	if _, ok := err.(*ErrDeadline); !ok {
		t.Fatalf("expected *ErrDeadline, got %v", err)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("deadline not enforced: tool ran %s", elapsed)
	}
}

// TestGuard_ParentCancelled 父 ctx 被取消（而非自身 deadline）→ 返回 *ErrParentCancelled。
func TestGuard_ParentCancelled(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel() // 父 ctx 已经取消
	g := New(Config{Timeout: 5 * time.Second})
	_, err := g.Execute(parent, func(ctx context.Context) (types.ToolResult, error) {
		time.Sleep(time.Second)
		return okResult("x"), nil
	})
	pc, ok := err.(*ErrParentCancelled)
	if !ok {
		t.Fatalf("expected *ErrParentCancelled, got %v", err)
	}
	if pc.Err == nil {
		t.Fatalf("expected wrapped parent error, got nil")
	}
}

// TestGuard_NoPrematureTimeout 反证：工具在 deadline 内完成（含 margin）→ 成功透传，
// 证明我们不是"到点就杀"，而是精确尊重完成。
func TestGuard_NoPrematureTimeout(t *testing.T) {
	g := New(Config{Timeout: 100 * time.Millisecond})
	res, err := g.Execute(context.Background(), func(ctx context.Context) (types.ToolResult, error) {
		time.Sleep(60 * time.Millisecond) // 在 deadline 内
		return okResult("in-time"), nil
	})
	if err != nil {
		t.Fatalf("unexpected error (tool finished within deadline): %v", err)
	}
	if res.Content != "in-time" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestGuard_ParentAliveNoFalseCancel 反证：父 ctx 未取消、工具按时完成 →
// 绝不误报 *ErrParentCancelled（证明取消归因正确）。
func TestGuard_ParentAliveNoFalseCancel(t *testing.T) {
	g := New(Config{Timeout: 100 * time.Millisecond})
	_, err := g.Execute(context.Background(), func(ctx context.Context) (types.ToolResult, error) {
		time.Sleep(20 * time.Millisecond)
		return okResult("ok"), nil
	})
	if _, ok := err.(*ErrParentCancelled); ok {
		t.Fatalf("falsely attributed a live parent to a cancellation: %v", err)
	}
}

// TestGuard_ToolErrorPropagated 工具自身返回的错误原样透传（非超时/取消）。
func TestGuard_ToolErrorPropagated(t *testing.T) {
	g := New(Config{Timeout: 100 * time.Millisecond})
	want := errors.New("boom")
	_, err := g.Execute(context.Background(), func(ctx context.Context) (types.ToolResult, error) {
		return types.ToolResult{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected tool error propagated, got %v", err)
	}
}
