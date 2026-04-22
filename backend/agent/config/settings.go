package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/safety"
)

// Settings represents the user/project settings file.
// Mirrors Claude Code's settings.json schema.
type Settings struct {
	// Permission rules
	Permissions PermissionRules `json:"permissions,omitempty"`

	// Auto mode configuration
	AutoMode *AutoModeSettings `json:"autoMode,omitempty"`

	// MCP servers
	MCPServers map[string]interface{} `json:"mcpServers,omitempty"`

	// Hooks configuration
	Hooks HooksConfig `json:"hooks,omitempty"`

	// Model preferences
	Model string `json:"model,omitempty"`

	// Feature flags
	Features map[string]bool `json:"features,omitempty"`

	// Environment variables for agent
	Env map[string]string `json:"env,omitempty"`

	// Allowed commands (bash patterns)
	AllowedCommands []string `json:"allowedCommands,omitempty"`

	// Denied commands (bash patterns)
	DeniedCommands []string `json:"deniedCommands,omitempty"`
}

// PermissionRules defines permission rules from settings.
// Mirrors Claude Code's permission rules config.
type PermissionRules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// AutoModeSettings configures the YOLO classifier.
// Mirrors Claude Code's settings.autoMode.
type AutoModeSettings struct {
	Allow       []string `json:"allow,omitempty"`
	SoftDeny    []string `json:"softDeny,omitempty"`
	Environment []string `json:"environment,omitempty"`
}

// HooksConfig defines hook configurations.
// Mirrors Claude Code's hooks settings.
type HooksConfig struct {
	PreToolUse  map[string][]HookConfig `json:"PreToolUse,omitempty"`
	PostToolUse map[string][]HookConfig `json:"PostToolUse,omitempty"`
}

// HookConfig defines a single hook.
type HookConfig struct {
	Command   string `json:"command"`
	Timeout   int    `json:"timeout,omitempty"`   // ms
	OnFailure string `json:"onFailure,omitempty"` // "block", "continue", "ask"
}

// SettingsManager manages settings from multiple sources.
// Mirrors Claude Code's settings hierarchy: defaults < global < project < local.
type SettingsManager struct {
	settings  Settings
	configDir string
	mu        sync.RWMutex
	loadPaths []string
}

// NewSettingsManager creates a new settings manager.
func NewSettingsManager(homeDir string) *SettingsManager {
	sm := &SettingsManager{
		configDir: filepath.Join(homeDir, DotDir),
	}
	sm.loadDefaults()
	return sm
}

// loadDefaults sets default settings values.
func (sm *SettingsManager) loadDefaults() {
	sm.settings = Settings{
		Features: map[string]bool{
			"SESSION_MEMORY": true,
			"AUTO_COMPACT":   true,
		},
	}
}

// LoadAll loads settings from all sources in priority order.
// Mirrors Claude Code's settings loading hierarchy.
func (sm *SettingsManager) LoadAll(projectRoot string) error {
	// 1. Global settings (~/.tiancan/settings.json)
	globalPath := filepath.Join(sm.configDir, "settings.json")
	if err := sm.loadFromFile(globalPath); err == nil {
		sm.loadPaths = append(sm.loadPaths, globalPath)
	}

	// 2. Project settings (.tiancan/settings.json)
	if projectRoot != "" {
		projectPath := filepath.Join(projectRoot, DotDir, "settings.json")
		if err := sm.loadFromFile(projectPath); err == nil {
			sm.loadPaths = append(sm.loadPaths, projectPath)
		}
	}

	// 3. Local settings (.tiancan/settings.local.json) — not committed to VCS
	if projectRoot != "" {
		localPath := filepath.Join(projectRoot, DotDir, "settings.local.json")
		if err := sm.loadFromFile(localPath); err == nil {
			sm.loadPaths = append(sm.loadPaths, localPath)
		}
	}

	// 4. Environment overrides
	sm.loadFromEnv()

	return nil
}

// loadFromFile loads settings from a JSON file (merges with existing).
func (sm *SettingsManager) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var fileSettings Settings
	if err := json.Unmarshal(data, &fileSettings); err != nil {
		return fmt.Errorf("parse settings %s: %w", path, err)
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Merge: file values override existing
	sm.mergeSettings(fileSettings)
	return nil
}

// mergeSettings merges file settings into current settings.
func (sm *SettingsManager) mergeSettings(fileSettings Settings) {
	// Permissions: append
	sm.settings.Permissions.Allow = append(sm.settings.Permissions.Allow, fileSettings.Permissions.Allow...)
	sm.settings.Permissions.Deny = append(sm.settings.Permissions.Deny, fileSettings.Permissions.Deny...)
	sm.settings.Permissions.Ask = append(sm.settings.Permissions.Ask, fileSettings.Permissions.Ask...)

	// AutoMode: override if set
	if fileSettings.AutoMode != nil {
		sm.settings.AutoMode = fileSettings.AutoMode
	}

	// MCPServers: merge
	if sm.settings.MCPServers == nil {
		sm.settings.MCPServers = make(map[string]interface{})
	}
	for k, v := range fileSettings.MCPServers {
		sm.settings.MCPServers[k] = v
	}

	// Hooks: merge
	if fileSettings.Hooks.PreToolUse != nil {
		if sm.settings.Hooks.PreToolUse == nil {
			sm.settings.Hooks.PreToolUse = make(map[string][]HookConfig)
		}
		for k, v := range fileSettings.Hooks.PreToolUse {
			sm.settings.Hooks.PreToolUse[k] = v
		}
	}
	if fileSettings.Hooks.PostToolUse != nil {
		if sm.settings.Hooks.PostToolUse == nil {
			sm.settings.Hooks.PostToolUse = make(map[string][]HookConfig)
		}
		for k, v := range fileSettings.Hooks.PostToolUse {
			sm.settings.Hooks.PostToolUse[k] = v
		}
	}

	// Model: override if set
	if fileSettings.Model != "" {
		sm.settings.Model = fileSettings.Model
	}

	// Features: merge
	if sm.settings.Features == nil {
		sm.settings.Features = make(map[string]bool)
	}
	for k, v := range fileSettings.Features {
		sm.settings.Features[k] = v
	}

	// Env: merge
	if sm.settings.Env == nil {
		sm.settings.Env = make(map[string]string)
	}
	for k, v := range fileSettings.Env {
		sm.settings.Env[k] = v
	}

	// Commands: append
	sm.settings.AllowedCommands = append(sm.settings.AllowedCommands, fileSettings.AllowedCommands...)
	sm.settings.DeniedCommands = append(sm.settings.DeniedCommands, fileSettings.DeniedCommands...)
}

// loadFromEnv loads settings overrides from environment variables.
func (sm *SettingsManager) loadFromEnv() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// TIANCAN_MODEL overrides model
	if m := os.Getenv("TIANCAN_MODEL"); m != "" {
		sm.settings.Model = m
	}

	// TIANCAN_AUTO_MODE=1 enables auto mode
	if os.Getenv("TIANCAN_AUTO_MODE") == "1" {
		if sm.settings.AutoMode == nil {
			sm.settings.AutoMode = &AutoModeSettings{}
		}
	}

	// TIANCAN_BYPASS_PERMISSIONS=1
	if os.Getenv("TIANCAN_BYPASS_PERMISSIONS") == "1" {
		sm.settings.Permissions.Allow = append(sm.settings.Permissions.Allow, "*")
	}
}

// GetSettings returns a copy of the current settings.
func (sm *SettingsManager) GetSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings
}

// GetPermissionRules converts settings permission rules to safety rules.
// Mirrors Claude Code's permission rules config integration.
func (sm *SettingsManager) GetPermissionRules() (allow, deny, ask []safety.Rule) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, rule := range sm.settings.Permissions.Allow {
		allow = append(allow, safety.Rule{
			ToolName:     parseRuleToolName(rule),
			RuleBehavior: "allow",
			Pattern:      parseRulePattern(rule),
			Description:  fmt.Sprintf("Allowed by settings: %s", rule),
		})
	}
	for _, rule := range sm.settings.Permissions.Deny {
		deny = append(deny, safety.Rule{
			ToolName:     parseRuleToolName(rule),
			RuleBehavior: "deny",
			Pattern:      parseRulePattern(rule),
			Description:  fmt.Sprintf("Denied by settings: %s", rule),
		})
	}
	for _, rule := range sm.settings.Permissions.Ask {
		ask = append(ask, safety.Rule{
			ToolName:     parseRuleToolName(rule),
			RuleBehavior: "ask",
			Pattern:      parseRulePattern(rule),
			Description:  fmt.Sprintf("Ask by settings: %s", rule),
		})
	}
	return
}

// parseRuleToolName extracts the tool name from a permission rule string.
// Format: "ToolName" or "ToolName(pattern)" — mirrors Claude Code's permissionRuleValueFromString.
func parseRuleToolName(rule string) string {
	if idx := strings.Index(rule, "("); idx >= 0 {
		return rule[:idx]
	}
	if idx := strings.Index(rule, ":"); idx >= 0 {
		return rule[:idx]
	}
	return rule
}

// parseRulePattern extracts the pattern from a permission rule string.
func parseRulePattern(rule string) string {
	if idx := strings.Index(rule, "("); idx >= 0 {
		end := strings.LastIndex(rule, ")")
		if end > idx {
			return rule[idx+1 : end]
		}
	}
	if idx := strings.Index(rule, ":"); idx >= 0 {
		return rule[idx+1:]
	}
	return ""
}

// GetAutoModeRules returns auto mode rules for the YOLO classifier.
func (sm *SettingsManager) GetAutoModeRules() *AutoModeSettings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.AutoMode
}

// GetModel returns the configured model.
func (sm *SettingsManager) GetModel() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.Model
}

// GetFeatureFlags returns feature flag settings.
func (sm *SettingsManager) GetFeatureFlags() map[string]bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	cp := make(map[string]bool)
	for k, v := range sm.settings.Features {
		cp[k] = v
	}
	return cp
}

// GetLoadPaths returns the settings file paths that were loaded.
func (sm *SettingsManager) GetLoadPaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	cp := make([]string, len(sm.loadPaths))
	copy(cp, sm.loadPaths)
	return cp
}

// SaveToFile saves current settings to a JSON file.
func (sm *SettingsManager) SaveToFile(path string) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	data, err := json.MarshalIndent(sm.settings, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
