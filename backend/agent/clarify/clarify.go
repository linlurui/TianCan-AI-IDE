// Package clarify implements the intent-clarification state machine and the
// clarifier abstraction the agent loop uses to resolve ambiguous user intent.
//
// Design (per plan G9):
//   - Explicit open -> continued/settled lifecycle, tracked by Machine so the
//     loop can never silently lose a pending clarification.
//   - Timeout (or an offline user) surfaces as {pending:true}, NEVER as an
//     error. The non-interactive default (SilentClarifier) is fail-closed: it
//     always answers "pending" and never errors.
//   - The user's answer, once obtained, is fed back into the conversation as a
//     normal user message by the caller (see agent.go).
//
// The package is a leaf: it depends only on types-equivalent primitives,
// context, envconfig — no import of agent/safety/tools, so it cannot create a
// dependency cycle.
package clarify

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// Marker is the structured signal the model emits when it needs clarification.
// Everything after the marker (trimmed) is the clarification prompt. Production
// prompt-engineering instructs the model to use this marker; tests emit it
// directly through the mock adapter.
const Marker = "<<CLARIFY>>"

// State is a node of the clarification state machine.
type State int

const (
	// StateIdle: no clarification in progress.
	StateIdle State = iota
	// StateOpen: a clarification question was raised, awaiting the answer.
	StateOpen
	// StateContinued: another clarification round was raised before settling.
	StateContinued
	// StateSettled: clarification resolved, the loop has proceeded.
	StateSettled
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateOpen:
		return "open"
	case StateContinued:
		return "continued"
	case StateSettled:
		return "settled"
	default:
		return "unknown"
	}
}

// Machine tracks the open -> continued/settled clarification lifecycle.
// It is safe for concurrent use.
type Machine struct {
	mu     sync.Mutex
	state  State
	rounds int
}

// NewMachine returns an idle clarification machine.
func NewMachine() *Machine {
	return &Machine{state: StateIdle}
}

// Open records that a clarification question was raised.
// From Idle it moves to Open; from Open/Continued it moves to Continued (still
// clarifying). It returns the new state and the 1-based round number.
func (m *Machine) Open() (State, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rounds++
	switch m.state {
	case StateIdle:
		m.state = StateOpen
	case StateOpen, StateContinued:
		m.state = StateContinued
	}
	return m.state, m.rounds
}

// Settle records that the loop has proceeded past clarification. Moves to
// Settled from any state.
func (m *Machine) Settle() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = StateSettled
	return m.state
}

// State returns the current state.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// IsSettled reports whether clarification has been resolved.
func (m *Machine) IsSettled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == StateSettled
}

// Rounds returns how many clarification rounds have been opened.
func (m *Machine) Rounds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rounds
}

// Reset returns the machine to Idle (used between independent runs).
func (m *Machine) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = StateIdle
	m.rounds = 0
}

// Clarifier asks the (possibly offline) user for the intent behind prompt.
//
// Reporting contract — callers MUST honor it:
//   - answer != "": the user's answer; feed it back as a user message.
//   - pending == true: no answer yet (timeout / offline). Treat as
//     {pending:true}, NOT as an error.
//   - err != nil: transport failure. Callers should still prefer returning
//     pending over surfacing err, to honor the "timeout is not an error" rule.
type Clarifier interface {
	Clarify(ctx context.Context, prompt string, timeout time.Duration) (answer string, pending bool, err error)
}

// SilentClarifier is the non-interactive default: always reports pending and
// never errors (fail-closed — awaiting a user that isn't there).
type SilentClarifier struct{}

// Clarify implements Clarifier.
func (SilentClarifier) Clarify(ctx context.Context, prompt string, timeout time.Duration) (string, bool, error) {
	return "", true, nil
}

// EchoClarifier returns a fixed answer — used for open/interactive harnesses
// and as a test double.
type EchoClarifier struct{ Answer string }

// Clarify implements Clarifier.
func (c EchoClarifier) Clarify(ctx context.Context, prompt string, timeout time.Duration) (string, bool, error) {
	return c.Answer, false, nil
}

// TimeoutClarifier waits out the deadline (or ctx cancellation) and then
// reports pending — it exercises the timeout path without a real user.
type TimeoutClarifier struct{}

// Clarify implements Clarifier.
func (TimeoutClarifier) Clarify(ctx context.Context, prompt string, timeout time.Duration) (string, bool, error) {
	select {
	case <-ctx.Done():
		return "", true, nil
	case <-time.After(timeout):
		return "", true, nil
	}
}

// IsClarification reports whether text is a clarification request and extracts
// the prompt (everything after Marker, trimmed).
func IsClarification(text string) (string, bool) {
	idx := strings.Index(text, Marker)
	if idx < 0 {
		return "", false
	}
	prompt := strings.TrimSpace(text[idx+len(Marker):])
	return prompt, true
}

const (
	envClarifyTimeout = "TIANCAN_CLARIFY_TIMEOUT_SEC"
	envClarifyRounds  = "TIANCAN_CLARIFY_MAX_ROUNDS"
)

// DefaultTimeoutSec is the per-clarification deadline in seconds.
func DefaultTimeoutSec() int {
	return envconfig.EnvInt(envClarifyTimeout, 30)
}

// DefaultMaxRounds caps how many clarification rounds the loop will attempt
// before settling (to avoid an infinite ask/answer loop).
func DefaultMaxRounds() int {
	return envconfig.EnvInt(envClarifyRounds, 4)
}

// NewDefault returns the production default clarifier: the fail-closed silent
// (non-interactive) clarifier. The interactive UI answerer is wired in G16.
func NewDefault() Clarifier {
	return SilentClarifier{}
}
