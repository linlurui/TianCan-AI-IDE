package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ConfigName is the project-level config file name (analogous to TIANCAN.md).
const ConfigName = "TIANCAN.md"
const LocalConfigName = "TIANCAN.local.md"
const DotDir = ".tiancan"
const RulesDir = "rules"

// MaxMemoryCharacterCount is the recommended max chars for a config file.
const MaxMemoryCharacterCount = 40000

// textFileExtensions are allowed for @include directives.
// Loaded from TIANCAN_TEXT_EXTENSIONS env var — no hardcoded lists.
var textFileExtensions = loadTextFileExtensions()

// loadTextFileExtensions loads allowed text file extensions from env.
// TIANCAN_TEXT_EXTENSIONS=.md,.txt,.json,.yaml,.go,.py,...
func loadTextFileExtensions() map[string]bool {
	m := make(map[string]bool)
	if v := os.Getenv("TIANCAN_TEXT_EXTENSIONS"); v != "" {
		for _, e := range strings.Split(v, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				m[e] = true
			}
		}
		return m
	}
	return map[string]bool{
		".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true,
		".toml": true, ".xml": true, ".csv": true, ".html": true, ".css": true,
		".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".py": true,
		".go": true, ".rs": true, ".java": true, ".c": true, ".cpp": true,
		".h": true, ".cs": true, ".swift": true, ".sh": true, ".bash": true,
		".zsh": true, ".sql": true, ".proto": true, ".env": true, ".ini": true,
		".cfg": true, ".conf": true, ".svelte": true, ".vue": true,
	}
}

// includePattern matches @include directives: @path, @./path, @~/path, @/path
var includePattern = regexp.MustCompile(`@(~?[/\\.\w][\w./\\-]*)`)

// Frontmatter represents parsed YAML frontmatter from a config file.
type Frontmatter struct {
	Globs       []string `json:"globs,omitempty"`
	Description string   `json:"description,omitempty"`
	AlwaysApply bool     `json:"alwaysApply,omitempty"`
}

// ConfigFile holds a loaded config file with metadata.
type ConfigFile struct {
	Path        string
	Type        types.MemoryType `json:"type"`
	Content     string
	Frontmatter Frontmatter
	Globs       []string
	Parent      string
}

// LoadAllConfigs loads config files in priority order:
// 1. Managed (system-wide)  2. User (~/.tiancan/)  3. Project (walk up from cwd)
// 4. Local (TIANCAN.local.md)  5. AutoMem entrypoint
// Later files have higher priority.
func LoadAllConfigs(cwd string, homeDir string) ([]ConfigFile, error) {
	var result []ConfigFile
	processed := make(map[string]bool)

	// 1. Managed config (system-wide, e.g. /etc/tiancan/TIANCAN.md)
	if managedPath := getManagedConfigPath(); managedPath != "" {
		if f, err := loadConfigFile(managedPath, types.MemoryManaged, processed); err == nil {
			result = append(result, f)
		}
		loadRulesDir(filepath.Join(filepath.Dir(managedPath), DotDir, RulesDir),
			types.MemoryManaged, &result, processed)
	}

	// 2. User config (~/.tiancan/TIANCAN.md)
	userDir := filepath.Join(homeDir, DotDir)
	if f, err := loadConfigFile(filepath.Join(userDir, ConfigName), types.MemoryUser, processed); err == nil {
		result = append(result, f)
	}
	loadRulesDir(filepath.Join(userDir, RulesDir), types.MemoryUser, &result, processed)

	// 3. Project + Local configs (walk from root to cwd, later = higher priority)
	dirs := walkToRoot(cwd)
	for _, dir := range dirs {
		// Project: TIANCAN.md
		if f, err := loadConfigFile(filepath.Join(dir, ConfigName), types.MemoryProject, processed); err == nil {
			result = append(result, f)
		}
		// Project: .tiancan/TIANCAN.md
		if f, err := loadConfigFile(filepath.Join(dir, DotDir, ConfigName), types.MemoryProject, processed); err == nil {
			result = append(result, f)
		}
		// Project: .tiancan/rules/*.md
		loadRulesDir(filepath.Join(dir, DotDir, RulesDir), types.MemoryProject, &result, processed)
		// Local: TIANCAN.local.md
		if f, err := loadConfigFile(filepath.Join(dir, LocalConfigName), types.MemoryLocal, processed); err == nil {
			result = append(result, f)
		}
	}

	return result, nil
}

// loadConfigFile reads and processes a single config file.
func loadConfigFile(path string, memType types.MemoryType, processed map[string]bool) (ConfigFile, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return ConfigFile{}, err
	}
	if processed[absPath] {
		return ConfigFile{}, fmt.Errorf("already processed: %s", absPath)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return ConfigFile{}, err
	}

	content := string(data)
	if len(content) > MaxMemoryCharacterCount {
		content = content[:MaxMemoryCharacterCount]
	}

	fm := parseFrontmatter(content)
	// Strip frontmatter from content
	content = stripFrontmatter(content)

	// Process @include directives
	content, err = processIncludes(content, filepath.Dir(absPath), homeDirFromPath(absPath), processed)
	if err != nil {
		// Log but don't fail on include errors
		fmt.Fprintf(os.Stderr, "config: include error in %s: %v\n", absPath, err)
	}

	processed[absPath] = true

	return ConfigFile{
		Path:        absPath,
		Type:        memType,
		Content:     content,
		Frontmatter: fm,
		Globs:       fm.Globs,
	}, nil
}

// loadRulesDir loads all .md files from a rules directory.
func loadRulesDir(dir string, memType types.MemoryType, result *[]ConfigFile, processed map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			loadRulesDir(filepath.Join(dir, entry.Name()), memType, result, processed)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if f, err := loadConfigFile(path, memType, processed); err == nil {
			*result = append(*result, f)
		}
	}
}

// parseFrontmatter extracts YAML frontmatter from content.
func parseFrontmatter(content string) Frontmatter {
	fm := Frontmatter{}
	if !strings.HasPrefix(content, "---") {
		return fm
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return fm
	}
	fmText := strings.TrimSpace(content[3 : end+3])

	// Parse simple YAML key: value pairs
	for _, line := range strings.Split(fmText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch key {
		case "globs":
			// Parse array: ["*.go", "*.ts"] or - *.go format
			fm.Globs = parseGlobs(val)
		case "description":
			fm.Description = strings.Trim(val, `"'`)
		case "alwaysApply":
			fm.AlwaysApply = val == "true"
		}
	}
	return fm
}

// parseGlobs parses glob patterns from YAML value.
func parseGlobs(val string) []string {
	val = strings.TrimSpace(val)
	var globs []string
	// Array format: ["*.go", "*.ts"]
	if strings.HasPrefix(val, "[") {
		val = strings.Trim(val, "[]")
		for _, item := range strings.Split(val, ",") {
			item = strings.TrimSpace(item)
			item = strings.Trim(item, `"'`)
			if item != "" {
				globs = append(globs, item)
			}
		}
	}
	return globs
}

// stripFrontmatter removes YAML frontmatter from content.
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return content
	}
	return strings.TrimSpace(content[end+6:])
}

// processIncludes resolves @include directives in content.
func processIncludes(content string, baseDir string, homeDir string, processed map[string]bool) (string, error) {
	var buf bytes.Buffer
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		// Skip lines inside code blocks
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			buf.WriteString(line)
			buf.WriteByte('\n')
			continue
		}
		matches := includePattern.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			buf.WriteString(line)
			buf.WriteByte('\n')
			continue
		}
		// Replace each @include with file content
		result := line
		for _, m := range matches {
			includePath := m[1]
			resolved := resolveIncludePath(includePath, baseDir, homeDir)
			if resolved == "" {
				continue
			}
			absResolved, err := filepath.Abs(resolved)
			if err != nil {
				continue
			}
			if processed[absResolved] {
				continue
			}
			// Check extension is allowed
			ext := strings.ToLower(filepath.Ext(absResolved))
			if !textFileExtensions[ext] {
				continue
			}
			data, err := os.ReadFile(absResolved)
			if err != nil {
				continue
			}
			processed[absResolved] = true
			result = strings.Replace(result, m[0], string(data), 1)
		}
		buf.WriteString(result)
		buf.WriteByte('\n')
	}
	return buf.String(), scanner.Err()
}

// resolveIncludePath resolves an @include path to an absolute path.
func resolveIncludePath(includePath string, baseDir string, homeDir string) string {
	if strings.HasPrefix(includePath, "~/") {
		return filepath.Join(homeDir, includePath[2:])
	}
	if filepath.IsAbs(includePath) {
		return includePath
	}
	return filepath.Join(baseDir, includePath)
}

// walkToRoot returns directories from root to cwd.
func walkToRoot(cwd string) []string {
	var dirs []string
	current := cwd
	for {
		dirs = append([]string{current}, dirs...)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return dirs
}

func getManagedConfigPath() string {
	// Check environment variable first
	if p := os.Getenv("TIANCAN_MANAGED_CONFIG"); p != "" {
		return p
	}
	// Default: /etc/tiancan/TIANCAN.md (Linux/macOS)
	if _, err := os.Stat("/etc/tiancan/TIANCAN.md"); err == nil {
		return "/etc/tiancan/TIANCAN.md"
	}
	return ""
}

func homeDirFromPath(path string) string {
	home, _ := os.UserHomeDir()
	return home
}

// BuildSystemPrompt assembles the system prompt from all loaded config files.
func BuildSystemPrompt(configs []ConfigFile) string {
	var sb strings.Builder
	sb.WriteString("# Instructions\n\n")
	sb.WriteString("Codebase and user instructions are shown below. Be sure to adhere to these instructions. ")
	sb.WriteString("IMPORTANT: These instructions OVERRIDE default behavior and you MUST follow them exactly as written.\n\n")

	for _, cfg := range configs {
		if cfg.Content == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("## %s: %s\n\n", cfg.Type, cfg.Path))
		sb.WriteString(cfg.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// MatchesGlob checks if a file path matches any of the config's glob patterns.
func (c *ConfigFile) MatchesGlob(filePath string) bool {
	if len(c.Globs) == 0 {
		return c.Frontmatter.AlwaysApply
	}
	name := filepath.Base(filePath)
	for _, pattern := range c.Globs {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
		// Support directory patterns like "src/**/*.go"
		if strings.Contains(pattern, "/") {
			if matched, _ := filepath.Match(pattern, filePath); matched {
				return true
			}
		}
	}
	return false
}
