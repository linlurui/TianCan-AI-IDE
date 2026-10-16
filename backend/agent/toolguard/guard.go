// Package toolguard provides the per-tool execution guard (G6):
//   - a deadline wrapper that distinguishes a parent-context cancellation from
//     the tool's own deadline, and
//   - a duplicate / rejected-call detector that redirects a model which keeps
//     hammering the same denied call.
//
// It is a leaf package: it depends only on types (leaf) and envconfig (leaf),
// so the agent and the tools packages can import it without import cycles.
package toolguard

import (
	"context"
	"fmt"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// envToolTimeout tunes the per-tool hard deadline (seconds). 0 disables the own
// deadline so only the parent context governs cancellation.
const envToolTimeout = "TIANCAN_TOOL_TIMEOUT_SEC"

// ErrParentCancelled is returned by Guard.Execute when the tool aborted because
// the *parent* context was cancelled (e.g. the agent loop / user aborted), NOT
// because the tool hit its own per-tool deadline.
type ErrParentCancelled struct{ Err error }

func (e *ErrParentCancelled) Error() string {
	return fmt.Sprintf("tool aborted: parent context cancelled (%v)", e.Err)
}
func (e *ErrParentCancelled) Unwrap() error { return e.Err }

// ErrDeadline is returned by Guard.Execute when the tool exceeded its own
// per-tool deadline while the parent context was still alive.
type ErrDeadline struct{ Deadline time.Duration }

func (e *ErrDeadline) Error() string {
	return fmt.Sprintf("tool exceeded its deadline of %s", e.Deadline)
}

// Config tunes the guard.
type Config struct {
	// Timeout is the per-tool hard deadline. Zero disables the own deadline so
	// only the parent context governs cancellation.
	Timeout time.Duration
}

// DefaultConfig reads the deadline from TIANCAN_TOOL_TIMEOUT_SEC (default 120s).
func DefaultConfig() Config {
	return Config{Timeout: time.Duration(envconfig.EnvInt(envToolTimeout, 120)) * time.Second}
}

// Runner executes a tool under a (possibly derived) context.
type Runner func(ctx context.Context) (types.ToolResult, error)

type execResult struct {
	res types.ToolResult
	err error
}

// Guard applies the per-tool deadline and attributes a timeout correctly.
type Guard struct {
	cfg Config
}

// New creates a Guard from cfg.
func New(cfg Config) *Guard { return &Guard{cfg: cfg} }

// Execute runs r under a deadline derived from parent (when TimeoutSec > 0).
// It returns:
//   - the tool result, if the tool finished before the deadline;
//   - *ErrParentCancelled if the parent context was cancelled (regardless of the
//     deadline);
//   - *ErrDeadline if only the derived deadline fired while the parent was alive.
//
// The distinction matters: a tool that ignores its context would otherwise look
// identical whether the user aborted the whole run or the tool simply hung. By
// querying parent.Err() first we report the true cause.
func (g *Guard) Execute(parent context.Context, r Runner) (types.ToolResult, error) {
	if g.cfg.Timeout <= 0 {
		return r(parent)
	}
	deadline := g.cfg.Timeout
	execCtx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()

	done := make(chan execResult, 1)
	go func() {
		res, err := r(execCtx)
		done <- execResult{res: res, err: err}
	}()

	select {
	case out := <-done:
		return out.res, out.err
	case <-execCtx.Done():
		if parent.Err() != nil {
			return types.ToolResult{}, &ErrParentCancelled{Err: parent.Err()}
		}
		return types.ToolResult{}, &ErrDeadline{Deadline: deadline}
	}
}
