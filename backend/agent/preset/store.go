package preset

import (
	"fmt"
	"sync"
)

// Persister is the durability seam. Implementations decide where the active tuple
// lives (memory, file, settings store). The manager only ever calls Save when the
// tuple actually changed.
type Persister interface {
	// Load returns the persisted tuple, whether anything was persisted, and an
	// error. A nil error with existed=false means "no prior state".
	Load() (k Knobs, existed bool, err error)
	// Save persists the active tuple.
	Save(k Knobs) error
}

// SandboxApplier applies the sandbox knob to the runtime sandbox subsystem.
// Implemented by the agent (G4 wiring); nil is allowed and treated as a no-op.
type SandboxApplier interface {
	ApplySandbox(mode SandboxMode)
}

// ApprovalApplier applies the approval knob to the approval subsystem.
// Implemented by the agent (G8 wiring); nil is allowed and treated as a no-op.
type ApprovalApplier interface {
	ApplyApproval(mode ApprovalMode)
}

// Manager owns the active tuple, its persistence, and notification of the runtime
// appliers — but only when the tuple actually changes.
type Manager struct {
	reg          *Registry
	persist      Persister
	current      Knobs
	sandboxApply SandboxApplier
	approvalApply ApprovalApplier
	mu           sync.RWMutex
}

// NewManager builds a manager. It loads any persisted tuple; on a load error or
// when nothing was persisted, it falls back to the `auto` default in-memory
// WITHOUT persisting it — we must not write before the user actually changes a
// knob ("only write when knobs truly changed").
func NewManager(reg *Registry, p Persister) (*Manager, error) {
	m := &Manager{reg: reg, persist: p}
	k, existed, err := p.Load()
	if err != nil {
		// Durability problem on read: fall back to the auto default in-memory.
		// A Save error would surface later; we must not crash construction here.
		m.current = KnobsAuto
		return m, nil
	}
	if existed {
		m.current = k
	} else {
		m.current = KnobsAuto
	}
	return m, nil
}

// SetAppliers wires the runtime appliers (agent subsystems). Safe to leave nil.
func (m *Manager) SetAppliers(s SandboxApplier, a ApprovalApplier) {
	m.mu.Lock()
	m.sandboxApply = s
	m.approvalApply = a
	m.mu.Unlock()
}

// Current returns the active tuple.
func (m *Manager) Current() Knobs {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// ActiveName returns the effective preset name: the matching named preset, or
// `custom` if the tuple diverged from every named preset.
func (m *Manager) ActiveName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.reg.ResolveName(m.current)
}

// ApplyKnobs sets the active tuple. It ONLY persists (and applies to runtime
// subsystems) when the tuple actually differs from the current one — the core
// "only write when knobs truly changed" rule. Returns whether a write happened.
func (m *Manager) ApplyKnobs(k Knobs) (bool, error) {
	m.mu.Lock()
	if m.current == k {
		m.mu.Unlock()
		return false, nil
	}
	if err := m.persist.Save(k); err != nil {
		// Save failed: the tuple is NOT advanced and appliers are NOT invoked,
		// so the in-memory state stays consistent with durable state.
		m.mu.Unlock()
		return false, err
	}
	m.current = k
	sa, aa := m.sandboxApply, m.approvalApply
	m.mu.Unlock()

	if sa != nil {
		sa.ApplySandbox(k.Sandbox)
	}
	if aa != nil {
		aa.ApplyApproval(k.Approval)
	}
	return true, nil
}

// SelectPreset selects a named preset by name. Selecting the reserved `custom`
// sentinel or an unknown name is an error. Returns whether a write happened.
func (m *Manager) SelectPreset(name string) (bool, error) {
	if name == PresetCustom {
		return false, fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	k, ok := m.reg.Lookup(name)
	if !ok {
		return false, fmt.Errorf("%w: %q", ErrUnknownPreset, name)
	}
	return m.ApplyKnobs(k)
}
