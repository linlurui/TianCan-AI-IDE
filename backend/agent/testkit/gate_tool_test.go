package testkit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/parallel"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
)

// waitFor 轮询直到条件满足或超时；用于并发行为测试的确定性同步点。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestGateTool_BasicContract 表驱动验证 GateTool 的基础契约：
// 声明接口（Name/并发安全/只读）、执行计数、结果内容。
func TestGateTool_BasicContract(t *testing.T) {
	tests := []struct {
		name            string
		toolName        string
		concurrencySafe bool
		readOnly        bool
	}{
		{"并行只读工具", "probe_parallel", true, true},
		{"排他写工具", "probe_exclusive", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewGateTool(tt.toolName,
				WithConcurrencySafe(tt.concurrencySafe),
				WithReadOnly(tt.readOnly),
				WithAutoRelease(),
			)
			if tool.Name() != tt.toolName {
				t.Fatalf("Name()=%q want %q", tool.Name(), tt.toolName)
			}
			if tool.IsConcurrencySafe() != tt.concurrencySafe {
				t.Fatalf("IsConcurrencySafe()=%v want %v", tool.IsConcurrencySafe(), tt.concurrencySafe)
			}
			if tool.IsReadOnly() != tt.readOnly {
				t.Fatalf("IsReadOnly()=%v want %v", tool.IsReadOnly(), tt.readOnly)
			}
			res, err := tool.Execute(context.Background(), nil)
			if err != nil || !res.Success {
				t.Fatalf("Execute failed: res=%+v err=%v", res, err)
			}
			if tool.CallCount() != 1 {
				t.Fatalf("CallCount()=%d want 1", tool.CallCount())
			}
		})
	}
}

// TestGateTool_RespectsExecutorLimit 行为证据：
// 经 parallel.Executor（上限 2）跑 5 个阻塞型任务，GateTool 观测到的并发峰值
// 必须恰好等于 2——证明调度器信号量真的生效，且计数器没有漏记/多记。
func TestGateTool_RespectsExecutorLimit(t *testing.T) {
	const limit = 2
	const taskCount = 5

	tool := NewGateTool("probe", WithConcurrencySafe(true)) // 阻塞型
	exec := parallel.NewExecutor(limit)

	tasks := make([]types.ParallelTask, taskCount)
	for i := range tasks {
		tasks[i] = types.ParallelTask{ID: string(rune('a' + i)), Status: types.ParallelPending}
	}

	execFn := func(ctx context.Context, task types.ParallelTask) (*types.ParallelTaskResult, error) {
		res, err := tool.Execute(ctx, nil)
		if err != nil {
			return nil, err
		}
		return &types.ParallelTaskResult{Content: res.Content}, nil
	}

	done := make(chan []types.ParallelTask, 1)
	go func() { done <- exec.ExecuteTasks(context.Background(), tasks, execFn) }()

	// 两个任务进入并阻塞后，峰值必须已经是 2（确定性同步点）
	waitFor(t, 2*time.Second, "two tasks to enter Execute", func() bool {
		return tool.CallCount() >= limit
	})
	if got := tool.MaxConcurrency(); got != limit {
		t.Fatalf("MaxConcurrency()=%d want %d while %d tasks blocked", got, limit, limit)
	}

	tool.Release(taskCount) // 放行全部（含尚未进入的）
	results := <-done

	for _, task := range results {
		if task.Status != types.ParallelCompleted {
			t.Fatalf("task %s status=%v want completed", task.ID, task.Status)
		}
	}
	if tool.CallCount() != taskCount {
		t.Fatalf("CallCount()=%d want %d", tool.CallCount(), taskCount)
	}
	if got := tool.MaxConcurrency(); got != limit {
		t.Fatalf("MaxConcurrency()=%d want exactly %d (executor limit)", got, limit)
	}
}

// TestGateTool_BlocksUntilReleased 验证门闩语义：
// 未放行时 Execute 不返回；Release 后才完成。
func TestGateTool_BlocksUntilReleased(t *testing.T) {
	tool := NewGateTool("gated") // 默认非 auto-release

	var wg sync.WaitGroup
	wg.Add(1)
	var res types.ToolResult
	var err error
	go func() {
		defer wg.Done()
		res, err = tool.Execute(context.Background(), nil)
	}()

	waitFor(t, 2*time.Second, "Execute to enter", func() bool { return tool.CallCount() == 1 })
	select {
	case <-func() chan struct{} { ch := make(chan struct{}); go func() { wg.Wait(); close(ch) }(); return ch }():
		t.Fatal("Execute returned before Release")
	case <-time.After(30 * time.Millisecond):
		// 仍阻塞，符合预期
	}

	tool.Release(1)
	wg.Wait()
	if err != nil || !res.Success {
		t.Fatalf("Execute after release failed: res=%+v err=%v", res, err)
	}
}

// TestGateTool_CancelledContext 验证阻塞中的 Execute 在 ctx 取消时返回错误。
func TestGateTool_CancelledContext(t *testing.T) {
	tool := NewGateTool("gated")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := tool.Execute(ctx, nil)
	if err == nil {
		t.Fatal("blocked Execute must fail when ctx is cancelled")
	}
}

// TestGateTool_Counterexample_RecorderIsNotVacuous 反证测试（元验证）：
// 并发峰值记录必须真能抓到违规。这里故意绕过调度器、并发 3 个阻塞调用，
// 记录器必须如实报出峰值 3——若有人把并发计数改成恒 1（或断言改成只查 >0），
// 本测试必然失败，证明「峰值==2」这类断言不是空话。
func TestGateTool_Counterexample_RecorderIsNotVacuous(t *testing.T) {
	tool := NewGateTool("fake-witness") // 阻塞型

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tool.Execute(context.Background(), nil)
		}()
	}

	waitFor(t, 2*time.Second, "three concurrent calls to enter", func() bool {
		return tool.CallCount() == 3
	})
	if got := tool.MaxConcurrency(); got != 3 {
		t.Fatalf("recorder failed to capture 3-way overlap: MaxConcurrency()=%d", got)
	}
	// 契约断言「峰值==2」对此工具必须不成立
	if tool.MaxConcurrency() == 2 {
		t.Fatal("counterexample broken: recorder reported 2 for a 3-way overlap")
	}

	tool.Release(3)
	wg.Wait()
}
