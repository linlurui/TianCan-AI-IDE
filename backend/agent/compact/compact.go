package compact

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

const (
	// AutoCompactBufferTokens reserves tokens before triggering auto-compact.
	// Mirrors Claude Code's AUTOCOMPACT_BUFFER_TOKENS.
	AutoCompactBufferTokens = 13000
	// WarningThresholdBufferTokens warns the user about context usage.
	WarningThresholdBufferTokens = 20000
	// MaxConsecutiveFailures stops retrying after this many failures.
	MaxConsecutiveFailures = 3
	// DefaultContextWindow is the default context window size.
	DefaultContextWindow = 200000
	// MaxOutputTokensForSummary reserves tokens for the summary output.
	MaxOutputTokensForSummary = 20000

	// Post-compact token budgets — mirrors Claude Code's compact.ts
	PostCompactTokenBudget       = 50000
	PostCompactMaxFilesToRestore = 5
	PostCompactMaxTokensPerFile  = 5000

	// PTL retry — mirrors Claude Code's MAX_COMPACT_STREAMING_RETRIES
	MaxPTLRetries = 2
)

// CompactMode distinguishes manual vs auto compaction.
type CompactMode string

const (
	CompactAuto   CompactMode = "auto"
	CompactManual CompactMode = "manual"
)

// PartialCompactDirection defines the direction for partial compaction.
// Mirrors Claude Code's PartialCompactDirection.
type PartialCompactDirection string

const (
	CompactFrom PartialCompactDirection = "from"  // summarize messages after pivot
	CompactUpTo PartialCompactDirection = "up_to" // summarize messages before pivot
)

// CompactionState tracks compaction state across turns.
type CompactionState struct {
	Compacted           bool   `json:"compacted"`
	TurnCounter         int    `json:"turnCounter"`
	TurnID              string `json:"turnId"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	LastCompactTurnID   string `json:"lastCompactTurnId,omitempty"`
}

// CompactService handles context compression/distillation.
// Mirrors Claude Code's compact.ts + autoCompact.ts architecture.
type CompactService struct {
	contextWindow int
	enabled       bool
	state         CompactionState
}

// NewCompactService creates a new compaction service.
func NewCompactService() *CompactService {
	enabled := os.Getenv("TIANCAN_DISABLE_COMPACT") != "1"
	contextWindow := DefaultContextWindow
	if cw := os.Getenv("TIANCAN_CONTEXT_WINDOW"); cw != "" {
		if v, err := strconv.Atoi(cw); err == nil && v > 0 {
			contextWindow = v
		}
	}
	// Allow percentage override — mirrors Claude Code's CLAUDE_AUTOCOMPACT_PCT_OVERRIDE
	if pct := os.Getenv("TIANCAN_AUTOCOMPACT_PCT_OVERRIDE"); pct != "" {
		if v, err := strconv.ParseFloat(pct, 64); err == nil && v > 0 && v <= 100 {
			pctThreshold := int(float64(contextWindow-MaxOutputTokensForSummary) * v / 100)
			normalThreshold := contextWindow - MaxOutputTokensForSummary - AutoCompactBufferTokens
			if pctThreshold < normalThreshold {
				contextWindow = pctThreshold + MaxOutputTokensForSummary + AutoCompactBufferTokens
			}
		}
	}
	return &CompactService{
		contextWindow: contextWindow,
		enabled:       enabled,
	}
}

// IsEnabled returns whether compaction is enabled.
func (c *CompactService) IsEnabled() bool {
	return c.enabled
}

// GetAutoCompactThreshold returns the token count at which auto-compact triggers.
// Mirrors Claude Code's getAutoCompactThreshold().
func (c *CompactService) GetAutoCompactThreshold() int {
	effectiveWindow := c.contextWindow - MaxOutputTokensForSummary
	return effectiveWindow - AutoCompactBufferTokens
}

// ShouldAutoCompact checks if the message list exceeds the auto-compact threshold.
func (c *CompactService) ShouldAutoCompact(messages []types.Message) bool {
	if !c.enabled {
		return false
	}
	if c.state.ConsecutiveFailures >= MaxConsecutiveFailures {
		return false
	}

	tokenCount := EstimateTokens(messages)
	return tokenCount >= c.GetAutoCompactThreshold()
}

// CompactConversation compresses the conversation by summarizing older messages.
// Mirrors Claude Code's compactConversation().
func (c *CompactService) CompactConversation(messages []types.Message, chatFn ChatFunc) (*types.CompactionResult, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("not enough messages to compact")
	}

	preCompactTokens := EstimateTokens(messages)

	// Split messages: summarize older ones, keep recent ones
	keepCount := c.calculateMessagesToKeep(messages)
	if keepCount >= len(messages) {
		return nil, fmt.Errorf("nothing to compact")
	}

	toSummarize := messages[:len(messages)-keepCount]
	toKeep := messages[len(messages)-keepCount:]

	// Build summary prompt using Claude Code's detailed analysis+summary structure
	summaryPrompt := GetCompactPrompt("")

	// Serialize messages for the prompt
	serializedMessages := serializeMessagesForCompact(toSummarize)
	fullPrompt := summaryPrompt + "\n\n" + serializedMessages

	// Use the chat function to generate a summary
	summary, err := chatFn(fullPrompt)
	if err != nil {
		c.state.ConsecutiveFailures++
		return nil, fmt.Errorf("compact: summary generation failed: %w", err)
	}

	// Format the summary — strip <analysis>, format <summary> tags
	formattedSummary := FormatCompactSummary(summary)

	// Build compact boundary marker
	boundary := fmt.Sprintf("--- Compact Boundary (auto, pre=%d tokens, time=%s) ---",
		preCompactTokens, time.Now().Format(time.RFC3339))

	// Build the user-facing summary message — mirrors Claude Code's getCompactUserSummaryMessage()
	summaryContent := fmt.Sprintf(
		"This session is being continued from a previous conversation that ran out of context. "+
			"The summary below covers the earlier portion of the conversation.\n\n%s", formattedSummary)

	summaryMsg := types.Message{
		Role:             types.RoleUser,
		Content:          summaryContent,
		IsCompactSummary: true,
		Timestamp:        time.Now(),
	}

	postCompactTokens := EstimateTokens(append([]types.Message{summaryMsg}, toKeep...))

	result := &types.CompactionResult{
		BoundaryMarker:        boundary,
		SummaryMessages:       []types.Message{summaryMsg},
		PreCompactTokenCount:  preCompactTokens,
		PostCompactTokenCount: postCompactTokens,
	}

	c.state.Compacted = true
	c.state.ConsecutiveFailures = 0

	return result, nil
}

// CompactConversationWithSessionMemory performs compaction with session memory integration.
// Mirrors how Claude Code injects session memory into post-compact messages.
func (c *CompactService) CompactConversationWithSessionMemory(
	messages []types.Message,
	chatFn ChatFunc,
	sessionMemoryContent string,
) (*types.CompactionResult, error) {
	result, err := c.CompactConversation(messages, chatFn)
	if err != nil {
		return nil, err
	}

	// Inject session memory into the summary messages if available
	if sessionMemoryContent != "" {
		memMsg := types.Message{
			Role:      types.RoleUser,
			Content:   fmt.Sprintf("Session memory from previous context:\n\n%s", sessionMemoryContent),
			Timestamp: time.Now(),
		}
		result.SummaryMessages = append(result.SummaryMessages, memMsg)
		result.PostCompactTokenCount = EstimateTokens(append(result.SummaryMessages, messages[len(messages)-c.calculateMessagesToKeep(messages):]...))
	}

	return result, nil
}

// PartialCompactConversation performs a partial compaction around a pivot index.
// Mirrors Claude Code's partialCompactConversation().
func (c *CompactService) PartialCompactConversation(
	allMessages []types.Message,
	pivotIndex int,
	direction PartialCompactDirection,
	chatFn ChatFunc,
	userFeedback string,
) (*types.CompactionResult, error) {
	var toSummarize, toKeep []types.Message

	if direction == CompactUpTo {
		toSummarize = allMessages[:pivotIndex]
		toKeep = filterCompactBoundaries(allMessages[pivotIndex:])
	} else {
		toSummarize = allMessages[pivotIndex:]
		toKeep = allMessages[:pivotIndex]
	}

	if len(toSummarize) == 0 {
		return nil, fmt.Errorf("nothing to summarize")
	}

	preCompactTokens := EstimateTokens(allMessages)

	// Build partial compact prompt
	var customInstructions string
	if userFeedback != "" {
		customInstructions = fmt.Sprintf("User context: %s", userFeedback)
	}
	prompt := GetPartialCompactPrompt(customInstructions, direction)
	serializedMessages := serializeMessagesForCompact(toSummarize)
	fullPrompt := prompt + "\n\n" + serializedMessages

	summary, err := chatFn(fullPrompt)
	if err != nil {
		c.state.ConsecutiveFailures++
		return nil, fmt.Errorf("partial compact failed: %w", err)
	}

	formattedSummary := FormatCompactSummary(summary)

	boundary := fmt.Sprintf("--- Compact Boundary (partial/%s, pre=%d tokens, time=%s) ---",
		direction, preCompactTokens, time.Now().Format(time.RFC3339))

	summaryMsg := types.Message{
		Role:             types.RoleUser,
		Content:          formattedSummary,
		IsCompactSummary: true,
		Timestamp:        time.Now(),
	}

	var resultMessages []types.Message
	if direction == CompactUpTo {
		resultMessages = append([]types.Message{summaryMsg}, toKeep...)
	} else {
		resultMessages = append(toKeep, summaryMsg)
	}

	postCompactTokens := EstimateTokens(resultMessages)

	return &types.CompactionResult{
		BoundaryMarker:        boundary,
		SummaryMessages:       []types.Message{summaryMsg},
		PreCompactTokenCount:  preCompactTokens,
		PostCompactTokenCount: postCompactTokens,
	}, nil
}

// calculateMessagesToKeep determines how many recent messages to preserve.
func (c *CompactService) calculateMessagesToKeep(messages []types.Message) int {
	minTokens := 10000
	minMessages := 4

	totalTokens := 0
	textMsgCount := 0
	keepCount := 0

	for i := len(messages) - 1; i >= 0; i-- {
		msgTokens := estimateMessageTokens(messages[i])
		totalTokens += msgTokens
		if messages[i].Role == types.RoleUser || messages[i].Role == types.RoleAssistant {
			textMsgCount++
		}
		keepCount++

		if totalTokens >= minTokens && textMsgCount >= minMessages {
			break
		}
		if totalTokens >= c.contextWindow/2 {
			break
		}
	}

	return keepCount
}

// --- Compact prompts (mirrors Claude Code's prompt.ts) ---

const noToolsPreamble = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.
- You already have all the context you need in the conversation above.
- Tool calls will be REJECTED and will waste your only turn — you will fail the task.
- Your entire response must be plain text: an <analysis> block followed by a <summary> block.

`

const detailedAnalysisInstruction = `Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.`

const baseCompactPrompt = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

` + detailedAnalysisInstruction + `

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Pay special attention to the most recent messages and include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List all errors that you ran into, and how you fixed them. Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results. These are critical for understanding the users' feedback and changing intent.
7. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
8. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include file names and code snippets where applicable.
9. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's most recent explicit requests, and the task you were working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the users request. Do not start on tangential requests or really old requests that were already completed without confirming with the user first.
                       If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]
   - [...]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Summary of the changes made to this file, if any]
      - [Important Code Snippet]
   - [File Name 2]
      - [Important Code Snippet]
   - [...]

4. Errors and fixes:
    - [Detailed description of error 1]:
      - [How you fixed the error]
      - [User feedback on the error if any]
    - [...]

5. Problem Solving:
   [Description of solved problems and ongoing troubleshooting]

6. All user messages: 
    - [Detailed non tool use user message]
    - [...]

7. Pending Tasks:
   - [Task 1]
   - [Task 2]
   - [...]

8. Current Work:
   [Precise description of current work]

9. Optional Next Step:
   [Optional Next step to take]

</summary>
</example>

Please provide your summary based on the conversation so far, following this structure and ensuring precision and thoroughness in your response. 
`

const partialCompactPromptFrom = `Your task is to create a detailed summary of the RECENT portion of the conversation — the messages that follow earlier retained context. The earlier messages are being kept intact and do NOT need to be summarized. Focus your summary on what was discussed, learned, and accomplished in the recent messages only.

` + detailedAnalysisInstruction + `

Your summary should include the following sections:

1. Primary Request and Intent: Capture the user's explicit requests and intents from the recent messages
2. Key Technical Concepts: List important technical concepts, technologies, and frameworks discussed recently.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List errors encountered and how they were fixed.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages from the recent portion that are not tool results.
7. Pending Tasks: Outline any pending tasks from the recent messages.
8. Current Work: Describe precisely what was being worked on immediately before this summary request.
9. Optional Next Step: List the next step related to the most recent work. Include direct quotes from the most recent conversation.

Please provide your summary based on the RECENT messages only (after the retained earlier context), following this structure and ensuring precision and thoroughness in your response.
`

const partialCompactPromptUpTo = `Your task is to create a detailed summary of this conversation. This summary will be placed at the start of a continuing session; newer messages that build on this context will follow after your summary (you do not see them here). Summarize thoroughly so that someone reading only your summary and then the newer messages can fully understand what happened and continue the work.

` + detailedAnalysisInstruction + `

Your summary should include the following sections:

1. Primary Request and Intent: Capture the user's explicit requests and intents in detail
2. Key Technical Concepts: List important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List errors encountered and how they were fixed.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results.
7. Pending Tasks: Outline any pending tasks.
8. Work Completed: Describe what was accomplished by the end of this portion.
9. Context for Continuing Work: Summarize any context, decisions, or state that would be needed to understand and continue the work in subsequent messages.

Please provide your summary following this structure, ensuring precision and thoroughness in your response.
`

const noToolsTrailer = "\n\nREMINDER: Do NOT call any tools. Respond with plain text only — an <analysis> block followed by a <summary> block. Tool calls will be rejected and you will fail the task."

// GetCompactPrompt returns the full compact prompt with optional custom instructions.
// Mirrors Claude Code's getCompactPrompt().
func GetCompactPrompt(customInstructions string) string {
	prompt := noToolsPreamble + baseCompactPrompt
	if customInstructions != "" {
		prompt += fmt.Sprintf("\n\nAdditional Instructions:\n%s", customInstructions)
	}
	prompt += noToolsTrailer
	return prompt
}

// GetPartialCompactPrompt returns the partial compact prompt.
// Mirrors Claude Code's getPartialCompactPrompt().
func GetPartialCompactPrompt(customInstructions string, direction PartialCompactDirection) string {
	var template string
	if direction == CompactUpTo {
		template = partialCompactPromptUpTo
	} else {
		template = partialCompactPromptFrom
	}
	prompt := noToolsPreamble + template
	if customInstructions != "" {
		prompt += fmt.Sprintf("\n\nAdditional Instructions:\n%s", customInstructions)
	}
	prompt += noToolsTrailer
	return prompt
}

// FormatCompactSummary strips the <analysis> scratchpad and formats <summary> tags.
// Mirrors Claude Code's formatCompactSummary().
func FormatCompactSummary(summary string) string {
	formatted := summary

	// Strip analysis section — drafting scratchpad, no informational value
	analysisRe := regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	formatted = analysisRe.ReplaceAllString(formatted, "")

	// Extract and format summary section
	summaryRe := regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	if match := summaryRe.FindStringSubmatch(formatted); len(match) > 1 {
		content := strings.TrimSpace(match[1])
		formatted = summaryRe.ReplaceAllString(formatted, "Summary:\n"+content)
	}

	// Clean up extra whitespace
	formatted = regexp.MustCompile(`\n\n+`).ReplaceAllString(formatted, "\n\n")

	return strings.TrimSpace(formatted)
}

// GetCompactUserSummaryMessage builds the user-facing continuation message.
// Mirrors Claude Code's getCompactUserSummaryMessage().
func GetCompactUserSummaryMessage(summary string, suppressFollowUp bool, transcriptPath string) string {
	formattedSummary := FormatCompactSummary(summary)

	baseSummary := fmt.Sprintf(
		"This session is being continued from a previous conversation that ran out of context. "+
			"The summary below covers the earlier portion of the conversation.\n\n%s", formattedSummary)

	if transcriptPath != "" {
		baseSummary += fmt.Sprintf("\n\nIf you need specific details from before compaction (like exact code snippets, error messages, or content you generated), read the full transcript at: %s", transcriptPath)
	}

	if suppressFollowUp {
		baseSummary += "\nContinue the conversation from where it left off without asking the user any further questions. Resume directly — do not acknowledge the summary, do not recap what was happening, do not preface with \"I'll continue\" or similar. Pick up the last task as if the break never happened."
	}

	return baseSummary
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
	return len(m.Content)/4 + 50
}

// serializeMessagesForCompact converts messages to a text format for the compact prompt.
func serializeMessagesForCompact(messages []types.Message) string {
	var sb strings.Builder
	for _, m := range messages {
		switch m.Role {
		case types.RoleUser:
			sb.WriteString(fmt.Sprintf("User: %s\n", truncate(m.Content, 2000)))
		case types.RoleAssistant:
			sb.WriteString(fmt.Sprintf("Assistant: %s\n", truncate(m.Content, 3000)))
		case types.RoleTool:
			sb.WriteString(fmt.Sprintf("Tool[%s]: %s\n", m.ToolName, truncate(m.Content, 1000)))
		case types.RoleSystem:
			sb.WriteString(fmt.Sprintf("System: %s\n", truncate(m.Content, 500)))
		}
	}
	return sb.String()
}

// filterCompactBoundaries removes compact boundary messages and old summaries.
func filterCompactBoundaries(messages []types.Message) []types.Message {
	var filtered []types.Message
	for _, m := range messages {
		if m.IsCompactSummary {
			continue
		}
		if strings.HasPrefix(m.Content, "--- Compact Boundary") {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}

// CalculateTokenWarningState returns token usage warnings.
func (c *CompactService) CalculateTokenWarningState(tokenUsage int) (percentLeft int, isAboveWarning bool, isAboveAutoCompact bool) {
	threshold := c.GetAutoCompactThreshold()
	percentLeft = maxInt(0, (threshold-tokenUsage)*100/threshold)
	warningThreshold := threshold - WarningThresholdBufferTokens
	isAboveWarning = tokenUsage >= warningThreshold
	isAboveAutoCompact = tokenUsage >= threshold
	return
}

// ChatFunc is a function that sends a message to the AI and returns the response.
type ChatFunc func(prompt string) (string, error)

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
