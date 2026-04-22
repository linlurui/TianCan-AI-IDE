package parallel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// Executor manages parallel execution of agent tasks.
type Executor struct {
	maxConcurrency int
	results        map[string]*types.ParallelTaskResult
	mu             sync.Mutex
}

// NewExecutor creates a new parallel executor.
func NewExecutor(maxConcurrency int) *Executor {
	if maxConcurrency <= 0 {
		maxConcurrency = 4
	}
	return &Executor{
		maxConcurrency: maxConcurrency,
		results:        make(map[string]*types.ParallelTaskResult),
	}
}

// ExecuteTasks runs multiple tasks in parallel, respecting concurrency limits.
// Returns results for all tasks once all are complete.
func (e *Executor) ExecuteTasks(ctx context.Context, tasks []types.ParallelTask, execFn TaskExecFunc) []types.ParallelTask {
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.maxConcurrency)

	for i := range tasks {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			tasks[idx].Status = types.ParallelRunning
			start := time.Now()

			result, err := execFn(ctx, tasks[idx])
			if err != nil {
				tasks[idx].Status = types.ParallelFailed
				tasks[idx].Result = &types.ParallelTaskResult{
					Error:      err.Error(),
					DurationMs: time.Since(start).Milliseconds(),
				}
			} else {
				tasks[idx].Status = types.ParallelCompleted
				result.DurationMs = time.Since(start).Milliseconds()
				tasks[idx].Result = result
			}

			e.mu.Lock()
			e.results[tasks[idx].ID] = tasks[idx].Result
			e.mu.Unlock()
		}(i)
	}

	wg.Wait()
	return tasks
}

// ExecuteSequential runs tasks one after another, passing results forward.
func (e *Executor) ExecuteSequential(ctx context.Context, tasks []types.ParallelTask, execFn TaskExecFunc) []types.ParallelTask {
	for i := range tasks {
		tasks[i].Status = types.ParallelRunning
		start := time.Now()

		result, err := execFn(ctx, tasks[i])
		if err != nil {
			tasks[i].Status = types.ParallelFailed
			tasks[i].Result = &types.ParallelTaskResult{
				Error:      err.Error(),
				DurationMs: time.Since(start).Milliseconds(),
			}
			// Stop on first failure in sequential mode
			for j := i + 1; j < len(tasks); j++ {
				tasks[j].Status = types.ParallelFailed
				tasks[j].Result = &types.ParallelTaskResult{
					Error: fmt.Sprintf("Skipped due to failure of task %s", tasks[i].ID),
				}
			}
			return tasks
		}

		tasks[i].Status = types.ParallelCompleted
		result.DurationMs = time.Since(start).Milliseconds()
		tasks[i].Result = result
	}
	return tasks
}

// GetResult returns the result for a completed task.
func (e *Executor) GetResult(taskID string) *types.ParallelTaskResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.results[taskID]
}

// TaskExecFunc is the function that executes a single task.
type TaskExecFunc func(ctx context.Context, task types.ParallelTask) (*types.ParallelTaskResult, error)
