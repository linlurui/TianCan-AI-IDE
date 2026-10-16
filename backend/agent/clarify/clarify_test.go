package clarify

import (
	"context"
	"testing"
	"time"
)

// --- State machine: open -> continued/settled ---

func TestMachine_Transitions(t *testing.T) {
	m := NewMachine()
	if m.State() != StateIdle {
		t.Fatalf("initial state = %v, want idle", m.State())
	}
	if m.IsSettled() {
		t.Fatalf("idle must not be settled")
	}

	s, r := m.Open()
	if s != StateOpen || r != 1 {
		t.Fatalf("1st Open = (%v,%d), want (open,1)", s, r)
	}

	s, r = m.Open() // still clarifying -> continued
	if s != StateContinued || r != 2 {
		t.Fatalf("2nd Open = (%v,%d), want (continued,2)", s, r)
	}

	s, r = m.Open() // stays continued
	if s != StateContinued || r != 3 {
		t.Fatalf("3rd Open = (%v,%d), want (continued,3)", s, r)
	}

	if m.IsSettled() {
		t.Fatalf("open/continued must not be settled")
	}

	s = m.Settle()
	if s != StateSettled || !m.IsSettled() {
		t.Fatalf("Settle = %v, want settled", s)
	}

	// Once settled, re-opening must not resurrect a pending clarification.
	s, _ = m.Open()
	if s != StateSettled {
		t.Fatalf("Open after Settle = %v, want settled (terminal)", s)
	}
}

func TestMachine_Reset(t *testing.T) {
	m := NewMachine()
	m.Open()
	m.Settle()
	m.Reset()
	if m.State() != StateIdle || m.Rounds() != 0 {
		t.Fatalf("Reset = (%v,%d), want (idle,0)", m.State(), m.Rounds())
	}
}

// --- IsClarification marker detection ---

func TestIsClarification(t *testing.T) {
	cases := []struct {
		in       string
		wantOk   bool
		wantPmt  string
	}{
		{Marker + "which file?", true, "which file?"},
		{"I need to know " + Marker + " which file to edit", true, "which file to edit"},
		{"no marker here", false, ""},
		{"", false, ""},
		// counterexample: "CLARIFY" alone (without the <<>> sentinel) is NOT a signal
		{"please CLARIFY this for me", false, ""},
	}
	for _, c := range cases {
		pmt, ok := IsClarification(c.in)
		if ok != c.wantOk || pmt != c.wantPmt {
			t.Fatalf("IsClarification(%q) = (%q,%v), want (%q,%v)", c.in, pmt, ok, c.wantPmt, c.wantOk)
		}
	}
}

// --- Clarifiers: the "timeout is not an error" rule ---

func TestSilentClarifier_PendingNotError(t *testing.T) {
	c := SilentClarifier{}
	ans, pending, err := c.Clarify(context.Background(), "p", 0)
	if ans != "" {
		t.Fatalf("silent clarifier must not fabricate an answer, got %q", ans)
	}
	if !pending {
		t.Fatalf("non-interactive default must report pending")
	}
	if err != nil {
		t.Fatalf("G9: silent clarifier must NOT surface an error (pending is not an error), got %v", err)
	}
}

func TestEchoClarifier_AnswerNotPending(t *testing.T) {
	c := EchoClarifier{Answer: "edit main.go"}
	ans, pending, err := c.Clarify(context.Background(), "p", 0)
	if ans != "edit main.go" {
		t.Fatalf("echo clarifier answer = %q, want edit main.go", ans)
	}
	if pending {
		t.Fatalf("an answered clarification must not be pending")
	}
	if err != nil {
		t.Fatalf("echo clarifier err = %v, want nil", err)
	}
}

func TestTimeoutClarifier_ReturnsPending(t *testing.T) {
	c := TimeoutClarifier{}
	done := make(chan struct{})
	go func() {
		ans, pending, err := c.Clarify(context.Background(), "p", 10*time.Millisecond)
		if ans != "" || !pending || err != nil {
			t.Errorf("timeout clarifier = (%q,%v,%v), want ('',true,nil)", ans, pending, err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout clarifier did not return within deadline")
	}
}

// --- Default config wiring ---

func TestDefaults(t *testing.T) {
	if DefaultTimeoutSec() <= 0 {
		t.Fatalf("default clarify timeout must be positive, got %d", DefaultTimeoutSec())
	}
	if DefaultMaxRounds() <= 0 {
		t.Fatalf("default max rounds must be positive, got %d", DefaultMaxRounds())
	}
	// NewDefault must be the fail-closed silent clarifier.
	if _, pending, _ := NewDefault().Clarify(context.Background(), "p", 0); !pending {
		t.Fatalf("default clarifier must be pending (fail-closed)")
	}
}
