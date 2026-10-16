// Package preset implements permission presets (G10).
//
// A preset is a binary tuple {sandbox, approval}. Two names are reserved:
//
//   - `auto`   — the system-managed default preset ({sandbox:on, approval:ask}).
//     It is always registered and is selectable.
//   - `custom` — a reserved sentinel returned by ResolveName when the active
//     tuple matches no named preset. It is never registered and never
//     selectable; it only ever means "the user diverged from every preset".
//
// The manager persists the active tuple but ONLY writes when a knob actually
// changes ("only write when knobs truly changed"): re-applying the current
// tuple is a no-op and never touches the persister or the runtime appliers.
//
// Design contract (溯源):
//   - Leaf package: depends only on the standard library. It never imports
//     agent/types/approval/safety/tools, so it introduces no import cycle and is
//     trivially unit-testable. The agent applies the two knobs to the G4 sandbox
//     and G8 approval subsystems through the SandboxApplier / ApprovalApplier
//     seams defined here.
//   - The two knobs are plain comparable string types; Knobs is a value struct so
//     the manager can detect "no real change" with a single ==.
package preset

import (
	"errors"
	"fmt"
	"sync"
)

// SandboxMode is the sandbox knob of a permission preset.
type SandboxMode string

const (
	// SandboxOn enables the process sandbox (rlimit prelude + deny rules, G4).
	SandboxOn SandboxMode = "on"
	// SandboxOff disables the process sandbox.
	SandboxOff SandboxMode = "off"
)

// ApprovalMode is the approval knob of a permission preset.
type ApprovalMode string

const (
	// ApprovalAsk prompts the user for each risky tool (the fail-closed default).
	ApprovalAsk ApprovalMode = "ask"
	// ApprovalAllow grants every request without prompting (permissive).
	ApprovalAllow ApprovalMode = "allow"
	// ApprovalNever rejects every request (deny-all).
	ApprovalNever ApprovalMode = "never"
)

// Knobs is the binary tuple {sandbox, approval} a permission preset carries.
// It is a comparable value type so the manager can detect "no real change".
type Knobs struct {
	Sandbox  SandboxMode
	Approval ApprovalMode
}

// KnobsAuto is the tuple bound to the reserved `auto` name.
var KnobsAuto = Knobs{Sandbox: SandboxOn, Approval: ApprovalAsk}

// Reserved preset names. User-defined presets may never use these.
const (
	PresetAuto   = "auto"   // system-managed default; selectable, has concrete knobs
	PresetCustom = "custom" // reserved sentinel for a diverged tuple; never selectable
)

// ErrReservedName is returned when trying to register/select a reserved name.
var ErrReservedName = errors.New("preset: reserved name cannot be used")

// ErrUnknownPreset is returned when selecting a name that is not registered.
var ErrUnknownPreset = errors.New("preset: unknown preset name")

// Registry holds the named presets. `auto` is always present; `custom` is
// reserved and never stored.
type Registry struct {
	mu    sync.RWMutex
	named map[string]Knobs
}

// NewRegistry builds a registry seeded with the system-managed `auto` preset.
func NewRegistry() *Registry {
	r := &Registry{named: make(map[string]Knobs)}
	r.named[PresetAuto] = KnobsAuto
	return r
}

// Register adds (or replaces) a named preset. It refuses the reserved names
// `auto` and `custom`, and empty names.
func (r *Registry) Register(name string, k Knobs) error {
	if name == PresetAuto || name == PresetCustom {
		return fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	if name == "" {
		return fmt.Errorf("preset: empty name cannot be registered")
	}
	r.mu.Lock()
	r.named[name] = k
	r.mu.Unlock()
	return nil
}

// Lookup returns the knobs of a named preset. `auto` resolves to KnobsAuto.
func (r *Registry) Lookup(name string) (Knobs, bool) {
	r.mu.RLock()
	k, ok := r.named[name]
	r.mu.RUnlock()
	return k, ok
}

// ResolveName maps a tuple back to the name of the matching named preset. If the
// tuple matches no registered preset it returns the reserved sentinel `custom`
// (the caller has diverged from every named preset). The `custom` key itself is
// never a match, because it is not a real preset.
func (r *Registry) ResolveName(k Knobs) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, k2 := range r.named {
		if name == PresetCustom {
			continue
		}
		if k2 == k {
			return name
		}
	}
	return PresetCustom
}
