package compact

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

type CompactionStrategy string

const (
	StrategySummary  CompactionStrategy = "summary"
	StrategyExtract  CompactionStrategy = "extract"
	StrategyTruncate CompactionStrategy = "truncate"
)

type CompactionLevel string

const (
	LevelLight      CompactionLevel = "light"
	LevelMedium     CompactionLevel = "medium"
	LevelAggressive CompactionLevel = "aggressive"
)

type CompactionPlan struct {
	Strategy          CompactionStrategy `json:"strategy"`
	Level             CompactionLevel    `json:"level"`
	TotalMessages     int                `json:"totalMessages"`
	MessagesToKeep    int                `json:"messagesToKeep"`
	MessagesToCompact int                `json:"messagesToCompact"`
}

type CompactionQuality struct {
	CompressionRatio float64 `json:"compressionRatio"`
	Score            float64 `json:"score"`
}

type AdvancedCompactor struct {
	mu            sync.Mutex
	contextWindow int
	chatFn        ChatFunc
}

func NewAdvancedCompactor(cw int, chatFn ChatFunc) *AdvancedCompactor {
	if cw <= 0 {
		cw = DefaultContextWindow
	}
	return &AdvancedCompactor{contextWindow: cw, chatFn: chatFn}
}

func (ac *AdvancedCompactor) Plan(msgs []types.Message) *CompactionPlan {
	if len(msgs) == 0 {
		return nil
	}
	tt := EstimateTokens(msgs)
	if tt < ac.contextWindow/2 {
		return nil
	}
	ratio := float64(tt) / float64(ac.contextWindow)
	var level CompactionLevel
	var keep float64
	switch {
	case ratio > 0.9:
		level, keep = LevelAggressive, 0.2
	case ratio > 0.7:
		level, keep = LevelMedium, 0.4
	default:
		level, keep = LevelLight, 0.7
	}
	kc := int(float64(len(msgs)) * (1 - keep))
	if kc < 4 {
		kc = 4
	}
	return &CompactionPlan{
		Strategy: StrategySummary, Level: level,
		TotalMessages: len(msgs), MessagesToKeep: kc,
		MessagesToCompact: len(msgs) - kc,
	}
}

func (ac *AdvancedCompactor) Execute(msgs []types.Message, plan *CompactionPlan) (*types.CompactionResult, *CompactionQuality, error) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if plan == nil {
		return nil, nil, fmt.Errorf("nil plan")
	}
	pre := EstimateTokens(msgs)
	toKeep := msgs[len(msgs)-plan.MessagesToKeep:]
	toCompact := msgs[:len(msgs)-plan.MessagesToKeep]
	summary, err := ac.generateSummary(toCompact)
	if err != nil {
		return nil, nil, err
	}
	sm := types.Message{
		Role:             types.RoleUser,
		Content:          fmt.Sprintf("[Compacted]\n%s\n---", summary),
		IsCompactSummary: true,
		Timestamp:        time.Now(),
	}
	result := &types.CompactionResult{
		BoundaryMarker:        fmt.Sprintf("--- Compact (%s,%s,pre=%d) ---", plan.Strategy, plan.Level, pre),
		SummaryMessages:       []types.Message{sm},
		PreCompactTokenCount:  pre,
		PostCompactTokenCount: EstimateTokens(append([]types.Message{sm}, toKeep...)),
	}
	cr := float64(result.PostCompactTokenCount) / float64(max(pre, 1))
	return result, &CompactionQuality{CompressionRatio: cr, Score: 1.0 - cr}, nil
}

func (ac *AdvancedCompactor) CompactWithProgressiveFallback(msgs []types.Message) (*types.CompactionResult, error) {
	plan := ac.Plan(msgs)
	if plan == nil {
		return nil, fmt.Errorf("no compaction needed")
	}
	r, _, err := ac.Execute(msgs, plan)
	if err == nil {
		return r, nil
	}
	kc := 4
	if kc >= len(msgs) {
		return nil, fmt.Errorf("too few messages")
	}
	sm := types.Message{
		Role: types.RoleUser, Content: ac.extractKeyFacts(msgs[:len(msgs)-kc]),
		IsCompactSummary: true, Timestamp: time.Now(),
	}
	return &types.CompactionResult{
		BoundaryMarker:  "fallback",
		SummaryMessages: []types.Message{sm},
		PreCompactTokenCount:  EstimateTokens(msgs),
		PostCompactTokenCount: EstimateTokens(append([]types.Message{sm}, msgs[len(msgs)-kc:]...)),
	}, nil
}

func (ac *AdvancedCompactor) generateSummary(msgs []types.Message) (string, error) {
	if ac.chatFn == nil {
		return ac.extractKeyFacts(msgs), nil
	}
	return ac.chatFn(buildAdvancedCompactPrompt(msgs))
}

func (ac *AdvancedCompactor) extractKeyFacts(msgs []types.Message) string {
	var f []string
	for _, m := range msgs {
		switch m.Role {
		case types.RoleUser:
			if len(m.Content) > 10 {
				f = append(f, "User: "+truncate(m.Content, 200))
			}
		case types.RoleAssistant:
			if m.ToolName != "" {
				f = append(f, "Called: "+m.ToolName)
			} else if len(m.Content) > 10 {
				f = append(f, "Resp: "+truncate(m.Content, 200))
			}
		case types.RoleTool:
			s := "ok"
			if !m.Success {
				s = "fail"
			}
			f = append(f, fmt.Sprintf("Tool %s %s", m.ToolName, s))
		}
	}
	return strings.Join(f, "\n")
}

func buildAdvancedCompactPrompt(msgs []types.Message) string {
	var sb strings.Builder
	sb.WriteString("Summarize this conversation preserving key decisions, code changes, and tool results. Be concise.\n\n")
	for _, m := range msgs {
		switch m.Role {
		case types.RoleUser:
			sb.WriteString(fmt.Sprintf("User: %s\n", truncate(m.Content, 2000)))
		case types.RoleAssistant:
			sb.WriteString(fmt.Sprintf("Assistant: %s\n", truncate(m.Content, 3000)))
		case types.RoleTool:
			sb.WriteString(fmt.Sprintf("Tool[%s]: %s\n", m.ToolName, truncate(m.Content, 1000)))
		}
	}
	return sb.String()
}
