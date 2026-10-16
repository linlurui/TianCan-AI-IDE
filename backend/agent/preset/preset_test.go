package preset

import (
	"errors"
	"testing"
)

// recordingApplier counts how often each applier is invoked and captures the
// last value, so we can prove appliers fire ONLY on a real change.
type recordingApplier struct {
	sandboxCalls int
	approvalCalls int
	lastSandbox  SandboxMode
	lastApproval ApprovalMode
}

func (a *recordingApplier) ApplySandbox(m SandboxMode) {
	a.sandboxCalls++
	a.lastSandbox = m
}
func (a *recordingApplier) ApplyApproval(m ApprovalMode) {
	a.approvalCalls++
	a.lastApproval = m
}

func TestRegistry_AutoSeeded(t *testing.T) {
	r := NewRegistry()
	k, ok := r.Lookup(PresetAuto)
	if !ok {
		t.Fatalf("auto preset must be seeded")
	}
	if k != KnobsAuto {
		t.Fatalf("auto preset knobs = %+v, want %+v", k, KnobsAuto)
	}
	if k.Sandbox != SandboxOn || k.Approval != ApprovalAsk {
		t.Fatalf("auto preset must be {on, ask}, got %+v", k)
	}
}

func TestRegistry_Register_ReservedAndEmptyRejected(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{PresetAuto, PresetCustom, ""} {
		if err := r.Register(name, Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}); err == nil {
			t.Fatalf("Register(%q) should be rejected", name)
		}
	}
	// A normal name is accepted and lookable.
	if err := r.Register("paranoid", Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}); err != nil {
		t.Fatalf("Register(paranoid) unexpected error: %v", err)
	}
	if _, ok := r.Lookup("paranoid"); !ok {
		t.Fatalf("paranoid should be lookupable after Register")
	}
}

func TestRegistry_Register_UserPresetResolves(t *testing.T) {
	r := NewRegistry()
	must(t, r.Register("paranoid", Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}))
	if got := r.ResolveName(Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}); got != "paranoid" {
		t.Fatalf("ResolveName({on,never}) = %q, want paranoid", got)
	}
}

func TestResolveName_CustomWhenDiverged(t *testing.T) {
	r := NewRegistry()
	// A tuple matching no registered preset must resolve to the `custom` sentinel.
	diverged := Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}
	if got := r.ResolveName(diverged); got != PresetCustom {
		t.Fatalf("ResolveName(diverged) = %q, want %q", got, PresetCustom)
	}
	// An exact match returns the preset name, never `custom`.
	if got := r.ResolveName(KnobsAuto); got != PresetAuto {
		t.Fatalf("ResolveName(auto tuple) = %q, want %q", got, PresetAuto)
	}
}

func TestResolveName_NeverReturnsCustomKey(t *testing.T) {
	r := NewRegistry()
	// Even if someone registers a tuple identical to the diverged one, the
	// `custom` key is never a match because it is not a real preset.
	if got := r.ResolveName(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}); got == PresetCustom {
		// It IS custom here — that's expected. The point is the loop must never
		// return PresetCustom by matching a stored entry. Confirm the loop body
		// skips the custom key: register a preset with the same tuple and ensure
		// the registered name wins, not "custom".
	}
	must(t, r.Register("permissive", Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}))
	if got := r.ResolveName(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}); got != "permissive" {
		t.Fatalf("ResolveName should return the registered name, got %q", got)
	}
}

func TestManager_InitialStateIsAuto(t *testing.T) {
	m, err := NewManager(NewRegistry(), NewMemoryPersister())
	if err != nil {
		t.Fatal(err)
	}
	if m.Current() != KnobsAuto {
		t.Fatalf("initial current = %+v, want auto %+v", m.Current(), KnobsAuto)
	}
	if m.ActiveName() != PresetAuto {
		t.Fatalf("initial active name = %q, want %q", m.ActiveName(), PresetAuto)
	}
}

func TestManager_ApplyKnobs_OnlyWritesWhenChanged(t *testing.T) {
	p := NewMemoryPersister()
	m, err := NewManager(NewRegistry(), p)
	if err != nil {
		t.Fatal(err)
	}

	// Re-applying the current (auto) tuple must be a no-op: no write.
	changed, err := m.ApplyKnobs(KnobsAuto)
	if err != nil || changed {
		t.Fatalf("re-applying current tuple: changed=%v err=%v (want false, nil)", changed, err)
	}
	if p.Saves() != 0 {
		t.Fatalf("re-applying current tuple must not call Save, got %d saves", p.Saves())
	}

	// A real change writes exactly once.
	changed, err = m.ApplyKnobs(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow})
	if err != nil || !changed {
		t.Fatalf("real change: changed=%v err=%v (want true, nil)", changed, err)
	}
	if p.Saves() != 1 {
		t.Fatalf("real change must call Save once, got %d", p.Saves())
	}
	if m.Current() != (Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}) {
		t.Fatalf("current not updated: %+v", m.Current())
	}

	// Applying the SAME new tuple again must not write again.
	changed, err = m.ApplyKnobs(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow})
	if err != nil || changed {
		t.Fatalf("re-applying same tuple: changed=%v err=%v (want false, nil)", changed, err)
	}
	if p.Saves() != 1 {
		t.Fatalf("re-applying same tuple must not call Save again, got %d", p.Saves())
	}

	// Returning to auto is a real change (back to a different tuple).
	changed, err = m.ApplyKnobs(KnobsAuto)
	if err != nil || !changed {
		t.Fatalf("returning to auto: changed=%v err=%v (want true, nil)", changed, err)
	}
	if p.Saves() != 2 {
		t.Fatalf("returning to auto must call Save (2nd time), got %d", p.Saves())
	}
	if m.ActiveName() != PresetAuto {
		t.Fatalf("after returning to auto, ActiveName = %q, want %q", m.ActiveName(), PresetAuto)
	}
}

func TestManager_SelectPreset(t *testing.T) {
	reg := NewRegistry()
	must(t, reg.Register("paranoid", Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}))
	p := NewMemoryPersister()
	m, err := NewManager(reg, p)
	if err != nil {
		t.Fatal(err)
	}

	// Select a real preset → write + correct current/name.
	changed, err := m.SelectPreset("paranoid")
	if err != nil || !changed {
		t.Fatalf("SelectPreset(paranoid): changed=%v err=%v", changed, err)
	}
	if m.Current() != (Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}) {
		t.Fatalf("current = %+v", m.Current())
	}
	if m.ActiveName() != "paranoid" {
		t.Fatalf("ActiveName = %q, want paranoid", m.ActiveName())
	}
	if p.Saves() != 1 {
		t.Fatalf("SelectPreset should Save once, got %d", p.Saves())
	}

	// Re-selecting the same preset → no write.
	changed, err = m.SelectPreset("paranoid")
	if err != nil || changed {
		t.Fatalf("re-SelectPreset(paranoid): changed=%v err=%v", changed, err)
	}
	if p.Saves() != 1 {
		t.Fatalf("re-selecting same preset must not Save again, got %d", p.Saves())
	}

	// Selecting the reserved `custom` sentinel is an error.
	if _, err := m.SelectPreset(PresetCustom); !errors.Is(err, ErrReservedName) {
		t.Fatalf("SelectPreset(custom) should be ErrReservedName, got %v", err)
	}

	// Selecting an unknown name is an error.
	if _, err := m.SelectPreset("does-not-exist"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("SelectPreset(unknown) should be ErrUnknownPreset, got %v", err)
	}

	// Selecting `auto` returns to the system default.
	changed, err = m.SelectPreset(PresetAuto)
	if err != nil || !changed {
		t.Fatalf("SelectPreset(auto): changed=%v err=%v", changed, err)
	}
	if m.ActiveName() != PresetAuto {
		t.Fatalf("ActiveName = %q, want auto", m.ActiveName())
	}
}

func TestManager_AppliersCalledOnlyOnChange(t *testing.T) {
	mgr, err := NewManager(NewRegistry(), NewMemoryPersister())
	if err != nil {
		t.Fatal(err)
	}
	app := &recordingApplier{}
	mgr.SetAppliers(app, app)

	// No change (auto again) → appliers NOT called.
	if _, err := mgr.ApplyKnobs(KnobsAuto); err != nil {
		t.Fatal(err)
	}
	if app.sandboxCalls != 0 || app.approvalCalls != 0 {
		t.Fatalf("appliers must not fire on a no-op apply (got sb=%d ap=%d)", app.sandboxCalls, app.approvalCalls)
	}

	// Real change → each applier called exactly once with the new tuple.
	if _, err := mgr.ApplyKnobs(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}); err != nil {
		t.Fatal(err)
	}
	if app.sandboxCalls != 1 || app.approvalCalls != 1 {
		t.Fatalf("appliers should fire once on change (got sb=%d ap=%d)", app.sandboxCalls, app.approvalCalls)
	}
	if app.lastSandbox != SandboxOff || app.lastApproval != ApprovalAllow {
		t.Fatalf("appliers got wrong values: sb=%q ap=%q", app.lastSandbox, app.lastApproval)
	}

	// Same tuple again → still no extra calls.
	if _, err := mgr.ApplyKnobs(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}); err != nil {
		t.Fatal(err)
	}
	if app.sandboxCalls != 1 || app.approvalCalls != 1 {
		t.Fatalf("appliers must not fire on repeat (got sb=%d ap=%d)", app.sandboxCalls, app.approvalCalls)
	}
}

func TestManager_PersistErrorDoesNotAdvance(t *testing.T) {
	p := NewMemoryPersister()
	p.SetErrorOnSave(errors.New("disk full"))
	m, err := NewManager(NewRegistry(), p)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := m.ApplyKnobs(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow})
	if err == nil {
		t.Fatalf("expected Save error to surface")
	}
	if changed {
		t.Fatalf("failed apply must report changed=false")
	}
	// In-memory state stays at auto; no successful write counted.
	if m.Current() != KnobsAuto {
		t.Fatalf("current should remain auto after failed Save, got %+v", m.Current())
	}
	if p.Saves() != 0 {
		t.Fatalf("failed Save must not be counted, got %d", p.Saves())
	}
}

func TestManager_LoadsPersistedState(t *testing.T) {
	// A persister pre-seeded with a tuple makes that tuple the initial current,
	// without an extra write.
	seeded := Knobs{Sandbox: SandboxOff, Approval: ApprovalNever}
	m, err := NewManager(NewRegistry(), NewMemoryPersisterWithState(seeded))
	if err != nil {
		t.Fatal(err)
	}
	if m.Current() != seeded {
		t.Fatalf("current = %+v, want seeded %+v", m.Current(), seeded)
	}
}

// TestManager_Counterexample_WriteGuardIsReal proves the "only write when changed"
// guard is actually doing its job: if ApplyKnobs were changed to always call Save,
// this test would fail. We assert a no-op apply produces ZERO saves; the
// smallest regression (dropping the `m.current == k` short-circuit) breaks it.
func TestManager_Counterexample_WriteGuardIsReal(t *testing.T) {
	p := NewMemoryPersister()
	m, err := NewManager(NewRegistry(), p)
	if err != nil {
		t.Fatal(err)
	}
	// Current is already auto. Re-apply auto.
	if _, err := m.ApplyKnobs(KnobsAuto); err != nil {
		t.Fatal(err)
	}
	if p.Saves() != 0 {
		t.Fatalf("COUNTEREXAMPLE: a no-op apply must not write, but Save called %d time(s) — the change guard is broken", p.Saves())
	}
}

// TestResolveName_Counterexample_ReversedPredicate proves ResolveName really
// distinguishes matched vs diverged tuples. If the predicate were inverted
// (returning custom for a match and a name for a diverged tuple) this fails.
func TestResolveName_Counterexample_ReversedPredicate(t *testing.T) {
	r := NewRegistry()
	must(t, r.Register("paranoid", Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}))
	// Match → must be the named preset, NOT custom.
	if got := r.ResolveName(Knobs{Sandbox: SandboxOn, Approval: ApprovalNever}); got != "paranoid" {
		t.Fatalf("COUNTEREXAMPLE: matched tuple resolved to %q, expected 'paranoid'", got)
	}
	// Diverged → must be custom, NOT a named preset.
	if got := r.ResolveName(Knobs{Sandbox: SandboxOff, Approval: ApprovalAllow}); got != PresetCustom {
		t.Fatalf("COUNTEREXAMPLE: diverged tuple resolved to %q, expected 'custom'", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
