package feature

import (
	"os"
	"sync"
)

// FeatureFlag represents a feature flag with its current state.
type FeatureFlag struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"` // "env", "default", "runtime"
}

// FlagRegistry manages feature flags for dynamic import control.
// Mirrors Claude Code's feature flag system (bun:bundle feature gates).
type FlagRegistry struct {
	flags map[string]*FeatureFlag
	mu    sync.RWMutex
}

// Global flag registry
var globalRegistry = NewFlagRegistry()

// NewFlagRegistry creates a new flag registry with defaults.
func NewFlagRegistry() *FlagRegistry {
	r := &FlagRegistry{
		flags: make(map[string]*FeatureFlag),
	}
	r.loadDefaults()
	r.loadFromEnv()
	return r
}

// loadDefaults sets the default feature flag values.
func (r *FlagRegistry) loadDefaults() {
	defaults := map[string]bool{
		// Core features
		"TRANSCRIPT_CLASSIFIER":  true,
		"FORK_SUBAGENT":          false,
		"BASH_CLASSIFIER":        true,
		"SESSION_MEMORY":         true,
		"AUTO_COMPACT":           true,
		"COORDINATOR_MODE":       false,
		"WORKTREE_ISOLATION":     true,
		"PARALLEL_EXECUTION":     true,
		"SKILL_SEARCH":           false,
		"POWERSHELL_AUTO_MODE":   false,
		"PROMPT_CACHE_BREAK_DETECTION": true,
		"KAIROS":                 false,
		"PROACTIVE":              false,
	}

	for name, enabled := range defaults {
		r.flags[name] = &FeatureFlag{
			Name:    name,
			Enabled: enabled,
			Source:  "default",
		}
	}
}

// loadFromEnv overrides defaults from environment variables.
// Format: TIANCAN_FEATURE_<FLAG_NAME>=1/0
func (r *FlagRegistry) loadFromEnv() {
	for name, flag := range r.flags {
		envKey := "TIANCAN_FEATURE_" + name
		if val := os.Getenv(envKey); val != "" {
			flag.Enabled = val == "1" || val == "true"
			flag.Source = "env"
		}
	}
}

// IsEnabled checks if a feature flag is enabled.
func (r *FlagRegistry) IsEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if flag, ok := r.flags[name]; ok {
		return flag.Enabled
	}
	return false
}

// SetFlag enables or disables a feature flag at runtime.
func (r *FlagRegistry) SetFlag(name string, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if flag, ok := r.flags[name]; ok {
		flag.Enabled = enabled
		flag.Source = "runtime"
	} else {
		r.flags[name] = &FeatureFlag{
			Name:    name,
			Enabled: enabled,
			Source:  "runtime",
		}
	}
}

// GetAllFlags returns all feature flags.
func (r *FlagRegistry) GetAllFlags() []FeatureFlag {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var flags []FeatureFlag
	for _, f := range r.flags {
		flags = append(flags, *f)
	}
	return flags
}

// --- Global convenience functions ---

// IsEnabled checks a feature flag in the global registry.
func IsEnabled(name string) bool {
	return globalRegistry.IsEnabled(name)
}

// SetFlag sets a feature flag in the global registry.
func SetFlag(name string, enabled bool) {
	globalRegistry.SetFlag(name, enabled)
}

// GetRegistry returns the global flag registry.
func GetRegistry() *FlagRegistry {
	return globalRegistry
}

// --- Plugin/Skill dynamic loading ---

// SkillSpec defines a dynamically loadable skill.
type SkillSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"filePath"`
	Enabled     bool   `json:"enabled"`
}

// SkillRegistry manages dynamically loaded skills.
type SkillRegistry struct {
	skills map[string]*SkillSpec
	mu     sync.RWMutex
}

var globalSkillRegistry = NewSkillRegistry()

// NewSkillRegistry creates a new skill registry.
func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{
		skills: make(map[string]*SkillSpec),
	}
}

// Register adds a skill to the registry.
func (sr *SkillRegistry) Register(spec *SkillSpec) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.skills[spec.Name] = spec
}

// Get returns a skill by name.
func (sr *SkillRegistry) Get(name string) (*SkillSpec, bool) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	s, ok := sr.skills[name]
	return s, ok
}

// List returns all registered skills.
func (sr *SkillRegistry) List() []*SkillSpec {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	var list []*SkillSpec
	for _, s := range sr.skills {
		list = append(list, s)
	}
	return list
}

// ListEnabled returns only enabled skills.
func (sr *SkillRegistry) ListEnabled() []*SkillSpec {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	var list []*SkillSpec
	for _, s := range sr.skills {
		if s.Enabled {
			list = append(list, s)
		}
	}
	return list
}

// Unregister removes a skill.
func (sr *SkillRegistry) Unregister(name string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.skills, name)
}

// GetSkillRegistry returns the global skill registry.
func GetSkillRegistry() *SkillRegistry {
	return globalSkillRegistry
}
