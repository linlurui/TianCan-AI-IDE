package goal_test

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/goal"
)

// --- CAS: revision guard -----------------------------------------------------

func TestStore_Set_CASSuccessBumpsRevision(t *testing.T) {
	s := goal.NewStore()
	cur := s.Get()
	if cur.Revision != 0 {
		t.Fatalf("initial revision must be 0, got %d", cur.Revision)
	}
	g, err := s.Set(goal.Goal{Description: "x"}, cur.Revision)
	if err != nil {
		t.Fatalf("first Set should succeed: %v", err)
	}
	if g.Revision != 1 {
		t.Fatalf("revision must bump to 1, got %d", g.Revision)
	}
	// Re-applying with the NOW-stale expect must fail.
	if _, err := s.Set(goal.Goal{Description: "y"}, cur.Revision); !errors.Is(err, goal.ErrRevisionConflict) {
		t.Fatalf("re-apply with stale expect must fail, got %v", err)
	}
}

// 反证: a stale writer (one that read an old revision) MUST be rejected. If Set
// ignored the expected revision, writer A's outdated view would silently clobber
// writer B's newer write — and this test would pass incorrectly.
func TestStore_Counterexample_StaleWriterRejected(t *testing.T) {
	s := goal.NewStore()
	a := s.Get() // revision 0 snapshot held by writer A

	// Writer B advances the store.
	if _, err := s.EnterPlanMode("written by B"); err != nil {
		t.Fatal(err)
	}
	if s.Get().Revision != 1 {
		t.Fatalf("after B's write revision should be 1, got %d", s.Get().Revision)
	}

	// Writer A applies its stale view (expects revision 0).
	stale := a
	stale.Description = "written by A"
	if _, err := s.Set(stale, a.Revision); !errors.Is(err, goal.ErrRevisionConflict) {
		t.Fatalf("stale writer must be rejected with ErrRevisionConflict, got %v", err)
	}
	// The store must still reflect B's write, not A's clobber.
	if s.Get().Description != "written by B" {
		t.Fatalf("store must reflect B's write, got %q", s.Get().Description)
	}
}

// --- Plan mode ---------------------------------------------------------------

func TestStore_EnterExitPlanMode(t *testing.T) {
	s := goal.NewStore()
	g, err := s.EnterPlanMode("refactor auth")
	if err != nil {
		t.Fatal(err)
	}
	if g.Mode != goal.ModePlan {
		t.Fatalf("mode must be plan, got %q", g.Mode)
	}
	if g.Description != "refactor auth" {
		t.Fatalf("description must be set, got %q", g.Description)
	}
	// A stale exit (expect old revision) must fail.
	if _, err := s.ExitPlanMode(); err != nil {
		t.Fatalf("fresh ExitPlanMode should succeed: %v", err)
	}
	if s.Get().Mode != goal.ModeNone {
		t.Fatalf("mode must revert to none, got %q", s.Get().Mode)
	}
}

// --- Round budget ------------------------------------------------------------

func TestStore_ConsumeRound_BudgetExhausted(t *testing.T) {
	s := goal.NewStore()
	if _, err := s.SetBudget(2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeRound(); err != nil {
		t.Fatalf("round 1 should succeed: %v", err)
	}
	if _, err := s.ConsumeRound(); err != nil {
		t.Fatalf("round 2 should succeed: %v", err)
	}
	// Third round must be fail-closed.
	if _, err := s.ConsumeRound(); !errors.Is(err, goal.ErrBudgetExhausted) {
		t.Fatalf("round 3 must be ErrBudgetExhausted, got %v", err)
	}
	// Rounds must be capped exactly at the budget, never exceeded.
	if s.Get().Rounds != 2 {
		t.Fatalf("Rounds must cap at 2, got %d", s.Get().Rounds)
	}
	// And a further consume still fails closed.
	if _, err := s.ConsumeRound(); !errors.Is(err, goal.ErrBudgetExhausted) {
		t.Fatalf("subsequent consume must still fail closed, got %v", err)
	}
}

func TestStore_ConsumeRound_UnlimitedWhenZero(t *testing.T) {
	s := goal.NewStore() // default MaxRounds == 0
	for i := 0; i < 50; i++ {
		if _, err := s.ConsumeRound(); err != nil {
			t.Fatalf("with unlimited budget round %d must not fail: %v", i, err)
		}
	}
	if s.Get().Rounds != 50 {
		t.Fatalf("Rounds should track consumption, got %d", s.Get().Rounds)
	}
}

func TestStore_SetDone_Terminal(t *testing.T) {
	s := goal.NewStore()
	if _, err := s.SetDone(); err != nil {
		t.Fatal(err)
	}
	if !s.Get().Done {
		t.Fatal("goal must be Done")
	}
	if _, err := s.ConsumeRound(); !errors.Is(err, goal.ErrTerminal) {
		t.Fatalf("mutating a done goal must be ErrTerminal, got %v", err)
	}
}

// --- Barrier (竞态栅栏) ------------------------------------------------------

func TestBarrier_PartiesOneNoOp(t *testing.T) {
	b := goal.NewBarrier(1)
	b.Wait() // must return immediately, no deadlock
	b.Wait()
}

func TestBarrier_ReleasesAllParties(t *testing.T) {
	const n = 5
	b := goal.NewBarrier(n)
	var released atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			b.Wait()
			released.Add(1)
		}()
	}
	close(start)
	wg.Wait()
	if released.Load() != int32(n) {
		t.Fatalf("barrier released %d parties, want %d", released.Load(), n)
	}
}

// TestBarrier_BlocksUntilAllArrive proves the barrier actually fences: the first
// n-1 parties must remain blocked (released == 0) until the final party arrives.
func TestBarrier_BlocksUntilAllArrive(t *testing.T) {
	const n = 4
	b := goal.NewBarrier(n)
	var arrived, released int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n-1; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			mu.Lock()
			arrived++
			mu.Unlock()
			b.Wait()
			mu.Lock()
			released++
			mu.Unlock()
		}()
	}
	close(start)
	// Spin until n-1 parties have arrived (and are therefore blocked in Wait).
	for {
		mu.Lock()
		a := arrived
		mu.Unlock()
		if a >= int32(n-1) {
			break
		}
		runtime.Gosched()
	}
	mu.Lock()
	rel := released
	mu.Unlock()
	if rel != 0 {
		t.Fatalf("barrier released %d parties before the last arrived (fence broken)", rel)
	}
	// The main goroutine is the final party -> all release.
	b.Wait()
	wg.Wait()
	mu.Lock()
	finalRel := released
	mu.Unlock()
	if finalRel != int32(n-1) {
		t.Fatalf("after last party arrived, released=%d want %d", finalRel, n-1)
	}
}

func TestBarrier_Reusable(t *testing.T) {
	const n = 3
	b := goal.NewBarrier(n)
	runCohort := func() {
		var released atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				b.Wait()
				released.Add(1)
			}()
		}
		close(start)
		wg.Wait()
		if released.Load() != int32(n) {
			t.Errorf("cohort released %d want %d", released.Load(), n)
		}
	}
	// Two successive generations must each release all parties.
	runCohort()
	runCohort()
}
