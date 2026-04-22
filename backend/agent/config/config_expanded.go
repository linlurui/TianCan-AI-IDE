package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Priority Merge Engine ───────────────────────────────────────

// MergeStrategy defines how conflicting config values are resolved.
type MergeStrategy string

const (
	MergeOverride MergeStrategy = "override" // later value wins
	MergeAppend   MergeStrategy = "append"   // concatenate values
	MergePrepend  MergeStrategy = "prepend"  // earlier value first
	MergeDefer    MergeStrategy = "defer"    // keep first non-empty
)

// MergeRule defines how a specific config key is merged.
type MergeRule struct {
	Key      string        `json:"key"`
	Strategy MergeStrategy `json:"strategy"`
}

// PriorityMerger merges config files by priority with configurable strategies.
type PriorityMerger struct {
	mu       sync.RWMutex
	rules    map[string]MergeStrategy // key → strategy
	defaults MergeStrategy
}

// NewPriorityMerger creates a merger with default override strategy.
func NewPriorityMerger() *PriorityMerger {
	return &PriorityMerger{
		rules:    make(map[string]MergeStrategy),
		defaults: MergeOverride,
	}
}

// SetRule sets a merge strategy for a specific key.
func (pm *PriorityMerger) SetRule(key string, strategy MergeStrategy) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.rules[key] = strategy
}

// Merge merges config files in priority order (later = higher priority).
func (pm *PriorityMerger) Merge(configs []ConfigFile) MergedConfig {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	mc := MergedConfig{
		Sections: make([]MergedSection, 0),
		RawMap:   make(map[string]string),
	}

	// Build merged sections
	for _, cfg := range configs {
		if cfg.Content == "" {
			continue
		}
		mc.Sections = append(mc.Sections, MergedSection{
			Source:  cfg.Path,
			Type:    cfg.Type,
			Content: cfg.Content,
		})
	}

	// Parse key-value pairs from each config and merge
	for _, cfg := range configs {
		pairs := parseKeyValuePairs(cfg.Content)
		for k, v := range pairs {
			strategy := pm.defaults
			if s, ok := pm.rules[k]; ok {
				strategy = s
			}
			existing, hasExisting := mc.RawMap[k]
			switch strategy {
			case MergeOverride:
				mc.RawMap[k] = v
			case MergeAppend:
				if hasExisting {
					mc.RawMap[k] = existing + "\n" + v
				} else {
					mc.RawMap[k] = v
				}
			case MergePrepend:
				if hasExisting {
					mc.RawMap[k] = v + "\n" + existing
				} else {
					mc.RawMap[k] = v
				}
			case MergeDefer:
				if !hasExisting || existing == "" {
					mc.RawMap[k] = v
				}
			}
		}
	}

	return mc
}

// MergedConfig holds the result of merging multiple config files.
type MergedConfig struct {
	Sections []MergedSection     `json:"sections"`
	RawMap   map[string]string   `json:"rawMap"`
}

// MergedSection represents a section of merged config.
type MergedSection struct {
	Source  string           `json:"source"`
	Type    types.MemoryType `json:"type"`
	Content string           `json:"content"`
}

// BuildPrompt assembles the merged config into a system prompt section.
func (mc *MergedConfig) BuildPrompt() string {
	var sb strings.Builder
	for _, s := range mc.Sections {
		sb.WriteString(fmt.Sprintf("## %s: %s\n\n%s\n\n", s.Type, s.Source, s.Content))
	}
	return sb.String()
}

// Get retrieves a merged key-value pair.
func (mc *MergedConfig) Get(key string) (string, bool) {
	v, ok := mc.RawMap[key]
	return v, ok
}

// ── Rule Engine ─────────────────────────────────────────────────

// ConfigRule represents a conditional config rule.
type ConfigRule struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Condition   string   `json:"condition"` // glob pattern for file path, or "always"
	Priority    int      `json:"priority"`
	Content     string   `json:"content"`
	Source      string   `json:"source"`
	Enabled     bool     `json:"enabled"`
}

// RuleEngine evaluates conditional config rules.
type RuleEngine struct {
	mu    sync.RWMutex
	rules []ConfigRule
}

// NewRuleEngine creates a rule engine.
func NewRuleEngine() *RuleEngine {
	return &RuleEngine{}
}

// AddRule adds a config rule.
func (re *RuleEngine) AddRule(rule ConfigRule) {
	re.mu.Lock()
	defer re.mu.Unlock()
	re.rules = append(re.rules, rule)
	sort.SliceStable(re.rules, func(i, j int) bool {
		return re.rules[i].Priority > re.rules[j].Priority
	})
}

// RemoveRule removes a rule by ID.
func (re *RuleEngine) RemoveRule(id string) {
	re.mu.Lock()
	defer re.mu.Unlock()
	filtered := re.rules[:0]
	for _, r := range re.rules {
		if r.ID != id {
			filtered = append(filtered, r)
		}
	}
	re.rules = filtered
}

// Evaluate returns all rules that match the given context.
func (re *RuleEngine) Evaluate(filePath string) []ConfigRule {
	re.mu.RLock()
	defer re.mu.RUnlock()
	var matched []ConfigRule
	for _, r := range re.rules {
		if !r.Enabled {
			continue
		}
		if r.Condition == "always" || r.Condition == "" {
			matched = append(matched, r)
			continue
		}
		// Glob match against file path
		if ok, _ := filepath.Match(r.Condition, filepath.Base(filePath)); ok {
			matched = append(matched, r)
			continue
		}
		// Directory prefix match
		if strings.HasPrefix(filePath, r.Condition) {
			matched = append(matched, r)
		}
	}
	return matched
}

// BuildRulePrompt assembles matched rules into a prompt section.
func (re *RuleEngine) BuildRulePrompt(filePath string) string {
	rules := re.Evaluate(filePath)
	if len(rules) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Active Rules\n\n")
	for _, r := range rules {
		sb.WriteString(fmt.Sprintf("## %s (priority %d)\n%s\n\n", r.Name, r.Priority, r.Content))
	}
	return sb.String()
}

// LoadRulesFromDir loads rule files from a directory.
func (re *RuleEngine) LoadRulesFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // directory doesn't exist is not an error
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			var rule ConfigRule
			if json.Unmarshal(data, &rule) == nil {
				rule.Source = path
				re.AddRule(rule)
			}
		} else {
			content := string(data)
			fm := parseFrontmatter(content)
			content = stripFrontmatter(content)
			re.AddRule(ConfigRule{
				ID:          entry.Name(),
				Name:        fm.Description,
				Condition:   strings.Join(fm.Globs, ","),
				Priority:    50,
				Content:     content,
				Source:      path,
				Enabled:     true,
			})
		}
	}
	return nil
}

// ── Config Resolver ─────────────────────────────────────────────

// ConfigResolver provides a unified interface for config resolution.
type ConfigResolver struct {
	merger *PriorityMerger
	engine *RuleEngine
	configs []ConfigFile
}

// NewConfigResolver creates a config resolver.
func NewConfigResolver() *ConfigResolver {
	return &ConfigResolver{
		merger: NewPriorityMerger(),
		engine: NewRuleEngine(),
	}
}

// LoadConfigs loads all configs from the filesystem.
func (cr *ConfigResolver) LoadConfigs(cwd, homeDir string) error {
	configs, err := LoadAllConfigs(cwd, homeDir)
	if err != nil {
		return err
	}
	cr.configs = configs
	return nil
}

// Resolve returns the merged config with rules applied for a given file path.
func (cr *ConfigResolver) Resolve(filePath string) string {
	merged := cr.merger.Merge(cr.configs)
	var sb strings.Builder
	sb.WriteString(merged.BuildPrompt())
	sb.WriteString(cr.engine.BuildRulePrompt(filePath))
	return sb.String()
}

// GetConfigs returns the loaded config files.
func (cr *ConfigResolver) GetConfigs() []ConfigFile {
	return cr.configs
}

// ── internal helpers ────────────────────────────────────────────

// parseKeyValuePairs extracts key: value pairs from markdown content.
func parseKeyValuePairs(content string) map[string]string {
	pairs := make(map[string]string)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key == "" || val == "" {
			continue
		}
		pairs[key] = val
	}
	return pairs
}
