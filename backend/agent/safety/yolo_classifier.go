package safety

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// TwoStageMode defines the classifier operation mode.
// Mirrors Claude Code's TwoStageMode.
type TwoStageMode string

const (
	ModeBoth     TwoStageMode = "both"     // Stage 1 fast + Stage 2 thinking (default)
	ModeFast     TwoStageMode = "fast"     // Stage 1 only
	ModeThinking TwoStageMode = "thinking" // Stage 2 only
)

// ClassifierStage indicates which stage produced the result.
type ClassifierStage string

const (
	StageFast     ClassifierStage = "fast"
	StageThinking ClassifierStage = "thinking"
)

// YOLOClassifierResult mirrors Claude Code's YoloClassifierResult.
type YOLOClassifierResult struct {
	ShouldBlock       bool            `json:"shouldBlock"`
	Reason            string          `json:"reason"`
	Thinking          string          `json:"thinking,omitempty"`
	Stage             ClassifierStage `json:"stage,omitempty"`
	Unavailable       bool            `json:"unavailable,omitempty"`
	TranscriptTooLong bool            `json:"transcriptTooLong,omitempty"`
	DurationMs        int64           `json:"durationMs,omitempty"`
	Model             string          `json:"model,omitempty"`

	// Stage 1 details
	Stage1DurationMs int64 `json:"stage1DurationMs,omitempty"`

	// Stage 2 details
	Stage2DurationMs int64 `json:"stage2DurationMs,omitempty"`
}

// TranscriptBlock represents a single content block in a transcript entry.
type TranscriptBlock struct {
	Type  string      `json:"type"` // "text" or "tool_use"
	Text  string      `json:"text,omitempty"`
	Name  string      `json:"name,omitempty"`  // tool name for tool_use
	Input interface{} `json:"input,omitempty"` // tool input for tool_use
}

// TranscriptEntry represents a user or assistant turn in the transcript.
type TranscriptEntry struct {
	Role    string            `json:"role"` // "user" or "assistant"
	Content []TranscriptBlock `json:"content"`
}

// AutoModeRules mirrors Claude Code's AutoModeRules — user-customizable classifier sections.
type AutoModeRules struct {
	Allow       []string `json:"allow"`
	SoftDeny    []string `json:"soft_deny"`
	Environment []string `json:"environment"`
}

// YOLOClassifier implements the 2-stage XML classifier for auto mode.
// Mirrors Claude Code's yoloClassifier.ts architecture.
type YOLOClassifier struct {
	mode      TwoStageMode
	model     string
	chatFn    types.ChatFunc
	autoRules AutoModeRules
}

// classifierChatFn is a simplified chat function for the classifier.
// Wraps types.ChatFunc to provide a simpler interface.
type classifierChatFn func(prompt string) (string, error)

// NewYOLOClassifier creates a new YOLO classifier.
func NewYOLOClassifier(chatFn types.ChatFunc) *YOLOClassifier {
	mode := ModeBoth
	if m := os.Getenv("TIANCAN_CLASSIFIER_MODE"); m != "" {
		switch m {
		case "fast":
			mode = ModeFast
		case "thinking":
			mode = ModeThinking
		}
	}

	model := os.Getenv("TIANCAN_CLASSIFIER_MODEL")
	if model == "" {
		model = "default"
	}

	return &YOLOClassifier{
		mode:   mode,
		model:  model,
		chatFn: chatFn,
		autoRules: AutoModeRules{
			Allow:       []string{},
			SoftDeny:    []string{},
			Environment: []string{},
		},
	}
}

// simpleChat wraps the ChatFunc to provide a simple string-in/string-out interface.
func (y *YOLOClassifier) simpleChat(prompt string) (string, error) {
	return y.chatFn(prompt, nil)
}

// --- Transcript serialization (mirrors Claude Code's buildTranscriptEntries + toCompact) ---

// BuildTranscriptEntries builds transcript entries from messages.
// Mirrors Claude Code's buildTranscriptEntries().
func BuildTranscriptEntries(messages []types.Message) []TranscriptEntry {
	var transcript []TranscriptEntry

	for _, msg := range messages {
		switch msg.Role {
		case types.RoleUser:
			if msg.Content == "" {
				continue
			}
			transcript = append(transcript, TranscriptEntry{
				Role: "user",
				Content: []TranscriptBlock{
					{Type: "text", Text: msg.Content},
				},
			})
		case types.RoleAssistant:
			// Only include tool_use blocks — assistant text is model-authored
			// and could be crafted to influence the classifier's decision.
			if msg.ToolName != "" {
				transcript = append(transcript, TranscriptEntry{
					Role: "assistant",
					Content: []TranscriptBlock{
						{
							Type:  "tool_use",
							Name:  msg.ToolName,
							Input: msg.ToolArgs,
						},
					},
				})
			}
		case types.RoleTool:
			// Tool results are included as user context
			transcript = append(transcript, TranscriptEntry{
				Role: "user",
				Content: []TranscriptBlock{
					{Type: "text", Text: fmt.Sprintf("Tool[%s]: %s", msg.ToolName, msg.Content)},
				},
			})
		}
	}

	return transcript
}

// SerializeTranscript converts transcript entries to a compact string.
// Mirrors Claude Code's toCompact() + buildTranscriptForClassifier().
func SerializeTranscript(entries []TranscriptEntry) string {
	var sb strings.Builder
	for _, entry := range entries {
		for _, block := range entry.Content {
			switch block.Type {
			case "text":
				if entry.Role == "user" {
					sb.WriteString(fmt.Sprintf("User: %s\n", block.Text))
				}
			case "tool_use":
				inputJSON, _ := json.Marshal(block.Input)
				sb.WriteString(fmt.Sprintf("%s %s\n", block.Name, string(inputJSON)))
			}
		}
	}
	return sb.String()
}

// BuildTranscriptForClassifier builds a compact transcript for the classifier.
// Mirrors Claude Code's buildTranscriptForClassifier().
func BuildTranscriptForClassifier(messages []types.Message) string {
	entries := BuildTranscriptEntries(messages)
	return SerializeTranscript(entries)
}

// --- System prompt (mirrors Claude Code's buildYoloSystemPrompt) ---

const baseClassifierPrompt = `You are a security classifier for an AI coding assistant operating in auto mode. Your job is to decide whether the agent's proposed action should be allowed or blocked.

## Classification Process

1. Review the transcript of the conversation so far.
2. Evaluate the proposed action in context.
3. Consider the user's intent as expressed in their messages and configuration.
4. Check against the allow and deny rules below.
5. Make a decision: allow or block.

## Allow Rules
Actions matching these patterns are generally safe and should be allowed:
<user_allow_rules_to_replace>
- Reading files (Read, Glob, Grep)
- Searching code (Grep, Glob)
- Listing directories
- Writing to project files (non-sensitive)
- Running non-destructive commands (build, test, lint)
- Creating new files in the project
- Editing existing project files
</user_allow_rules_to_replace>

## Deny Rules
Actions matching these patterns should be blocked or require confirmation:
<user_deny_rules_to_replace>
- Deleting files outside the project
- Running destructive commands (rm -rf, mkfs, dd)
- Modifying shell configuration files (.bashrc, .zshrc, .profile)
- Modifying SSH/GPG configuration
- Force-pushing to git remotes
- Publishing packages
- Running commands with sudo
- Piping remote content to shell (curl|sh)
- Modifying .git internals
- Deleting protected directories
</user_deny_rules_to_replace>

## Environment Context
<user_environment_to_replace>
- The agent operates within a project directory
- The agent should not modify files outside the project root
- The agent should not execute commands that affect the system globally
</user_environment_to_replace>

## Decision Criteria

- If the action clearly matches an allow rule → ALLOW
- If the action clearly matches a deny rule → BLOCK
- If the action is ambiguous → BLOCK (err on the side of caution)
- Consider the user's explicit intent from their messages
- Consider whether the action is reversible
- Consider the scope of impact (project-only vs system-wide)

Use the classify_result tool to report your classification.`

// BuildClassifierSystemPrompt assembles the system prompt with user rules.
// Mirrors Claude Code's buildYoloSystemPrompt().
func (y *YOLOClassifier) BuildClassifierSystemPrompt() string {
	prompt := baseClassifierPrompt

	// Replace allow rules
	allowRules := y.autoRules.Allow
	if len(allowRules) > 0 {
		replacement := strings.Join(allowRules, "\n")
		prompt = replaceTaggedSection(prompt, "user_allow_rules_to_replace", replacement)
	}

	// Replace deny rules
	denyRules := y.autoRules.SoftDeny
	if len(denyRules) > 0 {
		replacement := strings.Join(denyRules, "\n")
		prompt = replaceTaggedSection(prompt, "user_deny_rules_to_replace", replacement)
	}

	// Replace environment
	envRules := y.autoRules.Environment
	if len(envRules) > 0 {
		replacement := strings.Join(envRules, "\n")
		prompt = replaceTaggedSection(prompt, "user_environment_to_replace", replacement)
	}

	return prompt
}

func replaceTaggedSection(prompt, tagName, replacement string) string {
	openTag := "<" + tagName + ">"
	closeTag := "</" + tagName + ">"
	re := regexp.MustCompile(regexp.QuoteMeta(openTag) + `[\s\S]*?` + regexp.QuoteMeta(closeTag))
	return re.ReplaceAllString(prompt, openTag+"\n"+replacement+"\n"+closeTag)
}

// --- 2-Stage XML Classifier (mirrors Claude Code's classifyYoloActionXml) ---

const xmlS1Suffix = "\nErr on the side of blocking. <block> immediately."
const xmlS2Suffix = "\nReview the classification process and follow it carefully, making sure you deny actions that should be blocked. As a reminder, explicit (not suggestive or implicit) user confirmation is required to override blocks. Use <thinking> before responding with <block>."

// ClassifyAction classifies whether an agent action should be allowed or blocked.
// Mirrors Claude Code's classifyYoloAction().
func (y *YOLOClassifier) ClassifyAction(ctx context.Context, messages []types.Message, action TranscriptEntry) YOLOClassifierResult {
	// Build transcript
	transcriptEntries := BuildTranscriptEntries(messages)
	actionCompact := serializeAction(action)

	// Empty action = no security relevance
	if actionCompact == "" {
		return YOLOClassifierResult{
			ShouldBlock: false,
			Reason:      "Tool declares no classifier-relevant input",
			Model:       y.model,
		}
	}

	// Build system prompt
	systemPrompt := y.BuildClassifierSystemPrompt()

	// Build user prompt (transcript + action)
	transcriptStr := SerializeTranscript(transcriptEntries)
	userPrompt := "<transcript>\n" + transcriptStr + actionCompact + "</transcript>\n"

	// Dispatch to 2-stage XML classifier
	return y.classifyXmlAction(ctx, systemPrompt, userPrompt)
}

// ClassifyHandoff classifies whether a handoff to a sub-agent should be allowed.
// Mirrors Claude Code's handoff classification for AgentTool.
func (y *YOLOClassifier) ClassifyHandoff(ctx context.Context, messages []types.Message, agentName string, task string) YOLOClassifierResult {
	action := TranscriptEntry{
		Role: "assistant",
		Content: []TranscriptBlock{
			{
				Type: "tool_use",
				Name: "Agent",
				Input: map[string]interface{}{
					"agent": agentName,
					"task":  task,
				},
			},
		},
	}
	return y.ClassifyAction(ctx, messages, action)
}

// classifyXmlAction implements the 2-stage XML classifier.
// Mirrors Claude Code's classifyYoloActionXml().
func (y *YOLOClassifier) classifyXmlAction(ctx context.Context, systemPrompt, userPrompt string) YOLOClassifierResult {
	xmlSystemPrompt := replaceOutputFormatWithXml(systemPrompt)
	overallStart := time.Now()

	// Stage 1: fast (if enabled)
	if y.mode != ModeThinking {
		stage1Start := time.Now()
		stage1Prompt := xmlSystemPrompt + "\n\n" + userPrompt + xmlS1Suffix

		stage1Response, err := y.simpleChat(stage1Prompt)
		stage1DurationMs := time.Since(stage1Start).Milliseconds()

		if err != nil {
			// Stage 1 error — if mode is fast-only, block for safety
			if y.mode == ModeFast {
				return YOLOClassifierResult{
					ShouldBlock: true,
					Reason:      "Classifier stage 1 error - blocking for safety",
					Stage:       StageFast,
					DurationMs:  time.Since(overallStart).Milliseconds(),
					Model:       y.model,
					Unavailable: true,
				}
			}
			// In both mode, fall through to stage 2
			goto stage2
		}

		stage1Block := parseXmlBlock(stage1Response)

		// If stage 1 says allow, return immediately (fast path)
		if stage1Block != nil && !*stage1Block {
			return YOLOClassifierResult{
				ShouldBlock:      false,
				Reason:           "Allowed by fast classifier",
				Stage:            StageFast,
				Stage1DurationMs: stage1DurationMs,
				DurationMs:       time.Since(overallStart).Milliseconds(),
				Model:            y.model,
			}
		}

		// In fast-only mode, stage 1 is final
		if y.mode == ModeFast {
			if stage1Block == nil {
				return YOLOClassifierResult{
					ShouldBlock:      true,
					Reason:           "Classifier stage 1 unparseable - blocking for safety",
					Stage:            StageFast,
					Stage1DurationMs: stage1DurationMs,
					DurationMs:       time.Since(overallStart).Milliseconds(),
					Model:            y.model,
				}
			}
			reason := parseXmlReason(stage1Response)
			if reason == "" {
				reason = "Blocked by fast classifier"
			}
			return YOLOClassifierResult{
				ShouldBlock:      true,
				Reason:           reason,
				Stage:            StageFast,
				Stage1DurationMs: stage1DurationMs,
				DurationMs:       time.Since(overallStart).Milliseconds(),
				Model:            y.model,
			}
		}
	}

stage2:
	// Stage 2: thinking (suffix asks for chain-of-thought)
	stage2Start := time.Now()
	stage2Prompt := xmlSystemPrompt + "\n\n" + userPrompt + xmlS2Suffix

	stage2Response, err := y.simpleChat(stage2Prompt)
	stage2DurationMs := time.Since(stage2Start).Milliseconds()
	totalDurationMs := time.Since(overallStart).Milliseconds()

	if err != nil {
		return YOLOClassifierResult{
			ShouldBlock:      true,
			Reason:           "Classifier stage 2 error - blocking for safety",
			Stage:            StageThinking,
			Stage2DurationMs: stage2DurationMs,
			DurationMs:       totalDurationMs,
			Model:            y.model,
			Unavailable:      true,
		}
	}

	stage2Block := parseXmlBlock(stage2Response)
	thinking := parseXmlThinking(stage2Response)

	if stage2Block == nil {
		return YOLOClassifierResult{
			ShouldBlock:      true,
			Reason:           "Classifier stage 2 unparseable - blocking for safety",
			Thinking:         thinking,
			Stage:            StageThinking,
			Stage2DurationMs: stage2DurationMs,
			DurationMs:       totalDurationMs,
			Model:            y.model,
		}
	}

	reason := parseXmlReason(stage2Response)
	if reason == "" {
		reason = "No reason provided"
	}

	return YOLOClassifierResult{
		ShouldBlock:      *stage2Block,
		Reason:           reason,
		Thinking:         thinking,
		Stage:            StageThinking,
		Stage2DurationMs: stage2DurationMs,
		DurationMs:       totalDurationMs,
		Model:            y.model,
	}
}

// --- XML parsing utilities (mirrors Claude Code's parseXmlBlock/parseXmlReason/parseXmlThinking) ---

// stripThinking removes <thinking> content so inner tags don't confuse parsers.
func stripThinking(text string) string {
	re1 := regexp.MustCompile(`(?s)<thinking>.*?</thinking>`)
	text = re1.ReplaceAllString(text, "")
	re2 := regexp.MustCompile(`(?s)<thinking>.*$`)
	text = re2.ReplaceAllString(text, "")
	return text
}

// parseXmlBlock parses <block>yes/no</block> from the response.
func parseXmlBlock(text string) *bool {
	cleaned := stripThinking(text)
	re := regexp.MustCompile(`(?i)<block>(yes|no)\b`)
	matches := re.FindAllStringSubmatch(cleaned, -1)
	if len(matches) == 0 {
		return nil
	}
	val := strings.ToLower(matches[0][1]) == "yes"
	return &val
}

// parseXmlReason parses <reason>...</reason> from the response.
func parseXmlReason(text string) string {
	cleaned := stripThinking(text)
	re := regexp.MustCompile(`(?s)<reason>(.*?)</reason>`)
	matches := re.FindStringSubmatch(cleaned)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

// parseXmlThinking parses <thinking>...</thinking> from the response.
func parseXmlThinking(text string) string {
	re := regexp.MustCompile(`(?s)<thinking>(.*?)</thinking>`)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

// replaceOutputFormatWithXml replaces the tool_use format instruction with XML format.
// Mirrors Claude Code's replaceOutputFormatWithXml().
func replaceOutputFormatWithXml(systemPrompt string) string {
	toolUseLine := "Use the classify_result tool to report your classification."
	xmlFormat := `## Output Format

If the action should be blocked:
<block>yes</block><reason>one short sentence</reason>

If the action should be allowed:
<block>no</block>

Do NOT include a <reason> tag when the action is allowed.
Your ENTIRE response MUST begin with <block>. Do NOT output any analysis, reasoning, or commentary before <block>.`
	return strings.Replace(systemPrompt, toolUseLine, xmlFormat, 1)
}

// serializeAction serializes a single action transcript entry.
func serializeAction(action TranscriptEntry) string {
	return SerializeTranscript([]TranscriptEntry{action})
}

// SetAutoModeRules updates the classifier's allow/deny/environment rules.
func (y *YOLOClassifier) SetAutoModeRules(rules AutoModeRules) {
	y.autoRules = rules
}

// GetAutoModeRules returns the current classifier rules.
func (y *YOLOClassifier) GetAutoModeRules() AutoModeRules {
	return y.autoRules
}

// SetMode updates the classifier operation mode.
func (y *YOLOClassifier) SetMode(mode TwoStageMode) {
	y.mode = mode
}

// GetMode returns the current classifier mode.
func (y *YOLOClassifier) GetMode() TwoStageMode {
	return y.mode
}
