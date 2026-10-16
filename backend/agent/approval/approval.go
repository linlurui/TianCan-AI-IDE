// Package approval implements the user-approval four-value closed set with
// fail-closed semantics (G8).
//
// The closed set of answers to a permission prompt is exactly four values:
//
//	allowed-once  — the user allowed this specific invocation
//	rejected      — the user explicitly rejected
//	cancelled     — the user cancelled the prompt (ESC / closed / timeout)
//	unavailable   — the answerer could not produce an answer (fail-closed)
//
// Fail-closed guarantee: any error from the answerer is coerced to
// `unavailable`, and `unavailable` (like `rejected` / `cancelled`) is a denial.
// The ONLY value that permits execution is `allowed-once`. Nothing outside this
// set can widen it — that is what "closed set" buys us.
//
// The `never` strategy is resolved synchronously inside Resolve, BEFORE the
// answerer is ever dispatched, so a tool flagged `never` is rejected without any
// user round-trip and without any tool dispatch ("decided before any dispatch").
package approval

import (
	"context"
	"strings"
	"sync"

	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// Outcome is one of exactly four values — the closed set of approval answers.
type Outcome string

const (
	OutcomeAllowedOnce Outcome = "allowed-once"
	OutcomeRejected    Outcome = "rejected"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeUnavailable Outcome = "unavailable"
)

// IsDenial reports whether an outcome denies execution.
// Only OutcomeAllowedOnce permits the tool; every other value — including the
// fail-closed OutcomeUnavailable — is a denial. This single predicate is the
// closed set's decision boundary.
func (o Outcome) IsDenial() bool {
	return o != OutcomeAllowedOnce
}

// Request describes a permission prompt that needs an answer.
type Request struct {
	ToolName string
	Args     map[string]interface{}
	Reason   string // why confirmation is required (from the safety check)
	Level    int    // safety level that demanded confirmation
}

// Answerer answers a permission prompt. Implementations may be interactive
// (UI prompt) or programmatic. An error (or returning OutcomeUnavailable)
// means the prompt could not be satisfied and the caller must deny.
type Answerer interface {
	Answer(ctx context.Context, req Request) (Outcome, error)
}

// ApprovalSystem resolves permission requests into the four-value closed set.
type ApprovalSystem struct {
	answerer Answerer
	never    map[string]bool
	denyAll  bool // G10: preset "never" — short-circuit every request to Rejected
	mu       sync.RWMutex
}

// NewApprovalSystem builds an approval system with the given answerer.
// The `never` set starts empty; populate it via SetNever or NewDefault.
func NewApprovalSystem(answerer Answerer) *ApprovalSystem {
	return &ApprovalSystem{
		answerer: answerer,
		never:    make(map[string]bool),
	}
}

// NewDefault builds the production approval system: the rejecting answerer
// (non-interactive default, fail-closed) plus the never-list from
// TIANCAN_APPROVAL_NEVER.
func NewDefault() *ApprovalSystem {
	s := NewApprovalSystem(NewRejectingAnswerer())
	if list := envconfig.EnvList("TIANCAN_APPROVAL_NEVER"); list != nil {
		s.SetNever(list)
	}
	return s
}

// SetNever replaces the never-ask tool set. A tool in this set is always
// rejected before the answerer is dispatched. Each element may itself be a
// comma-separated list (mirroring the TIANCAN_APPROVAL_NEVER env format), and
// surrounding whitespace is trimmed.
func (s *ApprovalSystem) SetNever(tools []string) {
	m := make(map[string]bool)
	for _, group := range tools {
		for _, t := range strings.Split(group, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				m[t] = true
			}
		}
	}
	s.mu.Lock()
	s.never = m
	s.mu.Unlock()
}

// SetAnswerer reseats the answerer at runtime (used by G10 permission presets
// to switch the approval knob between ask / allow / never behaviour).
func (s *ApprovalSystem) SetAnswerer(a Answerer) {
	s.mu.Lock()
	s.answerer = a
	s.mu.Unlock()
}

// SetDenyAll toggles a global deny-all that short-circuits Resolve to Rejected
// before the never-set and before dispatching the answerer. Used by the G10
// "never" preset — every tool is rejected regardless of name.
func (s *ApprovalSystem) SetDenyAll(b bool) {
	s.mu.Lock()
	s.denyAll = b
	s.mu.Unlock()
}

// Resolve returns the closed-set outcome for a request.
//
// Order:
//  1. never-strategy — if the tool is in the never set, return Rejected
//     synchronously, before dispatching the answerer (G8: decided before any
//     dispatch).
//  2. otherwise dispatch the answerer. On error, coerce to Unavailable
//     (fail-closed). The returned Outcome is always one of the four values.
func (s *ApprovalSystem) Resolve(ctx context.Context, req Request) (Outcome, error) {
	s.mu.RLock()
	denyAll := s.denyAll
	never := s.never[req.ToolName]
	s.mu.RUnlock()
	if denyAll || never {
		return OutcomeRejected, nil
	}

	if s.answerer == nil {
		// No answerer wired — fail closed: approval cannot be obtained.
		return OutcomeUnavailable, nil
	}

	out, err := s.answerer.Answer(ctx, req)
	if err != nil {
		// Fail-closed: an answerer error must never grant permission.
		return OutcomeUnavailable, err
	}

	// Defensively coerce any out-of-set value to the closest denial so the
	// closed set can never be widened by a misbehaving answerer.
	switch out {
	case OutcomeAllowedOnce, OutcomeRejected, OutcomeCancelled, OutcomeUnavailable:
		return out, nil
	default:
		return OutcomeUnavailable, nil
	}
}

// RejectingAnswerer is the non-interactive default: it cannot prompt the user,
// so it answers Unavailable — which fails closed into a denial.
type RejectingAnswerer struct{}

// NewRejectingAnswerer builds the fail-closed non-interactive answerer.
func NewRejectingAnswerer() *RejectingAnswerer { return &RejectingAnswerer{} }

// Answer always answers Unavailable (no UI to prompt the user).
func (RejectingAnswerer) Answer(ctx context.Context, req Request) (Outcome, error) {
	return OutcomeUnavailable, nil
}

// PermissiveAnswerer always grants the request (trusted / interactive contexts
// and tests that simulate a user allowing).
type PermissiveAnswerer struct{}

// NewPermissiveAnswerer builds an answerer that always allows.
func NewPermissiveAnswerer() *PermissiveAnswerer { return &PermissiveAnswerer{} }

// Answer always answers allowed-once.
func (PermissiveAnswerer) Answer(ctx context.Context, req Request) (Outcome, error) {
	return OutcomeAllowedOnce, nil
}

// ErroringAnswerer always returns an error, to exercise the fail-closed path.
type ErroringAnswerer struct{ Err error }

// Answer returns its configured error.
func (a ErroringAnswerer) Answer(ctx context.Context, req Request) (Outcome, error) {
	return "", a.Err
}

// DenialReason returns a human-readable denial message for a denial outcome.
func DenialReason(o Outcome, toolName string) string {
	switch o {
	case OutcomeRejected:
		return "user rejected " + toolName
	case OutcomeCancelled:
		return "approval cancelled for " + toolName
	case OutcomeUnavailable:
		return "approval unavailable (non-interactive / answerer error) — " + toolName + " denied (fail-closed)"
	default:
		return "permission denied: " + toolName
	}
}

// allowedValues is the canonical closed set, used by tests and documentation.
var allowedValues = []Outcome{
	OutcomeAllowedOnce, OutcomeRejected, OutcomeCancelled, OutcomeUnavailable,
}

// ClosedSet returns the four canonical values. Tests use it to prove the set is
// exactly four and finite (no widening is possible anywhere in the codebase).
func ClosedSet() []Outcome { return allowedValues }
