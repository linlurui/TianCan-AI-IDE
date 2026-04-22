package parallel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Dependency Graph ─────────────────────────────────────────────

// DependencyGraph manages task dependencies for parallel execution.
type DependencyGraph struct {
	mu       sync.RWMutex
	edges    map[string][]string // task → dependencies
	revEdges map[string][]string // task → dependents
	tasks    map[string]*types.ParallelTask
}

// NewDependencyGraph creates a new dependency graph.
func NewDependencyGraph() *DependencyGraph {
	return &DependencyGraph{
		edges:    make(map[string][]string),
		revEdges: make(map[string][]string),
		tasks:    make(map[string]*types.ParallelTask),
	}
}

// AddTask adds a task with its dependencies.
func (dg *DependencyGraph) AddTask(task types.ParallelTask, deps []string) error {
	dg.mu.Lock()
	defer dg.mu.Unlock()

	// Check for cycles
	for _, dep := range deps {
		if dg.wouldCycle(task.ID, dep) {
			return fmt.Errorf("adding dependency %s→%s would create a cycle", task.ID, dep)
		}
	}

	taskCopy := task
	dg.tasks[task.ID] = &taskCopy
	dg.edges[task.ID] = deps
	for _, dep := range deps {
		dg.revEdges[dep] = append(dg.revEdges[dep], task.ID)
	}
	return nil
}

// GetReadyTasks returns tasks whose dependencies are all completed.
func (dg *DependencyGraph) GetReadyTasks(completed map[string]bool) []types.ParallelTask {
	dg.mu.RLock()
	defer dg.mu.RUnlock()

	var ready []types.ParallelTask
	for id, task := range dg.tasks {
		if task.Status != types.ParallelPending {
			continue
		}
		deps := dg.edges[id]
		allDone := true
		for _, dep := range deps {
			if !completed[dep] {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, *task)
		}
	}
	return ready
}

// GetDependents returns tasks that depend on the given task.
func (dg *DependencyGraph) GetDependents(taskID string) []string {
	dg.mu.RLock()
	defer dg.mu.RUnlock()
	return dg.revEdges[taskID]
}

// TopologicalSort returns tasks in a valid execution order.
func (dg *DependencyGraph) TopologicalSort() ([]string, error) {
	dg.mu.RLock()
	defer dg.mu.RUnlock()

	inDegree := make(map[string]int)
	for id := range dg.tasks {
		inDegree[id] = 0
	}
	for _, deps := range dg.edges {
		for _, dep := range deps {
			if _, ok := dg.tasks[dep]; ok {
				inDegree[dep] = inDegree[dep] // ensure exists
			}
		}
	}
	for id, deps := range dg.edges {
		inDegree[id] = len(deps)
	}

	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, dep := range dg.revEdges[id] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(order) != len(dg.tasks) {
		return nil, fmt.Errorf("cycle detected in dependency graph")
	}
	return order, nil
}

func (dg *DependencyGraph) wouldCycle(from, to string) bool {
	visited := make(map[string]bool)
	var dfs func(current string) bool
	dfs = func(current string) bool {
		if current == from {
			return true
		}
		if visited[current] {
			return false
		}
		visited[current] = true
		for _, dep := range dg.edges[current] {
			if dfs(dep) {
				return true
			}
		}
		return false
	}
	return dfs(to)
}

// ── Work Stealing Executor ──────────────────────────────────────

// WorkStealingExecutor distributes tasks across workers with work stealing.
type WorkStealingExecutor struct {
	maxWorkers int
	execFn     TaskExecFunc
}

// NewWorkStealingExecutor creates a work-stealing executor.
func NewWorkStealingExecutor(maxWorkers int, execFn TaskExecFunc) *WorkStealingExecutor {
	if maxWorkers <= 0 {
		maxWorkers = 4
	}
	return &WorkStealingExecutor{maxWorkers: maxWorkers, execFn: execFn}
}

// Execute runs tasks using work-stealing scheduling.
func (wse *WorkStealingExecutor) Execute(ctx context.Context, tasks []types.ParallelTask) []types.ParallelTask {
	if len(tasks) == 0 {
		return tasks
	}

	// Simple work-stealing: shared queue, workers pull when free
	taskCh := make(chan int, len(tasks))
	resultCh := make(chan taskResult, len(tasks))

	// Enqueue all tasks
	for i := range tasks {
		taskCh <- i
	}
	close(taskCh)

	// Start workers
	var wg sync.WaitGroup
	workers := wse.maxWorkers
	if workers > len(tasks) {
		workers = len(tasks)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range taskCh {
				select {
				case <-ctx.Done():
					resultCh <- taskResult{idx: idx, err: ctx.Err()}
					return
				default:
				}
				start := time.Now()
				result, err := wse.execFn(ctx, tasks[idx])
				dur := time.Since(start).Milliseconds()
				if err != nil {
					resultCh <- taskResult{idx: idx, err: err, duration: dur}
				} else {
					result.DurationMs = dur
					resultCh <- taskResult{idx: idx, result: result, duration: dur}
				}
			}
		}()
	}

	// Wait for all workers
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results
	for tr := range resultCh {
		if tr.err != nil {
			tasks[tr.idx].Status = types.ParallelFailed
			tasks[tr.idx].Result = &types.ParallelTaskResult{
				Error: tr.err.Error(), DurationMs: tr.duration,
			}
		} else {
			tasks[tr.idx].Status = types.ParallelCompleted
			tasks[tr.idx].Result = tr.result
		}
	}

	return tasks
}

type taskResult struct {
	idx      int
	result   *types.ParallelTaskResult
	err      error
	duration int64
}

// ── DAG Executor ────────────────────────────────────────────────

// DAGExecutor executes tasks respecting dependency ordering.
type DAGExecutor struct {
	graph    *DependencyGraph
	execFn   TaskExecFunc
	maxConc  int
}

// NewDAGExecutor creates a DAG-based executor.
func NewDAGExecutor(graph *DependencyGraph, execFn TaskExecFunc, maxConc int) *DAGExecutor {
	if maxConc <= 0 {
		maxConc = 4
	}
	return &DAGExecutor{graph: graph, execFn: execFn, maxConc: maxConc}
}

// Execute runs tasks respecting the dependency graph.
func (de *DAGExecutor) Execute(ctx context.Context) []types.ParallelTask {
	completed := make(map[string]bool)
	failed := make(map[string]bool)
	var allResults []types.ParallelTask

	for {
		ready := de.graph.GetReadyTasks(completed)
		if len(ready) == 0 {
			break
		}

		// Filter out tasks whose dependencies failed
		var runnable []types.ParallelTask
		for _, t := range ready {
			deps := de.graph.edges[t.ID]
			anyDepFailed := false
			for _, dep := range deps {
				if failed[dep] {
					anyDepFailed = true
					break
				}
			}
			if anyDepFailed {
				t.Status = types.ParallelFailed
				t.Result = &types.ParallelTaskResult{Error: "dependency failed"}
				allResults = append(allResults, t)
				failed[t.ID] = true
				completed[t.ID] = true
			} else {
				runnable = append(runnable, t)
			}
		}

		if len(runnable) == 0 {
			break
		}

		// Execute runnable tasks in parallel
		sem := make(chan struct{}, de.maxConc)
		var wg sync.WaitGroup
		var mu sync.Mutex

		for i := range runnable {
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				start := time.Now()
				result, err := de.execFn(ctx, runnable[idx])
				dur := time.Since(start).Milliseconds()

				mu.Lock()
				defer mu.Unlock()

				if err != nil {
					runnable[idx].Status = types.ParallelFailed
					runnable[idx].Result = &types.ParallelTaskResult{
						Error: err.Error(), DurationMs: dur,
					}
					failed[runnable[idx].ID] = true
				} else {
					runnable[idx].Status = types.ParallelCompleted
					result.DurationMs = dur
					runnable[idx].Result = result
				}
				completed[runnable[idx].ID] = true
				allResults = append(allResults, runnable[idx])
			}(i)
		}
		wg.Wait()
	}

	return allResults
}

// ── Result Aggregator ───────────────────────────────────────────

// ResultAggregator safely aggregates results from parallel tasks.
type ResultAggregator struct {
	mu      sync.Mutex
	results map[string]*types.ParallelTaskResult
	errors  []error
}

// NewResultAggregator creates a concurrent-safe result aggregator.
func NewResultAggregator() *ResultAggregator {
	return &ResultAggregator{
		results: make(map[string]*types.ParallelTaskResult),
	}
}

// Add adds a task result.
func (ra *ResultAggregator) Add(taskID string, result *types.ParallelTaskResult, err error) {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	if err != nil {
		ra.errors = append(ra.errors, fmt.Errorf("task %s: %w", taskID, err))
	}
	ra.results[taskID] = result
}

// GetResult returns the result for a task.
func (ra *ResultAggregator) GetResult(taskID string) (*types.ParallelTaskResult, bool) {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	r, ok := ra.results[taskID]
	return r, ok
}

// GetAllResults returns all results.
func (ra *ResultAggregator) GetAllResults() map[string]*types.ParallelTaskResult {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	cp := make(map[string]*types.ParallelTaskResult)
	for k, v := range ra.results {
		cp[k] = v
	}
	return cp
}

// HasErrors returns whether any task failed.
func (ra *ResultAggregator) HasErrors() bool {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	return len(ra.errors) > 0
}

// GetErrors returns all errors.
func (ra *ResultAggregator) GetErrors() []error {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	return ra.errors
}

// AggregateContent combines all successful task content.
func (ra *ResultAggregator) AggregateContent() string {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	var content string
	for _, r := range ra.results {
		if r != nil && r.Error == "" {
			content += r.Content + "\n"
		}
	}
	return content
}

// Summary returns a summary of all results.
func (ra *ResultAggregator) Summary() string {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	total := len(ra.results)
	failed := len(ra.errors)
	success := total - failed
	return fmt.Sprintf("Tasks: %d total, %d succeeded, %d failed", total, success, failed)
}
