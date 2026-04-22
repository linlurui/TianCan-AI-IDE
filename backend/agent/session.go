package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session represents a saved conversation session.
type Session struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	RootPath    string    `json:"rootPath"`
	AgentType   string    `json:"agentType,omitempty"`
	Messages    []Message `json:"messages"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	TotalTokens int       `json:"totalTokens"`
	DurationMs  int64     `json:"durationMs"`
}

// SessionStore manages conversation session persistence.
type SessionStore struct {
	baseDir string
	mu      sync.RWMutex
	cache   map[string]*Session
}

// NewSessionStore creates a new session store rooted at the given directory.
func NewSessionStore(baseDir string) *SessionStore {
	s := &SessionStore{
		baseDir: baseDir,
		cache:   make(map[string]*Session),
	}
	os.MkdirAll(baseDir, 0755)
	s.loadCache()
	return s
}

// Save persists a session to disk.
func (s *SessionStore) Save(session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session.UpdatedAt = time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}

	// Auto-generate title from first user message
	if session.Title == "" {
		for _, m := range session.Messages {
			if m.Role == RoleUser {
				title := m.Content
				if len(title) > 80 {
					title = title[:77] + "..."
				}
				session.Title = title
				break
			}
		}
	}

	path := s.sessionPath(session.ID)
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}

	s.cache[session.ID] = session
	return nil
}

// Load reads a session from disk.
func (s *SessionStore) Load(id string) (*Session, error) {
	s.mu.RLock()
	if cached, ok := s.cache[id]; ok {
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	path := s.sessionPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	s.mu.Lock()
	s.cache[id] = &session
	s.mu.Unlock()

	return &session, nil
}

// Delete removes a session from disk.
func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.sessionPath(id)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session file: %w", err)
	}
	delete(s.cache, id)
	return nil
}

// List returns sessions sorted by most recently updated.
func (s *SessionStore) List() ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessions []*Session
	for _, session := range s.cache {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

// ListByProject returns sessions for a specific root path.
func (s *SessionStore) ListByProject(rootPath string) ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessions []*Session
	for _, session := range s.cache {
		if session.RootPath == rootPath {
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

// Search searches session titles and message content for the query.
func (s *SessionStore) Search(query string) ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lower := strings.ToLower(query)
	var results []*Session
	for _, session := range s.cache {
		if strings.Contains(strings.ToLower(session.Title), lower) {
			results = append(results, session)
			continue
		}
		for _, m := range session.Messages {
			if strings.Contains(strings.ToLower(m.Content), lower) {
				results = append(results, session)
				break
			}
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})
	return results, nil
}

// NewSession creates a new session with a generated ID.
func NewSession(rootPath string) *Session {
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixMilli())
	return &Session{
		ID:        id,
		RootPath:  rootPath,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{},
	}
}

// AddMessage adds a message to the session.
func (s *Session) AddMessage(role MessageRole, content string) {
	s.Messages = append(s.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
}

// LastUserMessage returns the last user message content.
func (s *Session) LastUserMessage() string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == RoleUser {
			return s.Messages[i].Content
		}
	}
	return ""
}

// Summary returns a brief summary string of the session.
func (s *Session) Summary() string {
	userMsgs := 0
	assistantMsgs := 0
	toolMsgs := 0
	for _, m := range s.Messages {
		switch m.Role {
		case RoleUser:
			userMsgs++
		case RoleAssistant:
			assistantMsgs++
		case RoleTool:
			toolMsgs++
		}
	}
	return fmt.Sprintf("%d messages (%d user, %d assistant, %d tool), %d tokens, %dms",
		len(s.Messages), userMsgs, assistantMsgs, toolMsgs, s.TotalTokens, s.DurationMs)
}

// --- internal ---

func (s *SessionStore) sessionPath(id string) string {
	return filepath.Join(s.baseDir, id+".json")
}

func (s *SessionStore) loadCache() {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		path := filepath.Join(s.baseDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			continue
		}
		s.cache[id] = &session
	}
}
