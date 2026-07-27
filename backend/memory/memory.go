package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultBudget = 1200

var storeMu sync.Mutex

var (
	initMu       sync.Mutex
	initCalls    = map[string]*projectInitCall{}
	warmedCaches sync.Map
)

type projectInitCall struct {
	done   chan struct{}
	result ProjectCacheResult
	err    error
}

// Service exposes project memory operations to Wails and the AI service.
type Service struct{}

func NewService() *Service { return &Service{} }

type ProjectMemory struct {
	ID        string   `json:"id"`
	Project   string   `json:"project"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Files     []string `json:"files,omitempty"`
	Concepts  []string `json:"concepts,omitempty"`
	Strength  int      `json:"strength"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	Accesses  int      `json:"accesses"`
}

type storeFile struct {
	Memories []ProjectMemory `json:"memories"`
}

// ProjectCacheResult reports whether a project's persistent memory cache was
// already available or was silently created during this call.
type ProjectCacheResult struct {
	Project     string `json:"project"`
	Exists      bool   `json:"exists"`
	Initialized bool   `json:"initialized"`
	MemoryCount int    `json:"memoryCount"`
}

func storePath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "tiancan-ai-ide", "project-memory.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tiancan-ai-ide", "project-memory.json")
}

func readStore() (storeFile, error) {
	var st storeFile
	path := storePath()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return st, nil
	}
	return st, json.Unmarshal(b, &st)
}

func writeStore(st storeFile) error {
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func projectMemoryCount(st storeFile, projectRoot string) int {
	count := 0
	for _, memory := range st.Memories {
		if memory.Project == projectRoot {
			count++
		}
	}
	return count
}

func normalizeProjectRoot(projectRoot string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return "", fmt.Errorf("project root is required")
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat project root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root is not a directory")
	}
	return filepath.Clean(abs), nil
}

func readSmallFile(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	b := make([]byte, limit)
	n, _ := f.Read(b)
	return string(b[:n])
}

func firstPrefixedValue(content, prefix string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `"'`)
		}
	}
	return ""
}

func compactReadme(content string, limit int) string {
	var parts []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "[") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#>*- "))
		if line == "" || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "```") {
			continue
		}
		parts = append(parts, line)
		if len([]rune(strings.Join(parts, "；"))) >= limit {
			break
		}
	}
	return truncateRunes(strings.Join(parts, "；"), limit)
}

func seedProjectMemories(projectRoot string) ([]ProjectMemory, error) {
	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("read project root: %w", err)
	}

	fileSet := map[string]bool{}
	var directories []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if entry.IsDir() {
			if len(directories) < 8 && name != "node_modules" && name != "dist" && name != "build" && name != "vendor" {
				directories = append(directories, name)
			}
			continue
		}
		fileSet[name] = true
	}
	sort.Strings(directories)

	var manifests, concepts, details []string
	addManifest := func(name, concept string) {
		if fileSet[name] {
			manifests = append(manifests, name)
			concepts = append(concepts, concept)
		}
	}
	addManifest("go.mod", "Go")
	addManifest("package.json", "Node.js")
	addManifest("Cargo.toml", "Rust")
	addManifest("pyproject.toml", "Python")
	addManifest("pom.xml", "Java")
	addManifest("build.gradle", "Gradle")
	addManifest("pubspec.yaml", "Flutter")
	addManifest("wails.json", "Wails")
	addManifest("docker-compose.yml", "Docker")
	addManifest("Dockerfile", "Docker")

	if fileSet["go.mod"] {
		if module := firstPrefixedValue(readSmallFile(filepath.Join(projectRoot, "go.mod"), 16*1024), "module "); module != "" {
			details = append(details, "Go module "+module)
		}
	}
	if fileSet["package.json"] {
		var pkg struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal([]byte(readSmallFile(filepath.Join(projectRoot, "package.json"), 64*1024)), &pkg)
		if strings.TrimSpace(pkg.Name) != "" {
			details = append(details, "package "+strings.TrimSpace(pkg.Name))
		}
	}

	profileParts := []string{"项目名称 " + filepath.Base(projectRoot)}
	if len(concepts) > 0 {
		profileParts = append(profileParts, "技术栈 "+strings.Join(uniqueNonEmpty(concepts), ", "))
	}
	if len(details) > 0 {
		profileParts = append(profileParts, strings.Join(details, ", "))
	}
	if len(directories) > 0 {
		profileParts = append(profileParts, "一级目录 "+strings.Join(directories, ", "))
	}

	now := time.Now().Format(time.RFC3339)
	profileContent := "项目基础画像: " + strings.Join(profileParts, "；")
	seeds := []ProjectMemory{{
		ID:        memoryID(projectRoot, profileContent),
		Project:   projectRoot,
		Type:      "fact",
		Title:     "项目基础画像",
		Content:   profileContent,
		Files:     uniqueNonEmpty(manifests),
		Concepts:  uniqueNonEmpty(concepts),
		Strength:  6,
		CreatedAt: now,
		UpdatedAt: now,
	}}

	for _, readmeName := range []string{"README.md", "README.MD", "README"} {
		if !fileSet[readmeName] {
			continue
		}
		summary := compactReadme(readSmallFile(filepath.Join(projectRoot, readmeName), 64*1024), 480)
		if summary != "" {
			content := "项目说明摘要: " + summary
			seeds = append(seeds, ProjectMemory{
				ID:        memoryID(projectRoot, content),
				Project:   projectRoot,
				Type:      "architecture",
				Title:     "项目说明摘要",
				Content:   content,
				Files:     []string{readmeName},
				Concepts:  []string{"project-overview"},
				Strength:  5,
				CreatedAt: now,
				UpdatedAt: now,
			})
		}
		break
	}

	return seeds, nil
}

// EnsureProjectMemory performs a cheap persistent existence check and creates
// an initial cache without invoking an LLM. Calls for the same project are
// coalesced, and successful results stay hot for the process lifetime.
func EnsureProjectMemory(projectRoot string) (ProjectCacheResult, error) {
	root, err := normalizeProjectRoot(projectRoot)
	if err != nil {
		return ProjectCacheResult{}, err
	}
	if cached, ok := warmedCaches.Load(root); ok {
		return cached.(ProjectCacheResult), nil
	}

	initMu.Lock()
	if call, ok := initCalls[root]; ok {
		initMu.Unlock()
		<-call.done
		return call.result, call.err
	}
	call := &projectInitCall{done: make(chan struct{})}
	initCalls[root] = call
	initMu.Unlock()

	call.result, call.err = ensureProjectMemoryOnce(root)
	if call.err == nil {
		warmedCaches.Store(root, call.result)
	}
	initMu.Lock()
	delete(initCalls, root)
	close(call.done)
	initMu.Unlock()
	return call.result, call.err
}

func ensureProjectMemoryOnce(projectRoot string) (ProjectCacheResult, error) {
	storeMu.Lock()
	st, err := readStore()
	if err != nil {
		storeMu.Unlock()
		return ProjectCacheResult{}, err
	}
	count := projectMemoryCount(st, projectRoot)
	storeMu.Unlock()
	if count > 0 {
		return ProjectCacheResult{Project: projectRoot, Exists: true, MemoryCount: count}, nil
	}

	seeds, err := seedProjectMemories(projectRoot)
	if err != nil {
		return ProjectCacheResult{}, err
	}

	storeMu.Lock()
	defer storeMu.Unlock()
	st, err = readStore()
	if err != nil {
		return ProjectCacheResult{}, err
	}
	if count = projectMemoryCount(st, projectRoot); count > 0 {
		return ProjectCacheResult{Project: projectRoot, Exists: true, MemoryCount: count}, nil
	}
	st.Memories = append(st.Memories, seeds...)
	if err := writeStore(st); err != nil {
		return ProjectCacheResult{}, err
	}
	return ProjectCacheResult{Project: projectRoot, Exists: true, Initialized: true, MemoryCount: len(seeds)}, nil
}

func normalizeType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "architecture", "workflow", "preference", "bug", "pattern", "fact":
		return strings.ToLower(strings.TrimSpace(t))
	default:
		return "fact"
	}
}

func canonicalizeContent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func memoryID(project, content string) string {
	h := sha1.Sum([]byte(project + "\x00" + canonicalizeContent(content)))
	return "pmem_" + hex.EncodeToString(h[:])[:16]
}

func score(m ProjectMemory, query string) int {
	s := m.Strength*10 + m.Accesses
	if query == "" {
		return s
	}
	hay := strings.ToLower(m.Title + " " + m.Content + " " + strings.Join(m.Files, " ") + " " + strings.Join(m.Concepts, " "))
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if strings.Contains(hay, term) {
			s += 20
		}
	}
	return s
}

// Remember saves or reinforces a project-scoped memory.
func Remember(projectRoot, content, memType string, files, concepts []string) (ProjectMemory, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	content = strings.TrimSpace(content)
	if projectRoot == "" {
		return ProjectMemory{}, fmt.Errorf("project root is required")
	}
	if content == "" {
		return ProjectMemory{}, fmt.Errorf("memory content is required")
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	st, err := readStore()
	if err != nil {
		return ProjectMemory{}, err
	}
	now := time.Now().Format(time.RFC3339)
	id := memoryID(projectRoot, content)
	files = uniqueNonEmpty(files)
	concepts = uniqueNonEmpty(concepts)
	for i := range st.Memories {
		if st.Memories[i].ID == id {
			st.Memories[i].Strength++
			if st.Memories[i].Strength > 10 {
				st.Memories[i].Strength = 10
			}
			st.Memories[i].UpdatedAt = now
			st.Memories[i].Files = uniqueNonEmpty(append(st.Memories[i].Files, files...))
			st.Memories[i].Concepts = uniqueNonEmpty(append(st.Memories[i].Concepts, concepts...))
			if err := writeStore(st); err != nil {
				return ProjectMemory{}, err
			}
			return st.Memories[i], nil
		}
	}

	title := content
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80])
	}
	mem := ProjectMemory{
		ID:        id,
		Project:   projectRoot,
		Type:      normalizeType(memType),
		Title:     title,
		Content:   content,
		Files:     files,
		Concepts:  concepts,
		Strength:  7,
		CreatedAt: now,
		UpdatedAt: now,
	}
	st.Memories = append(st.Memories, mem)
	if err := writeStore(st); err != nil {
		return ProjectMemory{}, err
	}
	return mem, nil
}

// Recall returns ranked project memories.
func Recall(projectRoot, query string, limit int) ([]ProjectMemory, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return nil, fmt.Errorf("project root is required")
	}
	if limit <= 0 {
		limit = 8
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	st, err := readStore()
	if err != nil {
		return nil, err
	}
	var out []ProjectMemory
	for _, m := range st.Memories {
		if m.Project == projectRoot {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := score(out[i], query), score(out[j], query)
		if si == sj {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return si > sj
	})
	if len(out) > limit {
		out = out[:limit]
	}
	ids := map[string]bool{}
	for _, m := range out {
		ids[m.ID] = true
	}
	for i := range st.Memories {
		if ids[st.Memories[i].ID] {
			st.Memories[i].Accesses++
		}
	}
	_ = writeStore(st)
	return out, nil
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= limit {
		return string(rs)
	}
	if limit <= 1 {
		return string(rs[:limit])
	}
	return string(rs[:limit-1]) + "…"
}

func bestByType(memories []ProjectMemory, memType string) *ProjectMemory {
	var picked *ProjectMemory
	bestScore := -1
	for i := range memories {
		if memories[i].Type != memType {
			continue
		}
		s := score(memories[i], "")
		if picked == nil || s > bestScore {
			picked = &memories[i]
			bestScore = s
		}
	}
	return picked
}

func topTerms(memories []ProjectMemory, extractor func(ProjectMemory) []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	counts := map[string]int{}
	labels := map[string]string{}
	for _, mem := range memories {
		for _, item := range extractor(mem) {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			key := strings.ToLower(item)
			counts[key] += mem.Strength + 1
			if labels[key] == "" {
				labels[key] = item
			}
		}
	}
	type pair struct {
		key   string
		score int
	}
	items := make([]pair, 0, len(counts))
	for key, value := range counts {
		items = append(items, pair{key: key, score: value})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].key < items[j].key
		}
		return items[i].score > items[j].score
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, labels[item.key])
	}
	return out
}

func profileLines(memories []ProjectMemory) []string {
	if len(memories) == 0 {
		return nil
	}
	var lines []string
	if concepts := topTerms(memories, func(mem ProjectMemory) []string { return mem.Concepts }, 5); len(concepts) > 0 {
		lines = append(lines, "关键概念: "+strings.Join(concepts, ", "))
	}
	if files := topTerms(memories, func(mem ProjectMemory) []string { return mem.Files }, 4); len(files) > 0 {
		lines = append(lines, "关键文件: "+strings.Join(files, ", "))
	}
	typeLabels := map[string]string{
		"architecture": "架构约束",
		"workflow":     "工作流约定",
		"preference":   "用户偏好",
		"bug":          "易踩坑",
		"pattern":      "代码模式",
	}
	for _, memType := range []string{"architecture", "workflow", "preference", "bug", "pattern"} {
		if mem := bestByType(memories, memType); mem != nil {
			lines = append(lines, fmt.Sprintf("%s: %s", typeLabels[memType], truncateRunes(mem.Content, 90)))
		}
	}
	return lines
}

// RenderContext returns a compact token-budgeted project memory block.
func RenderContext(projectRoot, query string, budget int) string {
	if budget <= 0 {
		budget = defaultBudget
	}
	memories, err := Recall(projectRoot, query, 12)
	if err != nil || len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("项目记忆（跨模型共享，优先遵守；如与代码冲突，以代码为准）：\n")
	used := len([]rune(b.String()))
	profileBudget := budget / 2
	if profileBudget < used {
		profileBudget = used
	}
	for _, line := range profileLines(memories) {
		rendered := "- " + line + "\n"
		lineCost := len([]rune(rendered))
		if used+lineCost > profileBudget {
			break
		}
		b.WriteString(rendered)
		used += lineCost
	}
	for _, m := range memories {
		line := fmt.Sprintf("- [%s] %s", m.Type, truncateRunes(m.Content, 160))
		if len(m.Files) > 0 {
			line += " (files: " + strings.Join(m.Files, ", ") + ")"
		}
		line += "\n"
		lineCost := len([]rune(line))
		if used+lineCost > budget {
			break
		}
		b.WriteString(line)
		used += lineCost
	}
	return strings.TrimSpace(b.String())
}

func (s *Service) Remember(projectRoot, content, memType string, files, concepts []string) (ProjectMemory, error) {
	return Remember(projectRoot, content, memType, files, concepts)
}

func (s *Service) Recall(projectRoot, query string, limit int) ([]ProjectMemory, error) {
	return Recall(projectRoot, query, limit)
}

func (s *Service) RenderContext(projectRoot, query string, budget int) (string, error) {
	return RenderContext(projectRoot, query, budget), nil
}

func (s *Service) EnsureProjectMemory(projectRoot string) (ProjectCacheResult, error) {
	return EnsureProjectMemory(projectRoot)
}
