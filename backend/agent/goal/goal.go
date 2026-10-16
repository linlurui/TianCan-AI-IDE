// Package goal implements the goal-driven + plan-mode state machine (G12).
//
// A goal is a small, immutable-on-read value carrying:
//   - a description (the objective / plan text),
//   - a Revision (a compare-and-swap token guarding external writers),
//   - a Mode (none / plan),
//   - a round budget (MaxRounds) and how many rounds have been consumed (Rounds),
//   - a Done flag marking a settled goal.
//
// The Store serializes every mutation behind a mutex — that mutex is the
// "竞态栅栏" (race barrier) that prevents lost updates across concurrent writers.
// External writers must supply the expected Revision; a mismatch (a stale writer)
// is rejected with ErrRevisionConflict, so two concurrent editors cannot both
// succeed and clobber each other.
//
// Plan mode does NOT intercept tools. Enforcement is delegated to the sandbox /
// approval subsystems (G4 / G8): entering plan mode toggles the approval system's
// deny-all, so any tool requiring approval is rejected by the fail-closed path —
// not by a tool interceptor. The plan-mode paragraph in the system prompt is only
// guidance; the hard stop comes from the approval system.
//
// The package depends only on the standard library, so it can be imported by
// backend/agent without creating an import cycle.
package goal

import (
	"errors"
	"sync"
)

// Mode classifies the goal's current operating mode.
type Mode string

const (
	// ModeNone is the default operating mode (execute freely).
	ModeNone Mode = ""
	// ModePlan is plan mode: propose, don't execute mutating tools.
	ModePlan Mode = "plan"
)

// Goal is an immutable-on-read snapshot of the goal state.
type Goal struct {
	// Description is the objective / plan text.
	Description string
	// Revision is the CAS token. Every successful mutation bumps it by one.
	Revision int64
	// Mode is the current operating mode.
	Mode Mode
	// MaxRounds is the round budget; 0 means unlimited.
	MaxRounds int
	// Rounds is how many rounds have been consumed so far.
	Rounds int
	// Done marks a settled goal (no further mutations allowed).
	Done bool
}

// Errors returned by the Store.
var (
	// ErrRevisionConflict means the expected revision did not match the current
	// one — a stale writer tried to apply an outdated view.
	ErrRevisionConflict = errors.New("goal: revision conflict (stale writer)")
	// ErrBudgetExhausted means the round budget has been spent (fail-closed).
	ErrBudgetExhausted = errors.New("goal: round budget exhausted")
	// ErrTerminal means the goal is already Done and cannot be mutated further.
	ErrTerminal = errors.New("goal: goal is already done")
)

// Store owns the current Goal and serializes every mutation behind a mutex (the
// race barrier). It is safe for concurrent use.
type Store struct {
	mu  sync.Mutex
	cur Goal
}

// NewStore creates a Store with an empty goal and an unlimited round budget.
func NewStore() *Store {
	return &Store{cur: Goal{MaxRounds: 0}}
}

// Get returns a copy of the current goal snapshot.
func (s *Store) Get() Goal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Set applies g only if the Store's current revision equals expect (CAS). On
// success it bumps the revision and returns the new snapshot. A mismatched
// expect yields ErrRevisionConflict — the caller must re-read and retry.
//
// g.Revision is ignored; the Store controls the revision counter.
func (s *Store) Set(g Goal, expect int64) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur.Revision != expect {
		return s.cur, ErrRevisionConflict
	}
	g.Revision = expect + 1
	s.cur = g
	return s.cur, nil
}

// EnterPlanMode flips the goal into plan mode with the given description, via CAS
// against the current revision.
func (s *Store) EnterPlanMode(description string) (Goal, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	g := cur
	g.Description = description
	g.Mode = ModePlan
	return s.Set(g, cur.Revision)
}

// ExitPlanMode leaves plan mode (reverts to ModeNone), via CAS.
func (s *Store) ExitPlanMode() (Goal, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	g := cur
	g.Mode = ModeNone
	return s.Set(g, cur.Revision)
}

// SetBudget changes the round budget (0 = unlimited), via CAS.
func (s *Store) SetBudget(maxRounds int) (Goal, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	g := cur
	g.MaxRounds = maxRounds
	return s.Set(g, cur.Revision)
}

// SetDone marks the goal as settled, via CAS.
func (s *Store) SetDone() (Goal, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	g := cur
	g.Done = true
	return s.Set(g, cur.Revision)
}

// ConsumeRound spends one round of the budget. It returns ErrTerminal if the
// goal is already Done, and ErrBudgetExhausted (fail-closed) once Rounds reaches
// MaxRounds (when MaxRounds > 0). Each successful consume bumps the revision,
// so external CAS writers are forced to re-read after a round advances.
func (s *Store) ConsumeRound() (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur.Done {
		return s.cur, ErrTerminal
	}
	if s.cur.MaxRounds > 0 && s.cur.Rounds >= s.cur.MaxRounds {
		return s.cur, ErrBudgetExhausted
	}
	s.cur.Rounds++
	s.cur.Revision++
	return s.cur, nil
}
