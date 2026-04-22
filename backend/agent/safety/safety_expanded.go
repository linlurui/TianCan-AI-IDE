package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

type PermissionMode string

const (
	ModeDefault PermissionMode = "default"
	ModeAuto    PermissionMode = "auto"
	ModePlan    PermissionMode = "plan"
	ModeBypass  PermissionMode = "bypass"
)

type PermissionRule struct {
	ID          string         `json:"id"`
	ToolName    string         `json:"toolName"`
	Action      string         `json:"action"`
	PathPattern string         `json:"pathPattern,omitempty"`
	ArgPattern  string         `json:"argPattern,omitempty"`
	CompiledArg *regexp.Regexp `json:"-"`
	Priority    int            `json:"priority"`
	Description string         `json:"description"`
	Source      string         `json:"source"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
}

type PermissionLogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	ToolName  string                 `json:"toolName"`
	Decision  types.PermissionDecision `json:"decision"`
	Reason    string                 `json:"reason"`
}

type ConfirmationRequest struct {
	ToolName string                 `json:"toolName"`
	Args     map[string]interface{} `json:"args"`
	Reason   string                 `json:"reason"`
	Response chan bool              `json:"-"`
}

type SandboxConfig struct {
	Enabled         bool     `json:"enabled"`
	BlockedCommands []string `json:"blockedCommands"`
	MaxExecTimeSec  int      `json:"maxExecTimeSec"`
	MaxOutputBytes  int      `json:"maxOutputBytes"`
	NetworkAccess   bool     `json:"networkAccess"`
}

type PathWhitelist struct {
	mu    sync.RWMutex
	paths map[string]bool
}

func (pw *PathWhitelist) Add(path string) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.paths[path] = true
}

func (pw *PathWhitelist) IsAllowed(path, rootPath string) bool {
	pw.mu.RLock()
	defer pw.mu.RUnlock()
	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(rootPath, path)
	}
	if rootPath != "" && strings.HasPrefix(absPath, rootPath) {
		for _, d := range []string{".git", ".tiancan", ".ssh", ".gnupg"} {
			if strings.Contains(absPath, "/"+d+"/") || strings.HasSuffix(absPath, "/"+d) {
				return false
			}
		}
		return true
	}
	for allowed := range pw.paths {
		if strings.HasPrefix(absPath, allowed) {
			return true
		}
	}
	return false
}

// PermissionManager provides fine-grained permission management.
type PermissionManager struct {
	mu         sync.RWMutex
	mode       PermissionMode
	rootPath   string
	rules      []PermissionRule
	allowCache map[string]bool
	denyCache  map[string]bool
	sessionLog []PermissionLogEntry
	whitelist  PathWhitelist
	sandbox    SandboxConfig
}

func NewPermissionManager(rootPath string, mode PermissionMode) *PermissionManager {
	pm := &PermissionManager{
		mode:       mode,
		rootPath:   rootPath,
		rules:      DefaultPermissionRules(),
		allowCache: make(map[string]bool),
		denyCache:  make(map[string]bool),
		whitelist:  PathWhitelist{paths: make(map[string]bool)},
		sandbox:    DefaultSandboxConfig(),
	}
	pm.loadRulesFromEnv()
	return pm
}

func (pm *PermissionManager) Check(tool types.Tool, args map[string]interface{}) types.PermissionResult {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	toolName := tool.Name()

	if pm.mode == ModeBypass {
		return types.PermissionResult{Decision: types.DecisionAllow}
	}
	if pm.mode == ModePlan && !tool.IsReadOnly() {
		return types.PermissionResult{Decision: types.DecisionDeny, Reason: "Plan mode: write not allowed"}
	}

	// Check cache
	cacheKey := toolName + ":" + extractPathStr(args)
	if allowed, ok := pm.allowCache[cacheKey]; ok && allowed {
		return types.PermissionResult{Decision: types.DecisionAllow}
	}
	if denied, ok := pm.denyCache[cacheKey]; ok && denied {
		return types.PermissionResult{Decision: types.DecisionDeny, Reason: "Previously denied"}
	}

	// Evaluate rules by priority
	for _, rule := range pm.sortedRules() {
		if rule.ExpiresAt != nil && time.Now().After(*rule.ExpiresAt) {
			continue
		}
		if !pm.ruleMatches(rule, toolName, args) {
			continue
		}
		switch rule.Action {
		case "deny":
			pm.logDecision(toolName, types.DecisionDeny, rule.Description)
			return types.PermissionResult{Decision: types.DecisionDeny, Reason: rule.Description}
		case "ask":
			return types.PermissionResult{Decision: types.DecisionAsk, Reason: rule.Description}
		case "allow":
			pm.allowCache[cacheKey] = true
			pm.logDecision(toolName, types.DecisionAllow, rule.Description)
			return types.PermissionResult{Decision: types.DecisionAllow}
		}
	}

	// Sandbox check
	if pm.sandbox.Enabled && toolName == "bash" {
		if result := pm.checkSandbox(args); result.Decision != types.DecisionAllow {
			return result
		}
	}

	// Path whitelist for writes
	if !tool.IsReadOnly() {
		if path, ok := extractPath(args); ok && !pm.whitelist.IsAllowed(path, pm.rootPath) {
			return types.PermissionResult{Decision: types.DecisionAsk, Reason: "Path requires confirmation"}
		}
	}

	// Auto mode classifier
	if pm.mode == ModeAuto {
		result := pm.classifyAction(toolName, args)
		if result.ShouldBlock {
			pm.denyCache[cacheKey] = true
			return types.PermissionResult{Decision: types.DecisionDeny, Reason: result.Reason}
		}
		pm.allowCache[cacheKey] = true
		return types.PermissionResult{Decision: types.DecisionAllow}
	}

	if tool.IsReadOnly() {
		return types.PermissionResult{Decision: types.DecisionAllow}
	}
	return types.PermissionResult{Decision: types.DecisionAsk, Reason: fmt.Sprintf("Tool %s requires permission", toolName)}
}

func (pm *PermissionManager) AddRule(rule PermissionRule) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if rule.ArgPattern != "" {
		rule.CompiledArg = regexp.MustCompile(rule.ArgPattern)
	}
	pm.rules = append(pm.rules, rule)
	pm.allowCache = make(map[string]bool)
	pm.denyCache = make(map[string]bool)
}

func (pm *PermissionManager) SetMode(mode PermissionMode) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.mode = mode
	pm.allowCache = make(map[string]bool)
	pm.denyCache = make(map[string]bool)
}

func (pm *PermissionManager) GetMode() PermissionMode {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.mode
}

func (pm *PermissionManager) WhitelistPath(path string) { pm.whitelist.Add(path) }

func (pm *PermissionManager) GetLog() []PermissionLogEntry {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.sessionLog
}

// internal

func (pm *PermissionManager) sortedRules() []PermissionRule {
	sorted := make([]PermissionRule, len(pm.rules))
	copy(sorted, pm.rules)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Priority > sorted[j-1].Priority; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}

func (pm *PermissionManager) ruleMatches(rule PermissionRule, toolName string, args map[string]interface{}) bool {
	if rule.ToolName != "*" && rule.ToolName != toolName {
		return false
	}
	if rule.ArgPattern != "" {
		if rule.CompiledArg == nil {
			rule.CompiledArg = regexp.MustCompile(rule.ArgPattern)
		}
		for _, v := range args {
			if s, ok := v.(string); ok && rule.CompiledArg.MatchString(s) {
				return true
			}
		}
		return false
	}
	return true
}

func (pm *PermissionManager) checkSandbox(args map[string]interface{}) types.PermissionResult {
	cmd, _ := args["command"].(string)
	lower := strings.ToLower(cmd)
	for _, blocked := range pm.sandbox.BlockedCommands {
		if strings.Contains(lower, strings.ToLower(blocked)) {
			return types.PermissionResult{Decision: types.DecisionDeny, Reason: "Sandbox: blocked command"}
		}
	}
	if !pm.sandbox.NetworkAccess {
		for _, nc := range []string{"curl", "wget", "nc", "ssh", "scp"} {
			if strings.Contains(lower, nc) {
				return types.PermissionResult{Decision: types.DecisionDeny, Reason: "Sandbox: network denied"}
			}
		}
	}
	return types.PermissionResult{Decision: types.DecisionAllow}
}

func (pm *PermissionManager) classifyAction(toolName string, args map[string]interface{}) types.YOLOClassifierResult {
	if cmd, ok := args["command"].(string); ok {
		dangerous := []string{"rm -rf /", "mkfs", "dd if=", "curl.*|.*sh", "git push --force", "npm publish"}
		lower := strings.ToLower(cmd)
		for _, p := range dangerous {
			if strings.Contains(lower, p) {
				return types.YOLOClassifierResult{ShouldBlock: true, Reason: "Dangerous pattern: " + p}
			}
		}
	}
	return types.YOLOClassifierResult{ShouldBlock: false}
}

func (pm *PermissionManager) logDecision(toolName string, decision types.PermissionDecision, reason string) {
	pm.sessionLog = append(pm.sessionLog, PermissionLogEntry{
		Timestamp: time.Now(), ToolName: toolName, Decision: decision, Reason: reason,
	})
}

func (pm *PermissionManager) loadRulesFromEnv() {
	if denyTools := os.Getenv("TIANCAN_DENY_TOOLS"); denyTools != "" {
		for _, t := range strings.Split(denyTools, ",") {
			pm.rules = append(pm.rules, PermissionRule{
				ID: "env-deny-" + t, ToolName: strings.TrimSpace(t), Action: "deny",
				Priority: 70, Description: "Denied by env", Source: "env",
			})
		}
	}
	if os.Getenv("TIANCAN_AUTO_MODE") == "1" {
		pm.mode = ModeAuto
	}
}

func DefaultPermissionRules() []PermissionRule {
	return []PermissionRule{
		{ID: "deny-rm-rf", ToolName: "bash", Action: "deny", ArgPattern: `rm\s+-rf\s+/(|home)`, Priority: 100, Description: "Deny destructive rm", Source: "default"},
		{ID: "deny-mkfs", ToolName: "bash", Action: "deny", ArgPattern: `mkfs`, Priority: 100, Description: "Deny filesystem format", Source: "default"},
		{ID: "deny-curl-pipe", ToolName: "bash", Action: "deny", ArgPattern: `curl.*\|\s*(ba)?sh`, Priority: 90, Description: "Deny curl pipe shell", Source: "default"},
		{ID: "deny-force-push", ToolName: "git_push", Action: "deny", ArgPattern: `--force`, Priority: 80, Description: "Deny force push", Source: "default"},
		{ID: "allow-read", ToolName: "read_file", Action: "allow", Priority: 10, Description: "Allow reads", Source: "default"},
		{ID: "allow-grep", ToolName: "grep", Action: "allow", Priority: 10, Description: "Allow grep", Source: "default"},
		{ID: "allow-glob", ToolName: "glob", Action: "allow", Priority: 10, Description: "Allow glob", Source: "default"},
		{ID: "allow-list", ToolName: "list_directory", Action: "allow", Priority: 10, Description: "Allow listing", Source: "default"},
	}
}

func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		BlockedCommands: []string{"rm -rf /", "mkfs", "dd if= of=/dev/", "format"},
		MaxExecTimeSec:  120, MaxOutputBytes: 1 << 20, NetworkAccess: true,
	}
}

func extractPath(args map[string]interface{}) (string, bool) {
	for _, key := range []string{"path", "file_path", "file", "dir"} {
		if s, ok := args[key].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func extractPathStr(args map[string]interface{}) string {
	p, ok := extractPath(args)
	if !ok {
		return ""
	}
	return p
}

// SaveRules persists permission rules to disk.
func (pm *PermissionManager) SaveRules(path string) error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	data, _ := json.MarshalIndent(pm.rules, "", "  ")
	return os.WriteFile(path, data, 0644)
}

// LoadRules loads permission rules from disk.
func (pm *PermissionManager) LoadRules(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rules []PermissionRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return err
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.rules = append(pm.rules, rules...)
	for i := range pm.rules {
		if pm.rules[i].ArgPattern != "" {
			pm.rules[i].CompiledArg = regexp.MustCompile(pm.rules[i].ArgPattern)
		}
	}
	return nil
}
