package memory

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Semantic Memory Store ───────────────────────────────────────

// MemoryEntry represents a single memory record.
type MemoryEntry struct {
	ID          string                 `json:"id"`
	Content     string                 `json:"content"`
	Source      string                 `json:"source"`      // "user", "assistant", "system", "tool", "auto"
	Type        types.MemoryType       `json:"type"`        // Managed, User, Project, Local, AutoMem
	Tags        []string               `json:"tags,omitempty"`
	Embedding   []float64              `json:"embedding,omitempty"`
	Relevance   float64                `json:"relevance"`   // 0-1, how important
	AccessCount int                    `json:"accessCount"`
	CreatedAt   time.Time              `json:"createdAt"`
	AccessedAt  time.Time              `json:"accessedAt"`
	ExpiresAt   *time.Time             `json:"expiresAt,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// MemoryStore provides semantic memory storage and retrieval.
type MemoryStore struct {
	mu       sync.RWMutex
	entries  map[string]*MemoryEntry
	baseDir  string
	maxSize  int
	decayRate float64 // relevance decay per day
}

// NewMemoryStore creates a new semantic memory store.
func NewMemoryStore(baseDir string, maxSize int) *MemoryStore {
	if maxSize <= 0 {
		maxSize = 10000
	}
	ms := &MemoryStore{
		entries:   make(map[string]*MemoryEntry),
		baseDir:   baseDir,
		maxSize:   maxSize,
		decayRate: 0.05, // 5% relevance decay per day
	}
	ms.loadFromDisk()
	return ms
}

// Store adds a new memory entry.
func (ms *MemoryStore) Store(entry MemoryEntry) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if entry.ID == "" {
		entry.ID = generateMemoryID()
	}
	entry.CreatedAt = time.Now()
	entry.AccessedAt = time.Now()

	ms.entries[entry.ID] = &entry

	// Evict if over capacity
	if len(ms.entries) > ms.maxSize {
		ms.evict()
	}

	return ms.saveToDisk()
}

// StoreBatch adds multiple memory entries atomically.
func (ms *MemoryStore) StoreBatch(entries []MemoryEntry) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	for i := range entries {
		if entries[i].ID == "" {
			entries[i].ID = generateMemoryID()
		}
		entries[i].CreatedAt = time.Now()
		entries[i].AccessedAt = time.Now()
		ms.entries[entries[i].ID] = &entries[i]
	}

	if len(ms.entries) > ms.maxSize {
		ms.evict()
	}

	return ms.saveToDisk()
}

// Retrieve gets a memory by ID.
func (ms *MemoryStore) Retrieve(id string) (*MemoryEntry, bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	entry, ok := ms.entries[id]
	if ok {
		entry.AccessCount++
		entry.AccessedAt = time.Now()
	}
	return entry, ok
}

// Search performs keyword-based search over memories.
func (ms *MemoryStore) Search(query string, limit int) []MemoryEntry {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	lower := strings.ToLower(query)
	terms := strings.Fields(lower)

	var scored []struct {
		entry *MemoryEntry
		score float64
	}

	for _, entry := range ms.entries {
		score := ms.scoreEntry(entry, terms)
		if score > 0 {
			// Apply time decay
			score *= ms.decayFactor(entry)
			scored = append(scored, struct {
				entry *MemoryEntry
				score float64
			}{entry, score})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	result := make([]MemoryEntry, 0, limit)
	for i := 0; i < len(scored) && i < limit; i++ {
		result = append(result, *scored[i].entry)
	}
	return result
}

// SearchByTag retrieves memories matching specific tags.
func (ms *MemoryStore) SearchByTag(tags []string, limit int) []MemoryEntry {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	tagSet := make(map[string]bool)
	for _, t := range tags {
		tagSet[strings.ToLower(t)] = true
	}

	var result []MemoryEntry
	for _, entry := range ms.entries {
		matchCount := 0
		for _, t := range entry.Tags {
			if tagSet[strings.ToLower(t)] {
				matchCount++
			}
		}
		if matchCount > 0 {
			result = append(result, *entry)
		}
		if len(result) >= limit {
			break
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].AccessedAt.After(result[j].AccessedAt)
	})

	return result
}

// SearchByType retrieves memories of a specific type.
func (ms *MemoryStore) SearchByType(memType types.MemoryType, limit int) []MemoryEntry {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	var result []MemoryEntry
	for _, entry := range ms.entries {
		if entry.Type == memType {
			result = append(result, *entry)
		}
		if len(result) >= limit {
			break
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result
}

// GetContextForQuery retrieves the most relevant memories for a given query.
// This is the main method used by the agent to inject context into prompts.
func (ms *MemoryStore) GetContextForQuery(query string, maxTokens int) string {
	entries := ms.Search(query, 20)

	var sb strings.Builder
	tokenEstimate := 0

	for _, entry := range entries {
		line := fmt.Sprintf("- [%s] %s", entry.Type, entry.Content)
		estTokens := len(line) / 4 // rough token estimate
		if tokenEstimate+estTokens > maxTokens {
			break
		}
		sb.WriteString(line + "\n")
		tokenEstimate += estTokens
	}

	return sb.String()
}

// Delete removes a memory by ID.
func (ms *MemoryStore) Delete(id string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	delete(ms.entries, id)
	return ms.saveToDisk()
}

// Count returns the total number of memories.
func (ms *MemoryStore) Count() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.entries)
}

// PruneExpired removes all expired memories.
func (ms *MemoryStore) PruneExpired() int {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	now := time.Now()
	pruned := 0
	for id, entry := range ms.entries {
		if entry.ExpiresAt != nil && now.After(*entry.ExpiresAt) {
			delete(ms.entries, id)
			pruned++
		}
	}
	if pruned > 0 {
		ms.saveToDisk()
	}
	return pruned
}

// DecayRelevance applies time-based relevance decay to all entries.
func (ms *MemoryStore) DecayRelevance() {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	for _, entry := range ms.entries {
		factor := ms.decayFactor(entry)
		entry.Relevance *= factor
		// Boost frequently accessed memories
		if entry.AccessCount > 3 {
			entry.Relevance = math.Min(1.0, entry.Relevance*1.1)
		}
	}
}

// ── Context Window ──────────────────────────────────────────────

// ContextWindow manages the agent's context window with priority-based selection.
type ContextWindow struct {
	mu        sync.RWMutex
	maxTokens int
	items     []ContextItem
}

// ContextItem represents an item in the context window.
type ContextItem struct {
	Content    string    `json:"content"`
	Source     string    `json:"source"`     // "system", "memory", "conversation", "tool_result"
	Priority   float64   `json:"priority"`   // 0-1, higher = more likely to keep
	Tokens     int       `json:"tokens"`
	InsertedAt time.Time `json:"insertedAt"`
}

// NewContextWindow creates a context window with a token budget.
func NewContextWindow(maxTokens int) *ContextWindow {
	if maxTokens <= 0 {
		maxTokens = 128000
	}
	return &ContextWindow{
		maxTokens: maxTokens,
	}
}

// Add inserts an item into the context window.
func (cw *ContextWindow) Add(item ContextItem) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	item.Tokens = estimateTokens(item.Content)
	item.InsertedAt = time.Now()
	cw.items = append(cw.items, item)
}

// Build assembles the context window content within the token budget.
func (cw *ContextWindow) Build() string {
	cw.mu.RLock()
	defer cw.mu.RUnlock()

	// Sort by priority (descending), then by insertion order
	sorted := make([]ContextItem, len(cw.items))
	copy(sorted, cw.items)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	var sb strings.Builder
	usedTokens := 0

	for _, item := range sorted {
		if usedTokens+item.Tokens > cw.maxTokens {
			// Try to fit a truncated version
			remaining := cw.maxTokens - usedTokens
			if remaining > 100 {
				content := item.Content
				maxChars := remaining * 4
				if len(content) > maxChars {
					content = content[:maxChars] + "\n...[truncated]"
				}
				sb.WriteString(content + "\n")
				usedTokens += estimateTokens(content)
			}
			break
		}
		sb.WriteString(item.Content + "\n")
		usedTokens += item.Tokens
	}

	return sb.String()
}

// TokenUsage returns current token usage.
func (cw *ContextWindow) TokenUsage() int {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	total := 0
	for _, item := range cw.items {
		total += item.Tokens
	}
	return total
}

// Clear removes all items.
func (cw *ContextWindow) Clear() {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.items = cw.items[:0]
}

// ── Conversation Memory ─────────────────────────────────────────

// ConversationMemory tracks conversation-specific context.
type ConversationMemory struct {
	mu         sync.RWMutex
	sessionID  string
	messages   []types.Message
	summaries  []string
	entityMap  map[string][]string // entity → facts
	toolUsage  map[string]int      // tool name → usage count
	topicStack []string            // current topic stack
}

// NewConversationMemory creates conversation memory for a session.
func NewConversationMemory(sessionID string) *ConversationMemory {
	return &ConversationMemory{
		sessionID: sessionID,
		entityMap: make(map[string][]string),
		toolUsage: make(map[string]int),
	}
}

// AddMessage adds a message to the conversation history.
func (cm *ConversationMemory) AddMessage(msg types.Message) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.messages = append(cm.messages, msg)

	// Track tool usage
	if msg.ToolName != "" {
		cm.toolUsage[msg.ToolName]++
	}
}

// GetMessages returns recent messages up to maxCount.
func (cm *ConversationMemory) GetMessages(maxCount int) []types.Message {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if maxCount <= 0 || len(cm.messages) <= maxCount {
		return cm.messages
	}
	return cm.messages[len(cm.messages)-maxCount:]
}

// AddSummary adds a compacted summary section.
func (cm *ConversationMemory) AddSummary(summary string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.summaries = append(cm.summaries, summary)
}

// GetSummaries returns all summaries.
func (cm *ConversationMemory) GetSummaries() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.summaries
}

// RecordEntity records a fact about an entity.
func (cm *ConversationMemory) RecordEntity(entity, fact string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.entityMap[entity] = append(cm.entityMap[entity], fact)
}

// GetEntityFacts returns all known facts about an entity.
func (cm *ConversationMemory) GetEntityFacts(entity string) []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.entityMap[entity]
}

// GetToolUsage returns tool usage statistics.
func (cm *ConversationMemory) GetToolUsage() map[string]int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]int)
	for k, v := range cm.toolUsage {
		result[k] = v
	}
	return result
}

// PushTopic pushes a topic onto the topic stack.
func (cm *ConversationMemory) PushTopic(topic string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.topicStack = append(cm.topicStack, topic)
}

// PopTopic pops the current topic.
func (cm *ConversationMemory) PopTopic() string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if len(cm.topicStack) == 0 {
		return ""
	}
	topic := cm.topicStack[len(cm.topicStack)-1]
	cm.topicStack = cm.topicStack[:len(cm.topicStack)-1]
	return topic
}

// CurrentTopic returns the current topic.
func (cm *ConversationMemory) CurrentTopic() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if len(cm.topicStack) == 0 {
		return ""
	}
	return cm.topicStack[len(cm.topicStack)-1]
}

// ── internal helpers ────────────────────────────────────────────

func (ms *MemoryStore) scoreEntry(entry *MemoryEntry, terms []string) float64 {
	lower := strings.ToLower(entry.Content)
	score := 0.0

	for _, term := range terms {
		count := strings.Count(lower, term)
		if count > 0 {
			// TF-based scoring
			score += float64(count) * entry.Relevance
		}
	}

	// Boost by tag matches
	lowerTags := make([]string, len(entry.Tags))
	for i, t := range entry.Tags {
		lowerTags[i] = strings.ToLower(t)
	}
	for _, term := range terms {
		for _, tag := range lowerTags {
			if strings.Contains(tag, term) {
				score += 2.0
			}
		}
	}

	// Boost by source type
	switch entry.Source {
	case "user":
		score *= 1.5 // user-stated facts are important
	case "system":
		score *= 1.3
	}

	return score
}

func (ms *MemoryStore) decayFactor(entry *MemoryEntry) float64 {
	daysSinceAccess := time.Since(entry.AccessedAt).Hours() / 24
	return math.Exp(-ms.decayRate * daysSinceAccess)
}

func (ms *MemoryStore) evict() {
	// Remove lowest relevance entries until under capacity
	type scored struct {
		id    string
		score float64
	}
	var items []scored
	for id, entry := range ms.entries {
		items = append(items, scored{id, entry.Relevance * ms.decayFactor(entry)})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].score < items[j].score
	})

	toRemove := len(ms.entries) - ms.maxSize/2
	for i := 0; i < toRemove && i < len(items); i++ {
		delete(ms.entries, items[i].id)
	}
}

func (ms *MemoryStore) saveToDisk() error {
	if ms.baseDir == "" {
		return nil
	}
	data, err := json.MarshalIndent(ms.entries, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(ms.baseDir, "memory_store.json")
	return os.WriteFile(path, data, 0644)
}

func (ms *MemoryStore) loadFromDisk() {
	if ms.baseDir == "" {
		return
	}
	path := filepath.Join(ms.baseDir, "memory_store.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &ms.entries)
}

func generateMemoryID() string {
	return fmt.Sprintf("mem_%d", time.Now().UnixNano())
}

func estimateTokens(text string) int {
	return len(text) / 4
}
