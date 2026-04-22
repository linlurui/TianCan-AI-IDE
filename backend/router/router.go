package router

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ModelTier represents a capability tier for models.
type ModelTier string

const (
	TierLocal   ModelTier = "local" // LM Studio / local model
	TierAPI     ModelTier = "api"   // Remote API (OpenAI, DeepSeek, etc.)
	TierWeb     ModelTier = "web"   // Web-based (Claude web, ChatGPT web)
	TierUnknown ModelTier = "unknown"
)

// TaskCategory represents the type of task being routed.
type TaskCategory string

const (
	CategoryCodeEdit   TaskCategory = "code_edit"   // Write/modify code
	CategoryCodeReview TaskCategory = "code_review" // Read-only analysis
	CategoryChat       TaskCategory = "chat"        // General conversation
	CategoryResearch   TaskCategory = "research"    // Deep research / web search
	CategoryDebug      TaskCategory = "debug"       // Debugging assistance
)

// SmartRouterConfig holds the configuration for the Smart Router.
// TODO: Add hardware-aware fields (TotalMemoryGB, GPUMemoryGB, CPUCores) for auto model selection
// TODO: Add ModelSelectionStrategy field ("small_combo" | "single_large") determined by hardware
// TODO: Add SystemLoadThreshold for auto-downgrade based on real memory/CPU usage (gopsutil)
type SmartRouterConfig struct {
	AutoDowngrade bool        `json:"autoDowngrade"` // Enable auto-downgrade on failure
	AutoUpgrade   bool        `json:"autoUpgrade"`   // Enable auto-upgrade for complex tasks
	PreferredTier ModelTier   `json:"preferredTier"` // User's preferred tier
	FallbackOrder []ModelTier `json:"fallbackOrder"` // Order to try on failure
}

// DefaultConfig returns the default Smart Router configuration.
func DefaultConfig() SmartRouterConfig {
	return SmartRouterConfig{
		AutoDowngrade: true,
		AutoUpgrade:   false,
		PreferredTier: TierLocal,
		FallbackOrder: []ModelTier{TierLocal, TierAPI, TierWeb},
	}
}

// RouteResult represents the outcome of a routing decision.
type RouteResult struct {
	Tier       ModelTier    `json:"tier"`
	Category   TaskCategory `json:"category"`
	Confidence float64      `json:"confidence"`
	Reason     string       `json:"reason"`
	Fallback   bool         `json:"fallback"`
}

// FailureRecord tracks recent failures per tier.
type FailureRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Tier      ModelTier `json:"tier"`
	Error     string    `json:"error"`
}

// SmartRouter routes AI requests to the appropriate model tier
// based on task category, user preferences, and failure history.
type SmartRouter struct {
	mu       sync.RWMutex
	config   SmartRouterConfig
	failures []FailureRecord // recent failure records (last 10)
}

// NewSmartRouter creates a new Smart Router with the given config.
func NewSmartRouter(cfg SmartRouterConfig) *SmartRouter {
	return &SmartRouter{
		config:   cfg,
		failures: make([]FailureRecord, 0, 10),
	}
}

// Route determines the best model tier for a given user message.
// TODO: Implement hardware-aware model selection (detect RAM/VRAM → choose model size)
// TODO: Implement resource-aware auto-downgrade (monitor system load via gopsutil, downgrade when memory > 80%)
// TODO: Implement auto-upgrade based on task complexity score (not just confidence threshold)
func (sr *SmartRouter) Route(userMessage string) RouteResult {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	category := classifyTask(userMessage)
	confidence := taskConfidence(userMessage, category)

	// Start with preferred tier
	tier := sr.config.PreferredTier

	// Auto-upgrade: if task is complex and upgrade is enabled, use higher tier
	if sr.config.AutoUpgrade && confidence < 0.6 {
		switch category {
		case CategoryCodeEdit, CategoryResearch:
			if tier == TierLocal {
				tier = TierAPI
			}
		}
	}

	// Auto-downgrade: if preferred tier has recent failures, try next
	if sr.config.AutoDowngrade {
		if sr.hasRecentFailure(tier) {
			for _, fallback := range sr.config.FallbackOrder {
				if fallback != tier && !sr.hasRecentFailure(fallback) {
					tier = fallback
					return RouteResult{
						Tier:       tier,
						Category:   category,
						Confidence: confidence,
						Reason:     fmt.Sprintf("Preferred tier %s had recent failures, falling back to %s", sr.config.PreferredTier, tier),
						Fallback:   true,
					}
				}
			}
		}
	}

	return RouteResult{
		Tier:       tier,
		Category:   category,
		Confidence: confidence,
		Reason:     fmt.Sprintf("Routed to %s for %s task", tier, category),
		Fallback:   tier != sr.config.PreferredTier,
	}
}

// RecordFailure records a failure for a given tier.
func (sr *SmartRouter) RecordFailure(tier ModelTier, errMsg string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	sr.failures = append(sr.failures, FailureRecord{
		Timestamp: time.Now(),
		Tier:      tier,
		Error:     errMsg,
	})

	// Keep only last 10 failures
	if len(sr.failures) > 10 {
		sr.failures = sr.failures[len(sr.failures)-10:]
	}
}

// GetConfig returns the current router config.
func (sr *SmartRouter) GetConfig() SmartRouterConfig {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.config
}

// SetConfig updates the router config.
func (sr *SmartRouter) SetConfig(cfg SmartRouterConfig) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.config = cfg
}

// SetAutoDowngrade enables or disables auto-downgrade.
func (sr *SmartRouter) SetAutoDowngrade(enabled bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.config.AutoDowngrade = enabled
}

// SetAutoUpgrade enables or disables auto-upgrade.
func (sr *SmartRouter) SetAutoUpgrade(enabled bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.config.AutoUpgrade = enabled
}

// hasRecentFailure checks if a tier has had a failure in the last 5 minutes.
func (sr *SmartRouter) hasRecentFailure(tier ModelTier) bool {
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, f := range sr.failures {
		if f.Tier == tier && f.Timestamp.After(cutoff) {
			return true
		}
	}
	return false
}

// classifyTask uses keyword matching to determine the task category.
// TODO: Replace hardcoded keyword matching with AI-driven classification (delegate to LLM)
// TODO: Load keywords from config/env (TIANCAN_ROUTE_KEYWORDS) instead of hardcoding
func classifyTask(msg string) TaskCategory {
	lower := strings.ToLower(msg)

	// Code editing indicators
	editKeywords := []string{"修改", "改", "写", "创建", "添加", "删除", "重构", "实现", "fix", "write", "create", "edit", "modify", "delete", "refactor", "implement"}
	for _, kw := range editKeywords {
		if strings.Contains(lower, kw) {
			return CategoryCodeEdit
		}
	}

	// Research indicators
	researchKeywords := []string{"研究", "搜索", "查找资料", "调研", "research", "search", "investigate", "look up"}
	for _, kw := range researchKeywords {
		if strings.Contains(lower, kw) {
			return CategoryResearch
		}
	}

	// Debug indicators
	debugKeywords := []string{"调试", "断点", "报错", "错误", "bug", "debug", "breakpoint", "error", "crash", "trace"}
	for _, kw := range debugKeywords {
		if strings.Contains(lower, kw) {
			return CategoryDebug
		}
	}

	// Code review indicators
	reviewKeywords := []string{"审查", "检查", "分析", "解释", "review", "check", "analyze", "explain", "read"}
	for _, kw := range reviewKeywords {
		if strings.Contains(lower, kw) {
			return CategoryCodeReview
		}
	}

	return CategoryChat
}

// taskConfidence returns a confidence score (0-1) for the classification.
// TODO: Use LLM-based confidence scoring instead of message-length heuristic
func taskConfidence(msg string, category TaskCategory) float64 {
	if category == CategoryChat {
		return 0.5 // default category, low confidence
	}
	// More specific categories get higher confidence
	msgLen := len(msg)
	if msgLen < 10 {
		return 0.3 // very short messages are ambiguous
	}
	if msgLen > 100 {
		return 0.8 // longer messages provide more context
	}
	return 0.6
}
