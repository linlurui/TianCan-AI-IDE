package safety

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// SafetyLevel represents the defense depth level.
type SafetyLevel int

const (
	LevelRuleBased      SafetyLevel = iota // 0: Static rule checks
	LevelYOLOClassifier                    // 1: AI-based auto-mode classifier
	LevelSafetyCheck                       // 2: Path safety (.git, .tiancan, shell configs)
	LevelUserConfirm                       // 3: Explicit user confirmation
)

// SafetyConfig holds the safety system configuration.
type SafetyConfig struct {
	DenyRules  []Rule `json:"denyRules"`
	AskRules   []Rule `json:"askRules"`
	AllowRules []Rule `json:"allowRules"`
	AutoMode   bool   `json:"autoMode"`
	BypassMode bool   `json:"bypassMode"`
}

// Rule represents a permission rule.
type Rule struct {
	ToolName      string         `json:"toolName"`
	RuleBehavior  string         `json:"ruleBehavior"` // "deny", "ask", "allow"
	Pattern       string         `json:"pattern,omitempty"`
	CompiledRegex *regexp.Regexp `json:"-"`
	Description   string         `json:"description"`
}

// DenialRecord tracks a denied tool invocation for fallback analysis.
// Mirrors Claude Code's denial tracking for the YOLO classifier fallback.
type DenialRecord struct {
	ToolName      string                 `json:"toolName"`
	Args          map[string]interface{} `json:"args"`
	Reason        string                 `json:"reason"`
	Level         SafetyLevel            `json:"level"`
	Timestamp     time.Time              `json:"timestamp"`
	WasOverridden bool                   `json:"wasOverridden"`
}

// SafetySystem provides multi-level defense for tool execution.
// Mirrors Claude Code's permissions.ts + toolHooks.ts architecture.
// All classification decisions are AI-driven via the YOLO classifier —
// no hardcoded regex or pattern lists are used for security decisions.
type SafetySystem struct {
	config SafetyConfig

	// Configurable protected paths and safe tools — loaded from settings/env, not hardcoded.
	protectedPaths []string
	readOnlyTools  map[string]bool

	// AI-driven classifier — the single source of truth for allow/block decisions.
	classifier *YOLOClassifier

	// Tool registry for dynamic tool name resolution (replaces hardcoded lists).
	toolRegistry ToolRegistry

	// Hook registry (mirrors Claude Code's hook system)
	hooks   []HookEntry
	hooksMu sync.RWMutex

	// Denial tracking for fallback (mirrors Claude Code's denial tracking)
	denials    []DenialRecord
	denialsMu  sync.Mutex
	maxDenials int
}

// ToolRegistry provides dynamic tool name lookup — replaces hardcoded tool lists.
type ToolRegistry interface {
	IsRegistered(toolName string) bool
	IsReadOnly(toolName string) bool
	AllNames() []string
}

// NewSafetySystem creates a new safety system.
// Protected paths and tool lists are loaded from environment/config, not hardcoded.
// Classification is AI-driven via the YOLO classifier.
func NewSafetySystem() *SafetySystem {
	s := &SafetySystem{
		protectedPaths: loadProtectedPathsFromEnv(),
		readOnlyTools:  loadReadOnlyToolsFromEnv(),
		maxDenials:     100,
	}
	s.loadRulesFromEnv()
	return s
}

// SetClassifier injects the AI-driven YOLO classifier.
func (s *SafetySystem) SetClassifier(classifier *YOLOClassifier) {
	s.classifier = classifier
}

// SetToolRegistry injects the dynamic tool registry.
func (s *SafetySystem) SetToolRegistry(registry ToolRegistry) {
	s.toolRegistry = registry
}

// loadProtectedPathsFromEnv loads protected path patterns from environment.
// TIANCAN_PROTECTED_PATHS=.git,.ssh,.gnupg,...
// Falls back to sensible defaults only if no env is set.
func loadProtectedPathsFromEnv() []string {
	if v := os.Getenv("TIANCAN_PROTECTED_PATHS"); v != "" {
		var paths []string
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				paths = append(paths, p)
			}
		}
		return paths
	}
	// Defaults — only used when no env/config override exists
	return []string{".git", ".tiancan", ".ssh", ".gnupg"}
}

// loadReadOnlyToolsFromEnv loads read-only tool names from environment.
// TIANCAN_READONLY_TOOLS=read_file,grep,glob,...
func loadReadOnlyToolsFromEnv() map[string]bool {
	m := make(map[string]bool)
	if v := os.Getenv("TIANCAN_READONLY_TOOLS"); v != "" {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				m[t] = true
			}
		}
		return m
	}
	// Defaults — only used when no env/config override exists
	return map[string]bool{
		"read_file": true, "grep": true, "glob": true,
		"list_directory": true, "todo_write": true, "ask_user": true,
	}
}

// classifyActionAI delegates all classification to the AI-driven YOLO classifier.
// No hardcoded regex or pattern lists — the AI model is the single source of truth.
// If the classifier is unavailable, the decision falls back to user confirmation (safe default).
func (s *SafetySystem) classifyActionAI(toolName string, args map[string]interface{}) types.YOLOClassifierResult {
	if s.classifier == nil {
		// No AI classifier available — fall back to asking the user (safest default)
		return types.YOLOClassifierResult{
			ShouldBlock: true,
			Reason:      "AI classifier unavailable — requires user confirmation for safety",
		}
	}

	// Build the action transcript entry for the classifier
	action := TranscriptEntry{
		Role: "assistant",
		Content: []TranscriptBlock{
			{
				Type:  "tool_use",
				Name:  toolName,
				Input: args,
			},
		},
	}

	// Use recent denials as context for the classifier
	recentDenials := s.GetRecentDenials(5)
	var denialMessages []types.Message
	for _, d := range recentDenials {
		denialMessages = append(denialMessages, types.Message{
			Role:      types.RoleTool,
			Content:   fmt.Sprintf("Previously denied: %s — %s", d.ToolName, d.Reason),
			ToolName:  d.ToolName,
			Timestamp: d.Timestamp,
		})
	}

	result := s.classifier.ClassifyAction(context.Background(), denialMessages, action)

	// Record the classification for telemetry and fallback
	if result.ShouldBlock {
		s.recordDenial(toolName, args, result.Reason, LevelYOLOClassifier)
	}

	return types.YOLOClassifierResult{
		ShouldBlock: result.ShouldBlock,
		Reason:      result.Reason,
	}
}

// CheckPermission runs the full permission pipeline.
// Returns the decision and which level made it.
func (s *SafetySystem) CheckPermission(tool types.Tool, args map[string]interface{}, rootPath string) (types.PermissionResult, SafetyLevel) {
	toolName := tool.Name()

	// Level 0: Rule-based checks
	// 0a. Check deny rules
	if rule := s.getDenyRule(toolName); rule != nil {
		return types.PermissionResult{
			Decision: types.DecisionDeny,
			Reason:   fmt.Sprintf("Permission to use %s has been denied by rule: %s", toolName, rule.Description),
			DecisionReason: &types.DecisionReason{
				Type:         "rule",
				RuleBehavior: "deny",
				Description:  rule.Description,
			},
		}, LevelRuleBased
	}

	// 0b. Check ask rules
	if rule := s.getAskRule(toolName); rule != nil {
		return types.PermissionResult{
			Decision: types.DecisionAsk,
			Reason:   fmt.Sprintf("Tool %s requires permission (ask rule: %s)", toolName, rule.Description),
			DecisionReason: &types.DecisionReason{
				Type:         "rule",
				RuleBehavior: "ask",
				Description:  rule.Description,
			},
		}, LevelRuleBased
	}

	// 0c. Check tool-specific content rules (e.g. bash subcommand patterns)
	if rule := s.getContentRule(toolName, args); rule != nil {
		return types.PermissionResult{
			Decision: types.DecisionDeny,
			Reason:   fmt.Sprintf("Content denied by rule: %s", rule.Description),
			DecisionReason: &types.DecisionReason{
				Type:         "rule",
				RuleBehavior: rule.RuleBehavior,
				Description:  rule.Description,
			},
		}, LevelRuleBased
	}

	// Level 2: Safety checks for protected paths (configurable, not hardcoded)
	if !tool.IsReadOnly() {
		for _, pathKey := range []string{"path", "file_path", "directory", "destination"} {
			if path, ok := args[pathKey].(string); ok && path != "" {
				if s.isProtectedPath(path, rootPath) {
					return types.PermissionResult{
						Decision: types.DecisionAsk,
						Reason:   fmt.Sprintf("Path %s is in a protected directory", path),
						DecisionReason: &types.DecisionReason{
							Type:        "safetyCheck",
							Description: "Protected path requires confirmation",
						},
					}, LevelSafetyCheck
				}
			}
		}
	}

	// Bypass mode: allow everything that passed rule checks
	if s.config.BypassMode {
		return types.PermissionResult{
			Decision: types.DecisionAllow,
			DecisionReason: &types.DecisionReason{
				Type: "mode",
			},
		}, LevelRuleBased
	}

	// Level 1: YOLO classifier (auto mode) — AI-driven, no hardcoded patterns
	if s.config.AutoMode {
		isReadOnly := tool.IsReadOnly() || s.readOnlyTools[toolName]
		if !isReadOnly {
			result := s.classifyActionAI(toolName, args)
			if result.ShouldBlock {
				return types.PermissionResult{
					Decision: types.DecisionDeny,
					Reason:   result.Reason,
					DecisionReason: &types.DecisionReason{
						Type:        "classifier",
						Description: result.Reason,
					},
				}, LevelYOLOClassifier
			}
		}
	}

	// Check allow rules
	if rule := s.getAllowRule(toolName); rule != nil {
		return types.PermissionResult{
			Decision: types.DecisionAllow,
			DecisionReason: &types.DecisionReason{
				Type:         "rule",
				RuleBehavior: "allow",
				Description:  rule.Description,
			},
		}, LevelRuleBased
	}

	// Read-only tools auto-allow
	if s.readOnlyTools[toolName] || tool.IsReadOnly() {
		return types.PermissionResult{
			Decision: types.DecisionAllow,
		}, LevelRuleBased
	}

	// Level 3: Need user confirmation
	return types.PermissionResult{
		Decision: types.DecisionAsk,
		Reason:   fmt.Sprintf("Tool %s requires permission", toolName),
	}, LevelUserConfirm
}

// --- Denial tracking (mirrors Claude Code's denial tracking + fallback) ---

// recordDenial adds a denial record for fallback analysis.
func (s *SafetySystem) recordDenial(toolName string, args map[string]interface{}, reason string, level SafetyLevel) {
	s.denialsMu.Lock()
	defer s.denialsMu.Unlock()

	s.denials = append(s.denials, DenialRecord{
		ToolName:  toolName,
		Args:      args,
		Reason:    reason,
		Level:     level,
		Timestamp: time.Now(),
	})

	// Trim to max size
	if len(s.denials) > s.maxDenials {
		s.denials = s.denials[len(s.denials)-s.maxDenials:]
	}
}

// GetRecentDenials returns the N most recent denial records.
func (s *SafetySystem) GetRecentDenials(n int) []DenialRecord {
	s.denialsMu.Lock()
	defer s.denialsMu.Unlock()

	if n > len(s.denials) {
		n = len(s.denials)
	}
	if n <= 0 {
		return nil
	}

	result := make([]DenialRecord, n)
	copy(result, s.denials[len(s.denials)-n:])
	return result
}

// GetDenialCount returns the total number of recorded denials.
func (s *SafetySystem) GetDenialCount() int {
	s.denialsMu.Lock()
	defer s.denialsMu.Unlock()
	return len(s.denials)
}

// ClearDenials removes all denial records.
func (s *SafetySystem) ClearDenials() {
	s.denialsMu.Lock()
	defer s.denialsMu.Unlock()
	s.denials = nil
}

// isProtectedPath checks if a path is in a protected directory.
// Protected paths are loaded from config/env, not hardcoded.
func (s *SafetySystem) isProtectedPath(path string, rootPath string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	// Never allow writes outside the project root
	if rootPath != "" && !strings.HasPrefix(absPath, rootPath) {
		return true
	}

	// Check configurable protected subdirectories
	for _, dp := range s.protectedPaths {
		if strings.Contains(absPath, "/"+dp+"/") || strings.HasSuffix(absPath, "/"+dp) {
			return true
		}
	}
	return false
}

// Rule accessors

func (s *SafetySystem) getDenyRule(toolName string) *Rule {
	for i := range s.config.DenyRules {
		if s.config.DenyRules[i].ToolName == toolName || s.config.DenyRules[i].ToolName == "*" {
			return &s.config.DenyRules[i]
		}
	}
	return nil
}

func (s *SafetySystem) getAskRule(toolName string) *Rule {
	for i := range s.config.AskRules {
		if s.config.AskRules[i].ToolName == toolName || s.config.AskRules[i].ToolName == "*" {
			return &s.config.AskRules[i]
		}
	}
	return nil
}

func (s *SafetySystem) getAllowRule(toolName string) *Rule {
	for i := range s.config.AllowRules {
		if s.config.AllowRules[i].ToolName == toolName || s.config.AllowRules[i].ToolName == "*" {
			return &s.config.AllowRules[i]
		}
	}
	return nil
}

func (s *SafetySystem) getContentRule(toolName string, args map[string]interface{}) *Rule {
	// Check content-specific rules (e.g. bash subcommand patterns)
	for i := range s.config.DenyRules {
		rule := &s.config.DenyRules[i]
		if rule.Pattern == "" || rule.ToolName != toolName {
			continue
		}
		if rule.CompiledRegex == nil {
			rule.CompiledRegex = regexp.MustCompile(rule.Pattern)
		}
		// Check command content against pattern
		if cmd, ok := args["command"].(string); ok {
			if rule.CompiledRegex.MatchString(cmd) {
				return rule
			}
		}
	}
	return nil
}

// loadRulesFromEnv loads permission rules from environment variables.
func (s *SafetySystem) loadRulesFromEnv() {
	// TIANCAN_DENY_TOOLS=tool1,tool2,tool3
	if denyTools := os.Getenv("TIANCAN_DENY_TOOLS"); denyTools != "" {
		for _, t := range strings.Split(denyTools, ",") {
			s.config.DenyRules = append(s.config.DenyRules, Rule{
				ToolName:     strings.TrimSpace(t),
				RuleBehavior: "deny",
				Description:  fmt.Sprintf("Denied by environment: %s", t),
			})
		}
	}

	// TIANCAN_ASK_TOOLS=tool1,tool2
	if askTools := os.Getenv("TIANCAN_ASK_TOOLS"); askTools != "" {
		for _, t := range strings.Split(askTools, ",") {
			s.config.AskRules = append(s.config.AskRules, Rule{
				ToolName:     strings.TrimSpace(t),
				RuleBehavior: "ask",
				Description:  fmt.Sprintf("Ask rule from environment: %s", t),
			})
		}
	}

	// TIANCAN_AUTO_MODE=1 enables YOLO classifier
	s.config.AutoMode = os.Getenv("TIANCAN_AUTO_MODE") == "1"
	s.config.BypassMode = os.Getenv("TIANCAN_BYPASS_PERMISSIONS") == "1"
}

// SetConfig updates the safety configuration.
func (s *SafetySystem) SetConfig(cfg SafetyConfig) {
	s.config = cfg
}

// IsAutoMode returns whether auto mode (YOLO classifier) is active.
func (s *SafetySystem) IsAutoMode() bool {
	return s.config.AutoMode
}

// GetConfig returns the current safety configuration.
func (s *SafetySystem) GetConfig() SafetyConfig {
	return s.config
}
