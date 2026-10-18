package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/config"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/types"
	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

const (
	AutoMemDirName    = "memory"
	AutoMemEntrypoint = "MEMORY.md"
	DailyLogDir       = "logs"
	TeamMemDirName    = "team"
	SessionMemFile    = "SESSION_MEMORY.md"

	// ExtractionStateFile holds the persisted extraction cursor inside the
	// memory dir. The agent is rebuilt per user message, so without it the
	// token/tool-call thresholds would never accumulate across turns.
	ExtractionStateFile = ".extraction-state.json"

	// Token thresholds — mirror Claude Code's sessionMemoryUtils.ts defaults
	DefaultMinTokensToInit         = 10000
	DefaultMinTokensBetweenUpdate  = 5000
	DefaultToolCallsBetweenUpdates = 3

	// Section size limits — mirror Claude Code's prompts.ts
	MaxSectionLengthTokens      = 2000
	MaxTotalSessionMemoryTokens = 12000

	// Extraction concurrency
	ExtractionWaitTimeoutMs    = 15000
	ExtractionStaleThresholdMs = 60000
)

// SessionMemoryConfig mirrors Claude Code's SessionMemoryConfig.
type SessionMemoryConfig struct {
	MinimumMessageTokensToInit int `json:"minimumMessageTokensToInit"`
	MinimumTokensBetweenUpdate int `json:"minimumTokensBetweenUpdate"`
	ToolCallsBetweenUpdates    int `json:"toolCallsBetweenUpdates"`
}

// DefaultSessionMemoryConfig returns the default configuration.
func DefaultSessionMemoryConfig() SessionMemoryConfig {
	return SessionMemoryConfig{
		MinimumMessageTokensToInit: DefaultMinTokensToInit,
		MinimumTokensBetweenUpdate: DefaultMinTokensBetweenUpdate,
		ToolCallsBetweenUpdates:    DefaultToolCallsBetweenUpdates,
	}
}

// MemorySystem manages persistent memory across sessions.
// Mirrors Claude Code's SessionMemory + auto memory architecture.
type MemorySystem struct {
	baseDir    string
	autoMemDir string
	enabled    bool

	// Session memory state (mirrors sessionMemoryUtils.ts)
	mu                     sync.Mutex
	config                 SessionMemoryConfig
	initialized            bool
	tokensAtLastExtraction int
	lastSummarizedMsgUUID  string
	extractionStartedAt    int64 // unix ms, 0 = not extracting
	consecutiveFailures    int
}

// NewMemorySystem creates a new memory system.
func NewMemorySystem(homeDir string) *MemorySystem {
	if envconfig.EnvBool("TIANCAN_DISABLE_AUTO_MEMORY") {
		return &MemorySystem{enabled: false, config: DefaultSessionMemoryConfig()}
	}

	cfg := DefaultSessionMemoryConfig()
	// Allow env overrides
	if n := envconfig.EnvInt("TIANCAN_MEMORY_MIN_TOKENS_INIT", 0); n > 0 {
		cfg.MinimumMessageTokensToInit = n
	}
	if n := envconfig.EnvInt("TIANCAN_MEMORY_MIN_TOKENS_UPDATE", 0); n > 0 {
		cfg.MinimumTokensBetweenUpdate = n
	}
	if n := envconfig.EnvInt("TIANCAN_MEMORY_TOOL_CALLS_UPDATE", 0); n > 0 {
		cfg.ToolCallsBetweenUpdates = n
	}

	var autoDir string
	if override := envconfig.EnvString("TIANCAN_MEMORY_DIR"); override != "" {
		autoDir = filepath.Join(override, AutoMemDirName)
	} else {
		autoDir = filepath.Join(homeDir, config.DotDir, AutoMemDirName)
	}

	ms := &MemorySystem{
		baseDir:    filepath.Dir(autoDir),
		autoMemDir: autoDir,
		enabled:    true,
		config:     cfg,
	}
	// Restore the persisted cursor. A missing/failed read is fine: it just
	// means the thresholds start fresh for this home dir.
	_ = ms.LoadExtractionState()
	return ms
}

// LoadExtractionState restores the extraction cursor from disk.
//
// It matters because the agent — and therefore this MemorySystem — is rebuilt
// for every user message (see Service.AgentRunAdvanced): without persistence
// the token/tool-call thresholds would restart from zero each turn and never
// fire. Callers should treat an error here as harmless (fresh state).
func (m *MemorySystem) LoadExtractionState() error {
	if !m.enabled {
		return nil
	}
	data, err := os.ReadFile(m.stateFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var s extractionState
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialized = s.Initialized
	m.tokensAtLastExtraction = s.TokensAtLastExtraction
	m.lastSummarizedMsgUUID = s.LastSummarizedMsgUUID
	m.consecutiveFailures = s.ConsecutiveFailures
	return nil
}

// stateFilePath is where the extraction cursor is persisted.
func (m *MemorySystem) stateFilePath() string {
	return filepath.Join(m.autoMemDir, ExtractionStateFile)
}

// extractionState is the persisted subset of the extraction cursor.
type extractionState struct {
	Initialized            bool   `json:"initialized"`
	TokensAtLastExtraction int    `json:"tokensAtLastExtraction"`
	LastSummarizedMsgUUID  string `json:"lastSummarizedMsgUUID"`
	ConsecutiveFailures    int    `json:"consecutiveFailures"`
}

// saveStateLocked persists the cursor. Caller MUST hold m.mu: callers want the
// read-modify-write of the thresholds and their persistence to be atomic, and
// sync.Mutex is not reentrant.
func (m *MemorySystem) saveStateLocked() {
	if !m.enabled {
		return
	}
	s := extractionState{
		Initialized:            m.initialized,
		TokensAtLastExtraction: m.tokensAtLastExtraction,
		LastSummarizedMsgUUID:  m.lastSummarizedMsgUUID,
		ConsecutiveFailures:    m.consecutiveFailures,
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	// Best effort: a lost cursor costs one redundant extraction, not correctness.
	// The dir may not exist yet (EnsureDirExists runs once per agent), and a
	// failed write here would silently degrade into "trigger every turn".
	if err := os.MkdirAll(m.autoMemDir, 0755); err != nil {
		return
	}
	_ = os.WriteFile(m.stateFilePath(), data, 0644)
}

// IsEnabled returns whether auto memory is enabled.
func (m *MemorySystem) IsEnabled() bool {
	return m.enabled
}

// GetAutoMemPath returns the auto memory directory path.
func (m *MemorySystem) GetAutoMemPath() string {
	return m.autoMemDir
}

// GetAutoMemEntrypoint returns the path to MEMORY.md.
func (m *MemorySystem) GetAutoMemEntrypoint() string {
	return filepath.Join(m.autoMemDir, AutoMemEntrypoint)
}

// GetSessionMemoryPath returns the path to SESSION_MEMORY.md.
// Mirrors Claude Code's getSessionMemoryPath().
func (m *MemorySystem) GetSessionMemoryPath() string {
	return filepath.Join(m.autoMemDir, SessionMemFile)
}

// GetDailyLogPath returns the path for today's daily log.
func (m *MemorySystem) GetDailyLogPath(t time.Time) string {
	yyyy := fmt.Sprintf("%04d", t.Year())
	mm := fmt.Sprintf("%02d", int(t.Month()))
	dd := fmt.Sprintf("%02d", t.Day())
	return filepath.Join(m.autoMemDir, DailyLogDir, yyyy, mm, fmt.Sprintf("%s-%s-%s.md", yyyy, mm, dd))
}

// EnsureDirExists creates the memory directory if it doesn't exist.
func (m *MemorySystem) EnsureDirExists() error {
	if !m.enabled {
		return nil
	}
	return os.MkdirAll(m.autoMemDir, 0755)
}

// AppendToDailyLog appends a timestamped entry to today's daily log.
func (m *MemorySystem) AppendToDailyLog(entry string) error {
	if !m.enabled {
		return nil
	}
	logPath := m.GetDailyLogPath(time.Now())
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return err
	}

	timestamp := time.Now().Format("15:04:05")
	line := fmt.Sprintf("- **%s** %s\n", timestamp, entry)

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(line)
	return err
}

// --- Session Memory Template (mirrors Claude Code's prompts.ts) ---

const DefaultSessionMemoryTemplate = `# Session Title
_A short and distinctive 5-10 word descriptive title for the session. Super info dense, no filler_

# Current State
_What is actively being worked on right now? Pending tasks not yet completed. Immediate next steps._

# Task specification
_What did the user ask to build? Any design decisions or other explanatory context_

# Files and Functions
_What are the important files? In short, what do they contain and why are they relevant?_

# Workflow
_What bash commands are usually run and in what order? How to interpret their output if not obvious?_

# Errors & Corrections
_Errors encountered and how they were fixed. What did the user correct? What approaches failed and should not be tried again?_

# Codebase and System Documentation
_What are the important system components? How do they work/fit together?_

# Learnings
_What has worked well? What has not? What to avoid? Do not duplicate items from other sections_

# Key results
_If the user asked a specific output such as an answer to a question, a table, or other document, repeat the exact result here_

# Worklog
_Step by step, what was attempted, done? Very terse summary for each step_
`

// LoadSessionMemoryTemplate loads the session memory template, trying custom first.
// Mirrors Claude Code's loadSessionMemoryTemplate().
func (m *MemorySystem) LoadSessionMemoryTemplate() string {
	customPath := filepath.Join(m.autoMemDir, "config", "template.md")
	if data, err := os.ReadFile(customPath); err == nil {
		return string(data)
	}
	return DefaultSessionMemoryTemplate
}

// SetupSessionMemoryFile creates the session memory file with the template if it doesn't exist.
// Mirrors Claude Code's setupSessionMemoryFile().
func (m *MemorySystem) SetupSessionMemoryFile() error {
	if !m.enabled {
		return nil
	}
	if err := m.EnsureDirExists(); err != nil {
		return err
	}
	path := m.GetSessionMemoryPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		template := m.LoadSessionMemoryTemplate()
		return os.WriteFile(path, []byte(template), 0644)
	}
	return nil
}

// GetSessionMemoryContent reads the current session memory file.
// Mirrors Claude Code's getSessionMemoryContent().
func (m *MemorySystem) GetSessionMemoryContent() (string, error) {
	if !m.enabled {
		return "", nil
	}
	data, err := os.ReadFile(m.GetSessionMemoryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// IsSessionMemoryEmpty checks if the session memory is just the template (no real content).
// Mirrors Claude Code's isSessionMemoryEmpty().
func (m *MemorySystem) IsSessionMemoryEmpty(content string) bool {
	template := m.LoadSessionMemoryTemplate()
	return strings.TrimSpace(content) == strings.TrimSpace(template)
}

// --- Token-based extraction thresholds (mirrors sessionMemoryUtils.ts) ---

// ShouldExtractMemory determines whether session memory should be extracted.
// Mirrors Claude Code's shouldExtractMemory().
func (m *MemorySystem) ShouldExtractMemory(messages []types.Message) bool {
	if !m.enabled {
		return false
	}

	currentTokenCount := EstimateTokens(messages)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check initialization threshold
	justInitialized := false
	if !m.initialized {
		if currentTokenCount < m.config.MinimumMessageTokensToInit {
			return false
		}
		m.initialized = true
		justInitialized = true
	}

	// Check token growth threshold since last extraction
	tokensSinceLastExtraction := currentTokenCount - m.tokensAtLastExtraction
	hasMetTokenThreshold := tokensSinceLastExtraction >= m.config.MinimumTokensBetweenUpdate

	// Check tool call threshold
	toolCallsSinceLastUpdate := countToolCallsSince(messages, m.lastSummarizedMsgUUID)
	hasMetToolCallThreshold := toolCallsSinceLastUpdate >= m.config.ToolCallsBetweenUpdates

	// Check if last assistant turn has no tool calls (safe to extract)
	hasToolCallsInLastTurn := hasToolCallsInLastAssistantTurn(messages)

	// Trigger extraction when:
	// 1. Both thresholds met (tokens AND tool calls), OR
	// 2. No tool calls in last turn AND token threshold met
	// Token threshold is ALWAYS required to prevent excessive extractions.
	shouldExtract := (hasMetTokenThreshold && hasMetToolCallThreshold) ||
		(hasMetTokenThreshold && !hasToolCallsInLastTurn)

	if shouldExtract {
		if len(messages) > 0 {
			m.lastSummarizedMsgUUID = messages[len(messages)-1].UUID
		}
		m.tokensAtLastExtraction = currentTokenCount
		m.saveStateLocked() // caller holds the lock; see saveStateLocked
		return true
	}

	// Nothing changed but the init flip — only that one is worth persisting,
	// so a conversation does not hit the disk on every single turn.
	if justInitialized {
		m.saveStateLocked()
	}
	return false
}

// MarkExtractionStarted marks that a memory extraction is in progress.
// Mirrors Claude Code's markExtractionStarted().
func (m *MemorySystem) MarkExtractionStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extractionStartedAt = time.Now().UnixMilli()
}

// MarkExtractionCompleted marks that a memory extraction has completed.
// Mirrors Claude Code's markExtractionCompleted().
func (m *MemorySystem) MarkExtractionCompleted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extractionStartedAt = 0
	m.consecutiveFailures = 0
	m.saveStateLocked()
}

// MarkExtractionFailed records a failed extraction.
func (m *MemorySystem) MarkExtractionFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consecutiveFailures++
	m.saveStateLocked()
}

// WaitForExtraction waits for any in-progress extraction to complete.
// Mirrors Claude Code's waitForSessionMemoryExtraction().
func (m *MemorySystem) WaitForExtraction() {
	start := time.Now()
	for {
		m.mu.Lock()
		startedAt := m.extractionStartedAt
		m.mu.Unlock()

		if startedAt == 0 {
			return
		}
		age := time.Now().UnixMilli() - startedAt
		if age > ExtractionStaleThresholdMs {
			return
		}
		if time.Since(start).Milliseconds() > ExtractionWaitTimeoutMs {
			return
		}
		time.Sleep(1 * time.Second)
	}
}

// --- Memory prompt building ---

// LoadMemoryPrompt builds the memory section for the system prompt.
// Now includes both auto-memory (daily log) and session memory.
func (m *MemorySystem) LoadMemoryPrompt() string {
	if !m.enabled {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# auto memory\n\n")
	sb.WriteString(fmt.Sprintf("You have a persistent, file-based memory system found at: `%s`\n\n", m.autoMemDir))
	sb.WriteString("As you work, record anything worth remembering by **appending** to today's daily log file:\n\n")
	sb.WriteString(fmt.Sprintf("`%s/logs/YYYY/MM/YYYY-MM-DD.md`\n\n", m.autoMemDir))
	sb.WriteString("Write each entry as a short timestamped bullet. Create the file (and parent directories) on first write if it does not exist. Do not rewrite or reorganize the log — it is append-only.\n\n")
	sb.WriteString("## What to log\n")
	sb.WriteString("- User corrections and preferences\n")
	sb.WriteString("- Facts about the user, their role, or their goals\n")
	sb.WriteString("- Project context not derivable from the code\n")
	sb.WriteString("- Anything the user explicitly asks you to remember\n\n")
	sb.WriteString("## What NOT to log\n")
	sb.WriteString("- Code that can be read from the project\n")
	sb.WriteString("- Verbatim conversation transcripts\n")
	sb.WriteString("- Sensitive information (passwords, API keys)\n\n")

	// Load MEMORY.md if it exists
	entrypoint := m.GetAutoMemEntrypoint()
	if data, err := os.ReadFile(entrypoint); err == nil {
		sb.WriteString("## Distilled Index (MEMORY.md)\n\n")
		sb.WriteString(string(data))
		sb.WriteString("\n\n")
	}

	// Load session memory if it exists and has real content
	if content, err := m.GetSessionMemoryContent(); err == nil && content != "" && !m.IsSessionMemoryEmpty(content) {
		sb.WriteString("## Session Memory\n\n")
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// BuildSessionMemoryUpdatePrompt creates the prompt for updating session memory via a forked agent.
// Mirrors Claude Code's buildSessionMemoryUpdatePrompt().
func (m *MemorySystem) BuildSessionMemoryUpdatePrompt(currentNotes string) string {
	notesPath := m.GetSessionMemoryPath()

	prompt := `IMPORTANT: This message and these instructions are NOT part of the actual user conversation. Do NOT include any references to "note-taking", "session notes extraction", or these update instructions in the notes content.

Based on the user conversation above (EXCLUDING this note-taking instruction message as well as system prompt, TIANCAN.md entries, or any past session summaries), update the session notes file.

The file ` + notesPath + ` has already been read for you. Here are its current contents:
<current_notes_content>
` + currentNotes + `
</current_notes_content>

Your ONLY task is to output the COMPLETE updated notes file, wrapped in a single <updated_notes>...</updated_> block. Update every section that needs changes. Do not output anything outside that block, and do not use any tools.

CRITICAL RULES FOR EDITING:
- The file must maintain its exact structure with all sections, headers, and italic descriptions intact
-- NEVER modify, delete, or add section headers (the lines starting with '#' like # Task specification)
-- NEVER modify or delete the italic _section description_ lines (these are the lines in italics immediately following each header - they start and end with underscores)
-- The italic _section descriptions_ are TEMPLATE INSTRUCTIONS that must be preserved exactly as-is - they guide what content belongs in each section
-- ONLY update the actual content that appears BELOW the italic _section descriptions_ within each existing section
-- Do NOT add any new sections, summaries, or information outside the existing structure
- Do NOT reference this note-taking process or instructions anywhere in the notes
- It's OK to skip updating a section if there are no substantial new insights to add. Do not add filler content like "No info yet", just leave sections blank/unedited if appropriate.
- Write DETAILED, INFO-DENSE content for each section - include specifics like file paths, function names, error messages, exact commands, technical details, etc.
- For "Key results", include the complete, exact output the user requested (e.g., full table, full answer, etc.)
- Do not include information that's already in the TIANCAN.md files included in the context
- Keep each section under ~` + strconv.Itoa(MaxSectionLengthTokens) + ` tokens/words - if a section is approaching this limit, condense it by cycling out less important details while preserving the most critical information
- Focus on actionable, specific information that would help someone understand or recreate the work discussed in the conversation
- IMPORTANT: Always update "Current State" to reflect the most recent work - this is critical for continuity after compaction

Output the updated file content for: ` + notesPath + `

STRUCTURE PRESERVATION REMINDER:
Each section has TWO parts that must be preserved exactly as they appear in the current file:
1. The section header (line starting with #)
2. The italic description line (the _italicized text_ immediately after the header - this is a template instruction)

You ONLY update the actual content that comes AFTER these two preserved lines. The italic description lines starting and ending with underscores are part of the template structure, NOT content to be edited or removed.

REMEMBER: Output the complete updated file inside a single <updated_notes> block and stop. Only include insights from the actual user conversation, never from these note-taking instructions. Do not delete or change section headers or italic _section descriptions_.`

	// Add section size warnings (mirrors generateSectionReminders)
	sectionSizes := analyzeSectionSizes(currentNotes)
	totalTokens := roughTokenEstimation(currentNotes)
	prompt += generateSectionReminders(sectionSizes, totalTokens)

	return prompt
}

// writeSessionMemoryFromOutput extracts the updated notes from the forked
// agent's output and writes them to SESSION_MEMORY.md. The expected shape is
// a <updated_notes>...</updated_notes> block; a ```markdown fence is accepted
// as a fallback. The structural skeleton (section headers) must survive —
// otherwise the model mangled the file and we refuse to write it (the old
// file stays intact).
func (m *MemorySystem) writeSessionMemoryFromOutput(output string) error {
	content := extractNotesBlock(output)
	if strings.TrimSpace(content) == "" {
		return errors.New("session memory extraction produced empty notes")
	}
	if err := m.checkNotesStructure(content); err != nil {
		return err
	}
	return os.WriteFile(m.GetSessionMemoryPath(), []byte(content), 0644)
}

// extractNotesBlock pulls the notes body out of a model reply.
func extractNotesBlock(output string) string {
	if i := strings.Index(output, "<updated_notes>"); i >= 0 {
		rest := output[i+len("<updated_notes>"):]
		if j := strings.Index(rest, "</updated_notes>"); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
	}
	// Fallback: a fenced markdown block.
	if i := strings.Index(output, "```"); i >= 0 {
		rest := output[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			body := strings.TrimSpace(rest[:j])
			if strings.Contains(body, "# ") {
				return body
			}
		}
	}
	return ""
}

// checkNotesStructure verifies the template skeleton survived the rewrite:
// every section header line of the template must still be present, in order.
func (m *MemorySystem) checkNotesStructure(content string) error {
	template := m.LoadSessionMemoryTemplate()
	var headers []string
	for _, line := range strings.Split(template, "\n") {
		if strings.HasPrefix(line, "# ") {
			headers = append(headers, line)
		}
	}
	if len(headers) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	next := 0
	for _, line := range lines {
		if next < len(headers) && line == headers[next] {
			next++
		}
	}
	if next < len(headers) {
		return fmt.Errorf("extracted notes lost section header %q, refusing to overwrite", headers[next])
	}
	return nil
}

// --- Section size analysis (mirrors Claude Code's prompts.ts) ---

func analyzeSectionSizes(content string) map[string]int {
	sections := make(map[string]int)
	lines := strings.Split(content, "\n")
	currentSection := ""
	var currentContent []string

	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			if currentSection != "" && len(currentContent) > 0 {
				sectionContent := strings.Join(currentContent, "\n")
				sections[currentSection] = roughTokenEstimation(sectionContent)
			}
			currentSection = line
			currentContent = nil
		} else {
			currentContent = append(currentContent, line)
		}
	}
	if currentSection != "" && len(currentContent) > 0 {
		sectionContent := strings.Join(currentContent, "\n")
		sections[currentSection] = roughTokenEstimation(sectionContent)
	}
	return sections
}

func generateSectionReminders(sectionSizes map[string]int, totalTokens int) string {
	overBudget := totalTokens > MaxTotalSessionMemoryTokens

	var oversizedSections []string
	for section, tokens := range sectionSizes {
		if tokens > MaxSectionLengthTokens {
			oversizedSections = append(oversizedSections, fmt.Sprintf("- %q is ~%d tokens (limit: %d)", section, tokens, MaxSectionLengthTokens))
		}
	}

	if len(oversizedSections) == 0 && !overBudget {
		return ""
	}

	var parts []string
	if overBudget {
		parts = append(parts, fmt.Sprintf("\n\nCRITICAL: The session memory file is currently ~%d tokens, which exceeds the maximum of %d tokens. You MUST condense the file to fit within this budget. Aggressively shorten oversized sections by removing less important details, merging related items, and summarizing older entries. Prioritize keeping \"Current State\" and \"Errors & Corrections\" accurate and detailed.", totalTokens, MaxTotalSessionMemoryTokens))
	}
	if len(oversizedSections) > 0 {
		parts = append(parts, fmt.Sprintf("\n\n%s:\n%s",
			func() string {
				if overBudget {
					return "Oversized sections to condense"
				}
				return "IMPORTANT: The following sections exceed the per-section limit and MUST be condensed"
			}(),
			strings.Join(oversizedSections, "\n")))
	}
	return strings.Join(parts, "")
}

// TruncateSessionMemoryForCompact truncates oversized sections for compact messages.
// Mirrors Claude Code's truncateSessionMemoryForCompact().
func (m *MemorySystem) TruncateSessionMemoryForCompact(content string) (string, bool) {
	maxCharsPerSection := MaxSectionLengthTokens * 4 // rough estimation: 4 chars/token
	lines := strings.Split(content, "\n")
	var outputLines []string
	var currentSectionLines []string
	currentSectionHeader := ""
	wasTruncated := false

	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			flushed, truncated := flushSessionSection(currentSectionHeader, currentSectionLines, maxCharsPerSection)
			outputLines = append(outputLines, flushed...)
			wasTruncated = wasTruncated || truncated
			currentSectionHeader = line
			currentSectionLines = nil
		} else {
			currentSectionLines = append(currentSectionLines, line)
		}
	}
	flushed, truncated := flushSessionSection(currentSectionHeader, currentSectionLines, maxCharsPerSection)
	outputLines = append(outputLines, flushed...)
	wasTruncated = wasTruncated || truncated

	return strings.Join(outputLines, "\n"), wasTruncated
}

func flushSessionSection(header string, lines []string, maxChars int) ([]string, bool) {
	if header == "" {
		return lines, false
	}
	content := strings.Join(lines, "\n")
	if len(content) <= maxChars {
		return append([]string{header}, lines...), false
	}
	charCount := 0
	var kept []string
	kept = append(kept, header)
	for _, line := range lines {
		if charCount+len(line)+1 > maxChars {
			break
		}
		kept = append(kept, line)
		charCount += len(line) + 1
	}
	kept = append(kept, "\n[... section truncated for length ...]")
	return kept, true
}

// --- Memory extraction (mirrors Claude Code's extractSessionMemory) ---

// ExtractMemories extracts memories from conversation messages.
// Uses token-based thresholds and forked-agent extraction pattern.
// ExtractMemories appends explicit "remember this" requests to the daily log.
// Only messages AFTER the last summarized one are scanned: the message list is
// the whole conversation history, so a full scan would re-append every old
// "remember" line on every turn end.
func (m *MemorySystem) ExtractMemories(messages []types.Message) error {
	if !m.enabled || len(messages) == 0 {
		return nil
	}

	m.mu.Lock()
	since := m.lastSummarizedMsgUUID
	m.mu.Unlock()

	start := 0
	if since != "" {
		for i, msg := range messages {
			if msg.UUID == since {
				start = i + 1
				break
			}
		}
	}

	var lastUUID string
	for _, msg := range messages[start:] {
		if msg.UUID != "" {
			lastUUID = msg.UUID
		}
		if msg.Role != types.RoleUser {
			continue
		}
		content := strings.ToLower(msg.Content)
		if strings.Contains(content, "remember") ||
			strings.Contains(content, "记住") ||
			strings.Contains(content, "don't forget") ||
			strings.Contains(content, "别忘了") {
			if err := m.AppendToDailyLog(msg.Content); err != nil {
				return err
			}
		}
	}

	if lastUUID != "" {
		m.mu.Lock()
		m.lastSummarizedMsgUUID = lastUUID
		m.saveStateLocked()
		m.mu.Unlock()
	}
	return nil
}

// ExtractSessionMemory performs the full session memory extraction using a forked agent.
// Mirrors Claude Code's extractSessionMemory().
// The chatFn is used to run the forked subagent that produces the updated
// SESSION_MEMORY.md content; the result is written to disk by us (the chat
// backend is a plain chat function without file tools, so "edit the file
// yourself" style prompts would silently no-op).
func (m *MemorySystem) ExtractSessionMemory(messages []types.Message, chatFn types.ChatFunc) error {
	if !m.enabled {
		return nil
	}

	m.mu.Lock()
	if m.consecutiveFailures >= 3 {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// Ensure session memory file exists
	if err := m.SetupSessionMemoryFile(); err != nil {
		return err
	}

	// Read current notes
	currentNotes, err := m.GetSessionMemoryContent()
	if err != nil {
		return err
	}

	// Build update prompt
	updatePrompt := m.BuildSessionMemoryUpdatePrompt(currentNotes)

	// Mark extraction started
	m.MarkExtractionStarted()

	// Run the forked agent to produce the updated notes
	output, err := chatFn("", []types.Message{
		{Role: types.RoleUser, Content: updatePrompt, Timestamp: time.Now()},
	})
	if err != nil {
		m.MarkExtractionFailed()
		return fmt.Errorf("session memory extraction failed: %w", err)
	}
	if err := m.writeSessionMemoryFromOutput(output); err != nil {
		m.MarkExtractionFailed()
		return err
	}

	m.MarkExtractionCompleted()
	return nil
}

// ManuallyExtractSessionMemory forces a session memory extraction regardless of thresholds.
// Mirrors Claude Code's manuallyExtractSessionMemory().
func (m *MemorySystem) ManuallyExtractSessionMemory(messages []types.Message, chatFn types.ChatFunc) error {
	if !m.enabled {
		return fmt.Errorf("memory system is disabled")
	}

	// Two manual triggers (or one overlapping the automatic one) must not
	// rewrite the same file at once — wait for any in-flight extraction.
	m.WaitForExtraction()

	if err := m.SetupSessionMemoryFile(); err != nil {
		return err
	}

	currentNotes, err := m.GetSessionMemoryContent()
	if err != nil {
		return err
	}

	updatePrompt := m.BuildSessionMemoryUpdatePrompt(currentNotes)

	// Mark extraction started
	m.MarkExtractionStarted()
	output, err := chatFn("", []types.Message{
		{Role: types.RoleUser, Content: updatePrompt, Timestamp: time.Now()},
	})
	if err != nil {
		m.MarkExtractionFailed()
		return fmt.Errorf("manual session memory extraction failed: %w", err)
	}
	if err := m.writeSessionMemoryFromOutput(output); err != nil {
		m.MarkExtractionFailed()
		return err
	}

	m.MarkExtractionCompleted()

	// Update tracking
	m.mu.Lock()
	if len(messages) > 0 {
		m.lastSummarizedMsgUUID = messages[len(messages)-1].UUID
	}
	m.tokensAtLastExtraction = EstimateTokens(messages)
	m.saveStateLocked()
	m.mu.Unlock()

	return nil
}

// --- Session memory compaction integration ---

// GetSessionMemoryForCompact returns session memory content for compaction,
// truncated if needed to avoid consuming the entire post-compact token budget.
// Mirrors how Claude Code integrates session memory into compact messages.
// GetSessionMemoryForCompact returns notes for compaction, already truncated so
// a runaway notes file cannot eat the whole post-compact token budget.
//
// NOTE the second result is wasTruncated — it reports whether truncation kicked
// in, NOT whether there is anything usable. Callers that need "is there
// content" should use SessionMemoryForCompact instead.
func (m *MemorySystem) GetSessionMemoryForCompact() (string, bool) {
	if !m.enabled {
		return "", false
	}
	content, err := m.GetSessionMemoryContent()
	if err != nil || content == "" || m.IsSessionMemoryEmpty(content) {
		return "", false
	}
	truncated, wasTruncated := m.TruncateSessionMemoryForCompact(content)
	return truncated, wasTruncated
}

// SessionMemoryForCompact returns the notes to carry through compaction and
// reports whether there is anything worth injecting (i.e. the notes have real
// content beyond the untouched template).
func (m *MemorySystem) SessionMemoryForCompact() (string, bool) {
	if !m.enabled {
		return "", false
	}
	content, err := m.GetSessionMemoryContent()
	if err != nil || content == "" || m.IsSessionMemoryEmpty(content) {
		return "", false
	}
	notes, _ := m.TruncateSessionMemoryForCompact(content)
	return notes, true
}

// --- Helper functions ---

// EstimateTokens estimates the token count for a slice of messages.
func EstimateTokens(messages []types.Message) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	return total
}

func estimateMessageTokens(m types.Message) int {
	return len(m.Content)/4 + 50 // overhead for role, metadata
}

func roughTokenEstimation(text string) int {
	return len(text) / 4
}

func countToolCallsSince(messages []types.Message, sinceUUID string) int {
	counting := sinceUUID == ""
	count := 0
	for _, msg := range messages {
		if !counting && msg.UUID == sinceUUID {
			counting = true
			continue
		}
		if counting && msg.Role == types.RoleTool {
			count++
		}
	}
	return count
}

func hasToolCallsInLastAssistantTurn(messages []types.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleAssistant {
			return messages[i].ToolName != ""
		}
	}
	return false
}

// --- Auth persistence ---

// SaveAuth saves authentication state for a provider.
func (m *MemorySystem) SaveAuth(providerID string, auth map[string]string) error {
	if !m.enabled {
		return nil
	}
	if err := m.EnsureDirExists(); err != nil {
		return err
	}
	authPath := filepath.Join(m.autoMemDir, "auth", providerID+".json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(authPath, data, 0600)
}

// LoadAuth loads authentication state for a provider.
func (m *MemorySystem) LoadAuth(providerID string) (map[string]string, error) {
	if !m.enabled {
		return nil, fmt.Errorf("memory disabled")
	}
	authPath := filepath.Join(m.autoMemDir, "auth", providerID+".json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		return nil, err
	}
	var auth map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return auth, nil
}
