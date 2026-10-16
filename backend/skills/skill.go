package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// Skill represents a parsed .skill.yaml file.
type Skill struct {
	Name          string      `yaml:"name"`
	Description   string      `yaml:"description"`
	Body          string      `yaml:"body"`       // Freeform instruction text injected into the conversation when triggered (G5 pre-step).
	Version       string      `yaml:"version"`
	Triggers      []string    `yaml:"triggers"`
	Steps         []SkillStep `yaml:"steps"`
	DependsOn     []string    `yaml:"depends_on"` // Names of skills this skill depends on
	FilePath      string      `yaml:"-"`
	Validated     bool        `yaml:"-"` // Whether the skill has passed validation
	ValidationMsg string      `yaml:"-"` // Validation result message
}

// RenderPreStep renders the skill's guidance as instruction text for pre-step
// injection (G5). The text is intended for a user-role message — NOT the system
// prompt — so the static system prompt stays cacheable and per-skill matches do
// not bust the KV cache.
//
// Precedence: explicit Body wins; otherwise derive guidance from Description +
// Steps, so legacy skills without a body still inject meaningful instructions.
func (s *Skill) RenderPreStep() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill `%s` is now available. ", s.Name)
	if s.Body != "" {
		b.WriteString(s.Body)
		return b.String()
	}
	if s.Description != "" {
		b.WriteString(s.Description)
		b.WriteString("\n")
	}
	if len(s.Steps) > 0 {
		b.WriteString("Follow these steps:\n")
		for i, st := range s.Steps {
			fmt.Fprintf(&b, "  %d. call tool %q with arguments %v\n", i+1, st.Tool, st.Args)
		}
	}
	return b.String()
}

// SkillStep is a single step within a skill.
type SkillStep struct {
	Tool string                 `yaml:"tool"`
	Args map[string]interface{} `yaml:"args"`
}

// Manager discovers, loads, and invokes skills.
type Manager struct {
	mu       sync.RWMutex
	skills   map[string]*Skill // name -> skill
	rootPath string
}

// NewManager creates a skill manager for the given project root.
func NewManager(rootPath string) *Manager {
	return &Manager{
		skills:   make(map[string]*Skill),
		rootPath: rootPath,
	}
}

// LoadAll scans the project for .skill.yaml files and loads them.
func (m *Manager) LoadAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dirs := []string{
		filepath.Join(envconfig.ProjectDataDir(m.rootPath), "skills"),
		filepath.Join(m.rootPath, "skills"),
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // directory may not exist
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".skill.yaml") && !strings.HasSuffix(name, ".skill.yml") {
				continue
			}
			fp := filepath.Join(dir, name)
			skill, err := parseFile(fp)
			if err != nil {
				continue // skip malformed files
			}
			if skill.Name == "" {
				skill.Name = strings.TrimSuffix(name, ".skill.yaml")
				skill.Name = strings.TrimSuffix(skill.Name, ".skill.yml")
			}
			skill.FilePath = fp
			m.skills[skill.Name] = skill
		}
	}
	return nil
}

// Get returns a skill by name.
func (m *Manager) Get(name string) (*Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	return s, ok
}

// List returns all loaded skills.
func (m *Manager) List() []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Skill, 0, len(m.skills))
	for _, s := range m.skills {
		out = append(out, s)
	}
	return out
}

// MatchTrigger returns skills whose triggers match the given input.
func (m *Manager) MatchTrigger(input string) []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lower := strings.ToLower(input)
	var matched []*Skill
	for _, s := range m.skills {
		for _, t := range s.Triggers {
			if strings.Contains(lower, strings.ToLower(t)) {
				matched = append(matched, s)
				break
			}
		}
	}
	return matched
}

// SkillInfo is a lightweight summary for serialization.
type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Triggers    []string `json:"triggers"`
	StepCount   int      `json:"stepCount"`
	FilePath    string   `json:"filePath"`
}

// ListInfo returns lightweight skill info for API responses.
func (m *Manager) ListInfo() []SkillInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SkillInfo, 0, len(m.skills))
	for _, s := range m.skills {
		out = append(out, SkillInfo{
			Name:        s.Name,
			Description: s.Description,
			Version:     s.Version,
			Triggers:    s.Triggers,
			StepCount:   len(s.Steps),
			FilePath:    s.FilePath,
		})
	}
	return out
}

// parseFile reads and parses a .skill.yaml file.
func parseFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill file: %w", err)
	}
	var skill Skill
	if err := yaml.Unmarshal(data, &skill); err != nil {
		return nil, fmt.Errorf("parse skill yaml: %w", err)
	}
	if skill.Name == "" {
		return nil, fmt.Errorf("skill file %s missing name field", path)
	}
	return &skill, nil
}

// Validate checks a skill for correctness and safety.
// It verifies: name present, steps have valid tools, no circular dependencies.
func (m *Manager) Validate(name string) (bool, string) {
	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()
	if !ok {
		return false, fmt.Sprintf("skill %q not found", name)
	}

	// Check name
	if skill.Name == "" {
		return false, "skill missing name field"
	}

	// Check steps
	if len(skill.Steps) == 0 {
		return false, "skill has no steps"
	}
	for i, step := range skill.Steps {
		if step.Tool == "" {
			return false, fmt.Sprintf("step %d missing tool name", i+1)
		}
	}

	// Check for circular dependencies
	if err := m.resolveDependencies(name, nil); err != nil {
		return false, err.Error()
	}

	skill.Validated = true
	skill.ValidationMsg = "valid"
	return true, "valid"
}

// ValidateAll validates all loaded skills and returns results.
func (m *Manager) ValidateAll() map[string]string {
	m.mu.RLock()
	names := make([]string, 0, len(m.skills))
	for n := range m.skills {
		names = append(names, n)
	}
	m.mu.RUnlock()

	results := make(map[string]string)
	for _, n := range names {
		ok, msg := m.Validate(n)
		if ok {
			results[n] = "✓ " + msg
		} else {
			results[n] = "✗ " + msg
		}
	}
	return results
}

// resolveDependencies checks that all dependencies exist and there are no cycles.
func (m *Manager) resolveDependencies(name string, visited []string) error {
	// Cycle detection
	for _, v := range visited {
		if v == name {
			return fmt.Errorf("circular dependency detected: %s", strings.Join(append(visited, name), " → "))
		}
	}

	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("dependency %q not found", name)
	}

	visited = append(visited, name)
	for _, dep := range skill.DependsOn {
		if err := m.resolveDependencies(dep, visited); err != nil {
			return err
		}
	}
	return nil
}

// GetExecutionOrder returns skills in dependency-safe execution order (topological sort).
func (m *Manager) GetExecutionOrder(name string) ([]*Skill, error) {
	var order []*Skill
	seen := map[string]bool{}
	if err := m.topoSort(name, seen, &order); err != nil {
		return nil, err
	}
	return order, nil
}

func (m *Manager) topoSort(name string, seen map[string]bool, order *[]*Skill) error {
	if seen[name] {
		return nil
	}
	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}
	seen[name] = true
	for _, dep := range skill.DependsOn {
		if err := m.topoSort(dep, seen, order); err != nil {
			return err
		}
	}
	*order = append(*order, skill)
	return nil
}

// ─── Local Skill Management ────────────────────────────────────────────

// InstallLocal installs a skill from local YAML content.
// Validates the content before writing, and reloads the skill list.
func (m *Manager) InstallLocal(yamlContent string) error {
	var skill Skill
	if err := yaml.Unmarshal([]byte(yamlContent), &skill); err != nil {
		return fmt.Errorf("invalid skill YAML: %w", err)
	}
	if skill.Name == "" {
		return fmt.Errorf("skill missing name field")
	}
	if len(skill.Steps) == 0 {
		return fmt.Errorf("skill has no steps")
	}

	skillDir := filepath.Join(envconfig.ProjectDataDir(m.rootPath), "skills")
	os.MkdirAll(skillDir, 0755)
	destPath := filepath.Join(skillDir, skill.Name+".skill.yaml")
	if err := os.WriteFile(destPath, []byte(yamlContent), 0644); err != nil {
		return fmt.Errorf("write skill file: %w", err)
	}

	return m.LoadAll()
}

// InstallFromFile installs a skill from a local file path.
func (m *Manager) InstallFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read skill file: %w", err)
	}
	return m.InstallLocal(string(data))
}

// Delete removes a skill by name.
func (m *Manager) Delete(name string) error {
	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}

	if skill.FilePath == "" {
		return fmt.Errorf("skill %q has no file path (cannot delete)", name)
	}

	if err := os.Remove(skill.FilePath); err != nil {
		return fmt.Errorf("delete skill file: %w", err)
	}

	m.mu.Lock()
	delete(m.skills, name)
	m.mu.Unlock()
	return nil
}

// SkillDetail returns full detail for a skill (including validation status).
func (m *Manager) SkillDetail(name string) (*Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	return s, ok
}

// ─── Skill Market (Import/Export/Share) ─────────────────────────────────

// MarketEntry represents a skill available in the community market.
type MarketEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Category    string   `json:"category"`    // e.g. "testing", "refactor", "debug", "deploy"
	Tags        []string `json:"tags"`
	DownloadCount int    `json:"downloadCount"`
	Rating      float64  `json:"rating"`      // 0-5 stars
	RatingCount int      `json:"ratingCount"`
	UpdatedAt   string   `json:"updatedAt"`
	YAMLContent string   `json:"yamlContent"` // The actual skill YAML
}

// MarketConfig holds market settings.
type MarketConfig struct {
	MarketURL   string `json:"marketURL"`   // Community market API endpoint
	CacheDir    string `json:"cacheDir"`    // Local cache directory
	CacheTTLMin int    `json:"cacheTTLMin"` // Cache TTL in minutes
}

// DefaultMarketConfig returns default market configuration.
func DefaultMarketConfig() MarketConfig {
	return MarketConfig{
		MarketURL:   "https://tiancan.market.example.com/api/v1",
		CacheDir:    ".tiancan/market-cache",
		CacheTTLMin: 30,
	}
}

// ExportSkill exports a skill as a portable JSON string for sharing.
func (m *Manager) ExportSkill(name string) (string, error) {
	m.mu.RLock()
	skill, ok := m.skills[name]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}

	// Read raw YAML from file
	if skill.FilePath == "" {
		return "", fmt.Errorf("skill %q has no file path", name)
	}
	data, err := os.ReadFile(skill.FilePath)
	if err != nil {
		return "", fmt.Errorf("read skill file: %w", err)
	}

	entry := MarketEntry{
		Name:        skill.Name,
		Description: skill.Description,
		Version:     skill.Version,
		Author:      "local-user",
		Category:    InferCategory(skill),
		Tags:        skill.Triggers,
		YAMLContent: string(data),
	}

	jsonData, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal skill: %w", err)
	}
	return string(jsonData), nil
}

// ExportAll exports all local skills as a JSON array.
func (m *Manager) ExportAll() (string, error) {
	entries := make([]MarketEntry, 0, len(m.skills))
	for _, skill := range m.skills {
		if skill.FilePath == "" {
			continue
		}
		data, err := os.ReadFile(skill.FilePath)
		if err != nil {
			continue
		}
		entries = append(entries, MarketEntry{
			Name:        skill.Name,
			Description: skill.Description,
			Version:     skill.Version,
			Author:      "local-user",
			Category:    InferCategory(skill),
			Tags:        skill.Triggers,
			YAMLContent: string(data),
		})
	}
	jsonData, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal skills: %w", err)
	}
	return string(jsonData), nil
}

// ImportFromMarket imports a skill from a market entry JSON string.
func (m *Manager) ImportFromMarket(jsonStr string) error {
	var entry MarketEntry
	if err := json.Unmarshal([]byte(jsonStr), &entry); err != nil {
		return fmt.Errorf("invalid market entry JSON: %w", err)
	}
	if entry.Name == "" || entry.YAMLContent == "" {
		return fmt.Errorf("market entry missing required fields (name or yamlContent)")
	}
	return m.InstallLocal(entry.YAMLContent)
}

// DownloadFromURL downloads and installs a skill from a URL.
func (m *Manager) DownloadFromURL(url string) error {
	// Use HTTP GET to fetch the skill JSON
	resp, err := httpGet(url)
	if err != nil {
		return fmt.Errorf("download skill: %w", err)
	}
	return m.ImportFromMarket(resp)
}

// RateSkill records a rating for a skill (local rating storage).
func (m *Manager) RateSkill(name string, rating int) error {
	if rating < 1 || rating > 5 {
		return fmt.Errorf("rating must be between 1 and 5")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store rating in a local JSON file
	ratingsPath := filepath.Join(envconfig.ProjectDataDir(m.rootPath), "skill-ratings.json")
	var ratings map[string]int
	data, _ := os.ReadFile(ratingsPath)
	if data != nil {
		json.Unmarshal(data, &ratings)
	} else {
		ratings = make(map[string]int)
	}
	ratings[name] = rating
	newData, _ := json.MarshalIndent(ratings, "", "  ")
	os.WriteFile(ratingsPath, newData, 0644)
	return nil
}

// GetSkillRating returns the local rating for a skill.
func (m *Manager) GetSkillRating(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ratingsPath := filepath.Join(envconfig.ProjectDataDir(m.rootPath), "skill-ratings.json")
	data, err := os.ReadFile(ratingsPath)
	if err != nil {
		return 0
	}
	var ratings map[string]int
	if err := json.Unmarshal(data, &ratings); err != nil {
		return 0
	}
	return ratings[name]
}

// SearchMarket searches the community market for skills.
// Falls back to local skill search if marketURL is not configured.
func (m *Manager) SearchMarket(query, category string) ([]MarketEntry, error) {
	// If no market URL configured, search local skills
	marketURL := envconfig.EnvString("TIANCAN_MARKET_URL")
	if marketURL == "" {
		return m.searchLocal(query, category), nil
	}

	// Try to query the community market API
	url := fmt.Sprintf("%s/skills/search?q=%s&category=%s", marketURL, query, category)
	resp, err := httpGet(url)
	if err != nil {
		// Fallback to local search
		return m.searchLocal(query, category), nil
	}

	var results struct {
		Skills []MarketEntry `json:"skills"`
	}
	if err := json.Unmarshal([]byte(resp), &results); err != nil {
		return m.searchLocal(query, category), nil
	}
	return results.Skills, nil
}

// searchLocal searches local skills by name, description, or tags.
func (m *Manager) searchLocal(query, category string) []MarketEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []MarketEntry
	q := strings.ToLower(query)

	for _, skill := range m.skills {
		// Category filter
		if category != "" && category != "all" {
			cat := InferCategory(skill)
			if cat != category {
				continue
			}
		}

		// Text search
		if q != "" {
			match := strings.Contains(strings.ToLower(skill.Name), q) ||
				strings.Contains(strings.ToLower(skill.Description), q)
			for _, t := range skill.Triggers {
				if strings.Contains(strings.ToLower(t), q) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		data, _ := os.ReadFile(skill.FilePath)
		entry := MarketEntry{
			Name:        skill.Name,
			Description: skill.Description,
			Version:     skill.Version,
			Author:      "local",
			Category:    InferCategory(skill),
			Tags:        skill.Triggers,
			YAMLContent: string(data),
		}
		results = append(results, entry)
	}
	return results
}

// InferCategory guesses the skill category from its name, triggers, and steps.
func InferCategory(skill *Skill) string {
	lower := strings.ToLower(skill.Name)
	for _, t := range skill.Triggers {
		lower += " " + strings.ToLower(t)
	}
	for _, step := range skill.Steps {
		lower += " " + strings.ToLower(step.Tool)
	}

	switch {
	case strings.Contains(lower, "test"):
		return "testing"
	case strings.Contains(lower, "deploy") || strings.Contains(lower, "ssh") || strings.Contains(lower, "scp"):
		return "deploy"
	case strings.Contains(lower, "debug") || strings.Contains(lower, "trace"):
		return "debug"
	case strings.Contains(lower, "refactor") || strings.Contains(lower, "clean") || strings.Contains(lower, "optimize"):
		return "refactor"
	case strings.Contains(lower, "git") || strings.Contains(lower, "commit") || strings.Contains(lower, "branch"):
		return "git"
	case strings.Contains(lower, "db") || strings.Contains(lower, "database") || strings.Contains(lower, "sql"):
		return "database"
	case strings.Contains(lower, "api") || strings.Contains(lower, "http") || strings.Contains(lower, "rest"):
		return "api"
	case strings.Contains(lower, "doc") || strings.Contains(lower, "readme") || strings.Contains(lower, "comment"):
		return "docs"
	default:
		return "general"
	}
}

// httpGet performs a simple HTTP GET request.
func httpGet(url string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ExportSkillToFile saves an exported skill to a local JSON file.
func (m *Manager) ExportSkillToFile(name, destPath string) error {
	exported, err := m.ExportSkill(name)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, []byte(exported), 0644)
}

// ImportFromFile imports a skill from a local JSON market file.
func (m *Manager) ImportFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read market file: %w", err)
	}
	// Try parsing as array first (ExportAll format)
	var arr []MarketEntry
	if err := json.Unmarshal(data, &arr); err == nil {
		for _, entry := range arr {
			if err := m.ImportFromMarket(mustMarshal(entry)); err != nil {
				return fmt.Errorf("import %s: %w", entry.Name, err)
			}
		}
		return nil
	}
	// Try single entry
	return m.ImportFromMarket(string(data))
}

// mustMarshal marshals a value to JSON or panics.
func mustMarshal(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}
