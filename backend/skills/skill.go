package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Skill represents a parsed .skill.yaml file.
type Skill struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Version     string      `yaml:"version"`
	Triggers    []string    `yaml:"triggers"`
	Steps       []SkillStep `yaml:"steps"`
	FilePath    string      `yaml:"-"`
}

// SkillStep is a single step within a skill.
type SkillStep struct {
	Tool string                 `yaml:"tool"`
	Args map[string]interface{} `yaml:"args"`
}

// Manager discovers, loads, and invokes skills.
// TODO: Add skill market support (import/export skill packages, community registry URL)
// TODO: Add skill validation and sandboxing before execution
// TODO: Add skill dependency resolution (skills that depend on other skills)
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
		filepath.Join(m.rootPath, ".tiancan", "skills"),
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
