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

func normalizeType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "architecture", "workflow", "preference", "bug", "pattern", "fact":
		return strings.ToLower(strings.TrimSpace(t))
	default:
		return "fact"
	}
}

func memoryID(project, content string) string {
	h := sha1.Sum([]byte(project + "\x00" + strings.ToLower(strings.TrimSpace(content))))
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
	for i := range st.Memories {
		if st.Memories[i].ID == id {
			st.Memories[i].Strength++
			if st.Memories[i].Strength > 10 {
				st.Memories[i].Strength = 10
			}
			st.Memories[i].UpdatedAt = now
			if len(files) > 0 {
				st.Memories[i].Files = files
			}
			if len(concepts) > 0 {
				st.Memories[i].Concepts = concepts
			}
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
	for _, m := range memories {
		line := fmt.Sprintf("- [%s] %s", m.Type, m.Content)
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
