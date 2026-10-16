package approval

import (
	"context"
	"errors"
	"testing"
)

// recordingAnswerer records whether Answer was called and returns a canned value.
type recordingAnswerer struct {
	calls int
	out   Outcome
	err   error
}

func (a *recordingAnswerer) Answer(ctx context.Context, req Request) (Outcome, error) {
	a.calls++
	return a.out, a.err
}

// weirdAnswerer returns a value outside the closed set — exercises the coercion.
type weirdAnswerer struct{ out Outcome }

func (a weirdAnswerer) Answer(ctx context.Context, req Request) (Outcome, error) {
	return a.out, nil
}

// TestClosedSet_IsExactlyFour proves the set is a closed set: exactly four
// distinct values and nothing can widen it.
func TestClosedSet_IsExactlyFour(t *testing.T) {
	got := ClosedSet()
	if len(got) != 4 {
		t.Fatalf("closed set must have exactly 4 values, got %d: %v", len(got), got)
	}
	seen := make(map[Outcome]bool, len(got))
	for _, o := range got {
		if seen[o] {
			t.Fatalf("duplicate value in closed set: %q", o)
		}
		seen[o] = true
	}
	for _, want := range []Outcome{OutcomeAllowedOnce, OutcomeRejected, OutcomeCancelled, OutcomeUnavailable} {
		if !seen[want] {
			t.Fatalf("closed set missing %q", want)
		}
	}
}

// TestIsDenial pins the decision boundary: only allowed-once permits; the other
// three (including the fail-closed unavailable) are denials.
func TestIsDenial(t *testing.T) {
	if (OutcomeAllowedOnce).IsDenial() {
		t.Fatalf("allowed-once must NOT be a denial")
	}
	for _, o := range []Outcome{OutcomeRejected, OutcomeCancelled, OutcomeUnavailable} {
		if !o.IsDenial() {
			t.Fatalf("%q must be a denial", o)
		}
	}
}

// TestResolve_NeverBeforeDispatch proves the never-strategy is decided
// synchronously, BEFORE the answerer is dispatched: a never-flagged tool is
// rejected even with a permissive answerer, and the answerer is never consulted.
func TestResolve_NeverBeforeDispatch(t *testing.T) {
	ans := &recordingAnswerer{out: OutcomeAllowedOnce}
	s := NewApprovalSystem(ans)
	s.SetNever([]string{"deploy_widget"})

	out, err := s.Resolve(context.Background(), Request{ToolName: "deploy_widget"})
	if err != nil {
		t.Fatalf("never-reject must return no error, got %v", err)
	}
	if out != OutcomeRejected {
		t.Fatalf("never-flagged tool must be Rejected, got %q", out)
	}
	if ans.calls != 0 {
		t.Fatalf("answerer must NOT be dispatched for a never tool (got %d calls)", ans.calls)
	}
}

// TestResolve_SetNeverTrimsWhitespace proves SetNever normalizes input.
func TestResolve_SetNeverTrimsWhitespace(t *testing.T) {
	s := NewApprovalSystem(NewPermissiveAnswerer())
	s.SetNever([]string{"  rm_rf , bash "})

	out, _ := s.Resolve(context.Background(), Request{ToolName: "rm_rf"})
	if out != OutcomeRejected {
		t.Fatalf("never match (trimmed) failed, got %q", out)
	}
	out, _ = s.Resolve(context.Background(), Request{ToolName: "bash"})
	if out != OutcomeRejected {
		t.Fatalf("never match (trimmed, second entry) failed, got %q", out)
	}
	out, _ = s.Resolve(context.Background(), Request{ToolName: "git"})
	if out != OutcomeAllowedOnce {
		t.Fatalf("non-never tool should reach answerer, got %q", out)
	}
}

// TestResolve_PermissiveAnswerer reaches the answerer and gets allowed-once.
func TestResolve_PermissiveAnswerer(t *testing.T) {
	ans := &recordingAnswerer{out: OutcomeAllowedOnce}
	s := NewApprovalSystem(ans)
	out, err := s.Resolve(context.Background(), Request{ToolName: "deploy_widget"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != OutcomeAllowedOnce {
		t.Fatalf("expected allowed-once, got %q", out)
	}
	if ans.calls != 1 {
		t.Fatalf("answerer should be called exactly once, got %d", ans.calls)
	}
}

// TestResolve_RejectingAnswerer is the non-interactive default: unavailable.
func TestResolve_RejectingAnswerer(t *testing.T) {
	s := NewApprovalSystem(NewRejectingAnswerer())
	out, err := s.Resolve(context.Background(), Request{ToolName: "deploy_widget"})
	if err != nil {
		t.Fatalf("rejecting answerer must not error, got %v", err)
	}
	if out != OutcomeUnavailable {
		t.Fatalf("non-interactive default must be Unavailable, got %q", out)
	}
}

// TestResolve_AnswererError_FailClosed proves an answerer error is coerced to
// Unavailable (fail-closed) and the error is still surfaced for logging.
func TestResolve_AnswererError_FailClosed(t *testing.T) {
	boom := errors.New("answerer exploded")
	s := NewApprovalSystem(ErroringAnswerer{Err: boom})
	out, err := s.Resolve(context.Background(), Request{ToolName: "deploy_widget"})
	if out != OutcomeUnavailable {
		t.Fatalf("answerer error must coerce to Unavailable, got %q", out)
	}
	if err == nil {
		t.Fatalf("underlying error must propagate for logging")
	}
}

// TestResolve_OutOfSetCoerced proves a misbehaving answerer cannot widen the
// closed set: any out-of-set value is coerced to Unavailable (fail-closed).
func TestResolve_OutOfSetCoerced(t *testing.T) {
	s := NewApprovalSystem(weirdAnswerer{out: "maybe-allow"})
	out, _ := s.Resolve(context.Background(), Request{ToolName: "deploy_widget"})
	if out != OutcomeUnavailable {
		t.Fatalf("out-of-set answerer value must coerce to Unavailable, got %q", out)
	}
}

// TestResolve_NoAnswerer_FailClosed proves a system with no wired answerer fails
// closed to Unavailable rather than granting permission.
func TestResolve_NoAnswerer_FailClosed(t *testing.T) {
	s := NewApprovalSystem(nil)
	out, _ := s.Resolve(context.Background(), Request{ToolName: "deploy_widget"})
	if out != OutcomeUnavailable {
		t.Fatalf("nil answerer must fail closed to Unavailable, got %q", out)
	}
}

// TestDenialReason_ContainsToolName is a cheap regression guard so the user-facing
// message always identifies the denied tool.
func TestDenialReason_ContainsToolName(t *testing.T) {
	for _, o := range []Outcome{OutcomeRejected, OutcomeCancelled, OutcomeUnavailable} {
		if msg := DenialReason(o, "deploy_widget"); !contains(msg, "deploy_widget") {
			t.Fatalf("DenialReason(%q) must mention the tool, got %q", o, msg)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
