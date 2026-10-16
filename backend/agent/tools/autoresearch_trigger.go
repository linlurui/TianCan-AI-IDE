package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
	"github.com/linlurui/tiancan-ai-ide/backend/events"
)

// ─── AutoResearch Dual-Trigger System ────────────────────────────────────
//
// Triggering AutoResearch requires TWO conditions to be met simultaneously:
//
//   1. System Event: A predefined event occurs (e.g., performance bottleneck
//      detected, high memory usage, slow test execution). Events are mapped
//      from an external configuration (TIANCAN_AUTORESEARCH_EVENTS env).
//
//   2. Sub-Agent Judgment: A lightweight LLM call evaluates the specific
//      context of the event and decides whether AutoResearch is truly needed.
//      This prevents false triggers from transient spikes.
//
// Only when BOTH conditions are satisfied does the trigger fire.

// AutoResearchEvent represents a system event that may trigger AutoResearch.
type AutoResearchEvent struct {
	Type      string            `json:"type"`     // e.g. "perf_bottleneck", "high_memory", "slow_test"
	Source    string            `json:"source"`   // e.g. "pprof", "benchmark", "monitor"
	Details   string            `json:"details"`  // Human-readable description
	Metadata  map[string]string `json:"metadata"` // Extra context (file, function, metric value)
	Timestamp time.Time         `json:"timestamp"`
}

// TriggerDecision records whether AutoResearch should be triggered.
type TriggerDecision struct {
	ShouldTrigger bool    `json:"should_trigger"`
	Reason        string  `json:"reason"`
	Confidence    float64 `json:"confidence"` // 0.0-1.0
}

// AutoResearchTrigger manages the dual-detection mechanism.
type AutoResearchTrigger struct {
	mu          sync.Mutex
	llm         LLMCaller
	sink        events.Sink
	eventMap    map[string]bool // event types that are mapped as triggerable
	pending     []AutoResearchEvent
	cooldown    time.Duration // minimum time between triggers
	lastTrigger time.Time
}

// NewAutoResearchTrigger creates a new trigger instance.
// sink 为事件出口（G3 ProgressSink 抽象）；传 nil 时使用包级默认 Sink（静默）。
func NewAutoResearchTrigger(llm LLMCaller, sink events.Sink) *AutoResearchTrigger {
	if sink == nil {
		sink = events.Default()
	}
	t := &AutoResearchTrigger{
		llm:      llm,
		sink:     sink,
		eventMap: loadEventMapFromEnv(),
		pending:  make([]AutoResearchEvent, 0),
		cooldown: 5 * time.Minute, // don't re-trigger within 5 minutes
	}
	return t
}

// emit 发射事件到 Sink（ nil 安全）。
func (t *AutoResearchTrigger) emit(event string, payload interface{}) {
	if t.sink != nil {
		t.sink.Emit(event, payload)
	}
}

// loadEventMapFromEnv loads triggerable event types from TIANCAN_AUTORESEARCH_EVENTS.
// Default: "perf_bottleneck,high_memory,slow_test,test_failure"
func loadEventMapFromEnv() map[string]bool {
	m := map[string]bool{
		"perf_bottleneck": true,
		"high_memory":     true,
		"slow_test":       true,
		"test_failure":    true,
	}
	if list := envconfig.EnvList("TIANCAN_AUTORESEARCH_EVENTS"); list != nil {
		// Override with env-specified events
		m = make(map[string]bool)
		for _, e := range list {
			m[e] = true
		}
	}
	return m
}

// OnEvent is called when a system event occurs (Condition 1).
// If the event type is in the trigger map, it's queued for sub-agent evaluation.
func (t *AutoResearchTrigger) OnEvent(evt AutoResearchEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Condition 1: Is this event type mapped as triggerable?
	if !t.eventMap[evt.Type] {
		return
	}

	evt.Timestamp = time.Now()
	t.pending = append(t.pending, evt)

	// Keep only last 20 pending events
	if len(t.pending) > 20 {
		t.pending = t.pending[len(t.pending)-20:]
	}

	// Emit event to frontend for visibility
	t.emit("autoresearch:event", evt)

	log.Printf("autoresearch_trigger: event queued: %s (%s)", evt.Type, evt.Details)
}

// Evaluate checks pending events with the sub-agent LLM (Condition 2).
// Returns a TriggerDecision if both conditions are met, nil otherwise.
func (t *AutoResearchTrigger) Evaluate(ctx context.Context) *TriggerDecision {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.pending) == 0 {
		return nil
	}

	// Check cooldown
	if !t.lastTrigger.IsZero() && time.Since(t.lastTrigger) < t.cooldown {
		return nil
	}

	// Condition 2: Sub-agent LLM judgment
	if t.llm == nil {
		// No LLM available — auto-approve if there are pending events
		evt := t.pending[len(t.pending)-1]
		t.pending = t.pending[:0]
		t.lastTrigger = time.Now()
		return &TriggerDecision{
			ShouldTrigger: true,
			Reason:        fmt.Sprintf("Event %s detected (LLM unavailable, auto-approved)", evt.Type),
			Confidence:    0.5,
		}
	}

	// Build context for sub-agent
	var eventsDesc []string
	for _, evt := range t.pending {
		eventsDesc = append(eventsDesc, fmt.Sprintf("- [%s] %s: %s", evt.Type, evt.Source, evt.Details))
	}
	prompt := fmt.Sprintf(`You are a sub-agent that decides whether to trigger an automated code research and optimization process (AutoResearch).

The following system events were detected:
%s

Based on these events, should AutoResearch be triggered? Consider:
1. Are these events likely caused by a real code issue (not just transient load)?
2. Is the issue specific enough that AutoResearch can meaningfully investigate?
3. Would the potential benefit outweigh the cost of running the research loop?

Respond in JSON format only:
{"should_trigger": true/false, "reason": "brief explanation", "confidence": 0.0-1.0}`, strings.Join(eventsDesc, "\n"))

	resp, err := t.llm.CallLLM(ctx, prompt)
	if err != nil {
		log.Printf("autoresearch_trigger: LLM evaluation failed: %v", err)
		return nil
	}

	// Parse LLM response
	var decision TriggerDecision
	// Try to extract JSON from response
	resp = extractJSONFromResponse(resp)
	if err := json.Unmarshal([]byte(resp), &decision); err != nil {
		log.Printf("autoresearch_trigger: failed to parse LLM decision: %v (response: %s)", err, resp[:min(len(resp), 200)])
		return nil
	}

	if decision.ShouldTrigger && decision.Confidence >= 0.6 {
		t.pending = t.pending[:0]
		t.lastTrigger = time.Now()

		t.emit("autoresearch:triggered", decision)

		log.Printf("autoresearch_trigger: TRIGGERED (confidence=%.2f): %s", decision.Confidence, decision.Reason)
		return &decision
	}

	log.Printf("autoresearch_trigger: NOT triggered (confidence=%.2f): %s", decision.Confidence, decision.Reason)
	return nil
}

// extractJSONFromResponse tries to find a JSON object in the LLM response.
func extractJSONFromResponse(resp string) string {
	resp = strings.TrimSpace(resp)
	// Try direct parse first
	if strings.HasPrefix(resp, "{") {
		return resp
	}
	// Try to find JSON in markdown code block
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start != -1 && end > start {
		return resp[start : end+1]
	}
	return resp
}
