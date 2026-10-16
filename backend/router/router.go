package router

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/cpu"

	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ModelTier represents a capability tier for models.
type ModelTier string

const (
	TierLocal   ModelTier = "local" // LM Studio / local model
	TierAPI     ModelTier = "api"   // Remote API (OpenAI, DeepSeek, etc.)
	TierWeb     ModelTier = "web"   // Web-based (Claude web, ChatGPT web)
	TierUnknown ModelTier = "unknown"
)

// LocalModelStatus describes a local model's hardware availability.
type LocalModelStatus struct {
	ID            string  `json:"id"`
	Available     bool    `json:"available"`
	DisableReason string  `json:"disable_reason,omitempty"`
	MinRAMGB      float64 `json:"min_ram_gb"`
	MinVRAMGB     float64 `json:"min_vram_gb"`
	ParamsB       float64 `json:"params_b"`
	SizeGB        float64 `json:"size_gb"`
}

// TaskCategory represents the type of task being routed.
type TaskCategory string

const (
	CategoryCodeEdit   TaskCategory = "code_edit"   // Write/modify code
	CategoryCodeReview TaskCategory = "code_review" // Read-only analysis
	CategoryChat       TaskCategory = "chat"        // General conversation
	CategoryResearch   TaskCategory = "research"    // Deep research / web search
	CategoryDebug      TaskCategory = "debug"       // Debugging assistance
)

// ModelSelectionStrategy determines how models are selected based on hardware.
type ModelSelectionStrategy string

const (
	StrategySmallCombo  ModelSelectionStrategy = "small_combo"  // Use multiple small models (low RAM)
	StrategySingleLarge ModelSelectionStrategy = "single_large" // Use one large model (high RAM)
)

// TaskComplexityLevel represents the complexity level of a task
type TaskComplexityLevel int

const (
	ComplexityLow    TaskComplexityLevel = iota // Simple, single-file changes
	ComplexityMedium                           // Multi-file, requires understanding
	ComplexityHigh                             // Complex refactoring, architecture decisions
	ComplexityExpert                           // Expert-level: security, performance optimization
)

func (c TaskComplexityLevel) String() string {
	switch c {
	case ComplexityLow:
		return "low"
	case ComplexityMedium:
		return "medium"
	case ComplexityHigh:
		return "high"
	case ComplexityExpert:
		return "expert"
	default:
		return "unknown"
	}
}

// TaskComplexityConfig 控制不同复杂度任务的路由行为
type TaskComplexityConfig struct {
	Low    TierRecommendation `json:"low"`    // 低复杂度任务推荐
	Medium TierRecommendation `json:"medium"` // 中复杂度任务推荐
	High   TierRecommendation `json:"high"`   // 高复杂度任务推荐
	Expert TierRecommendation `json:"expert"` // 专家级任务推荐
}

// HardwareProfile 系统硬件配置
type HardwareProfile struct {
	CPUCores       int     `json:"cpuCores"`
	CPUPercent     float64 `json:"cpuPercent"`
	TotalRAMGB     float64 `json:"totalRamGB"`
	AvailableRAMGB float64 `json:"availableRamGB"`
	UsedRAMPercent float64 `json:"usedRamPercent"`
	GPUMemoryGB    float64 `json:"gpuMemoryGB"`
	HasGPU         bool    `json:"hasGpu"`
	GPUName        string  `json:"gpuName,omitempty"`
	GPUUtilization float64 `json:"gpuUtilization,omitempty"` // GPU 使用率百分比
	OS             string  `json:"os"`
}

// HardwareMonitor 提供实时硬件监控能力
type HardwareMonitor struct {
	mu               sync.RWMutex
	lastCheck        time.Time
	profile          *HardwareProfile
	monitoringActive bool
	stopCh           chan struct{}
	updateInterval   time.Duration
	app              *application.App // Wails 应用实例
}

// NewHardwareMonitor 创建新的硬件监控器
func NewHardwareMonitor() *HardwareMonitor {
	return &HardwareMonitor{
		profile:        &HardwareProfile{},
		updateInterval: 5 * time.Second,
		stopCh:         make(chan struct{}),
	}
}

// SetApp 设置 Wails 应用实例用于事件发射
func (hm *HardwareMonitor) SetApp(app *application.App) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.app = app
}

// StartMonitoring 启动后台硬件监控
func (hm *HardwareMonitor) StartMonitoring() {
	hm.mu.Lock()
	if hm.monitoringActive {
		hm.mu.Unlock()
		return
	}
	hm.monitoringActive = true
	stopCh := hm.stopCh // 捕获当前代 stopCh，避免与 StopMonitoring 的重建产生竞态
	hm.mu.Unlock()

	go func() {
		ticker := time.NewTicker(hm.updateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				hm.refreshProfile()
			case <-stopCh:
				return
			}
		}
	}()
}

// StopMonitoring 停止后台监控
func (hm *HardwareMonitor) StopMonitoring() {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.monitoringActive {
		close(hm.stopCh)
		hm.monitoringActive = false
		hm.stopCh = make(chan struct{})
	}
}

// GetProfile 返回最新的硬件配置（快照拷贝，避免调用方在读字段时与后台刷新产生数据竞争）
func (hm *HardwareMonitor) GetProfile() *HardwareProfile {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	snapshot := *hm.profile
	return &snapshot
}

// refreshProfile 刷新硬件配置
func (hm *HardwareMonitor) refreshProfile() {
	hm.mu.Lock()

	oldProfile := hm.profile
	hm.lastCheck = time.Now()

	// CPU 检测
	if percents, err := cpu.Percent(time.Second, false); err == nil && len(percents) > 0 {
		hm.profile.CPUPercent = percents[0]
	}

	// 内存检测
	if vmStat, err := mem.VirtualMemory(); err == nil {
		hm.profile.TotalRAMGB = float64(vmStat.Total) / 1024 / 1024 / 1024
		hm.profile.AvailableRAMGB = float64(vmStat.Available) / 1024 / 1024 / 1024
		hm.profile.UsedRAMPercent = vmStat.UsedPercent
	}

	// GPU 检测（增强版）
	hm.detectGPU()

	hm.profile.CPUCores = runtime.NumCPU()
	hm.profile.OS = runtime.GOOS

	// 检测重大变化并发射事件
	shouldEmit := false
	if oldProfile != nil {
		// 内存变化超过 10%
		memChange := abs(oldProfile.AvailableRAMGB - hm.profile.AvailableRAMGB)
		if memChange > oldProfile.AvailableRAMGB*0.1 {
			shouldEmit = true
		}
		// CPU 变化超过 20%
		if abs(oldProfile.CPUPercent-hm.profile.CPUPercent) > 20 {
			shouldEmit = true
		}
		// GPU 可用性变化
		if oldProfile.HasGPU != hm.profile.HasGPU {
			shouldEmit = true
		}
	}

	app := hm.app
	profile := hm.profile // 复制当前状态
	hm.mu.Unlock()

	// 发射事件
	if shouldEmit && app != nil {
		app.Event.Emit("router:hardware-changed", profile)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// detectGPU 增强的 GPU 检测逻辑
func (hm *HardwareMonitor) detectGPU() {
	// 首先检查环境变量
	if v := envconfig.EnvFloat("TIANCAN_GPU_MEMORY_GB", 0); v > 0 {
		hm.profile.GPUMemoryGB = v
		hm.profile.HasGPU = true
		if hm.profile.GPUName == "" {
			hm.profile.GPUName = envconfig.EnvString("TIANCAN_GPU_NAME")
		}
		return
	}

	// 尝试通过系统命令检测 NVIDIA GPU
	if hm.profile.GPUMemoryGB == 0 {
		if output, err := exec.Command("nvidia-smi", "--query-gpu=memory.total,memory.free,name,utilization.gpu",
			"--format=csv,noheader,nounits").CombinedOutput(); err == nil {
			parts := strings.Split(strings.TrimSpace(string(output)), ",")
			if len(parts) >= 3 {
				hm.profile.HasGPU = true
				if memTotal, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err == nil {
					hm.profile.GPUMemoryGB = memTotal / 1024 // MB to GB
				}
				hm.profile.GPUName = strings.TrimSpace(parts[2])
				if len(parts) >= 4 {
					if util, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64); err == nil {
						hm.profile.GPUUtilization = util
					}
				}
			}
		}
	}

	// 尝试检测 Apple Silicon GPU
	if runtime.GOOS == "darwin" && !hm.profile.HasGPU {
		if output, err := exec.Command("system_profiler", "SPDisplaysAgent", "-json").CombinedOutput(); err == nil {
			var report map[string]interface{}
			if json.Unmarshal(output, &report) == nil {
				if displays, ok := report["SPDisplaysAgent"].([]interface{}); ok && len(displays) > 0 {
					if d, ok := displays[0].(map[string]interface{}); ok {
						if vram, ok := d["spdisplays_vram"].(string); ok {
							// 提取 VRAM 大小 (e.g., "24 GB")
							if strings.Contains(vram, "GB") {
								hm.profile.HasGPU = true
								hm.profile.GPUName = "Apple Silicon GPU"
								if strings.Contains(vram, "Shared") {
									// 共享内存，估算可用
									hm.profile.GPUMemoryGB = 8 // 默认估算
								} else if v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(vram, "GB", "")), 64); err == nil {
									hm.profile.GPUMemoryGB = v
								}
							}
						}
					}
				}
			}
		}
	}
}

// TaskComplexity 任务复杂度评估
type TaskComplexity struct {
	Level        string  `json:"level"`        // "simple", "moderate", "complex"
	Score        float64 `json:"score"`        // 0-100
	TokensNeeded int     `json:"tokensNeeded"` // 预估 token 消耗
	Reason       string  `json:"reason"`
}

// ToComplexityLevel 将 TaskComplexity 转换为 TaskComplexityLevel
func (tc *TaskComplexity) ToComplexityLevel() TaskComplexityLevel {
	switch tc.Level {
	case "simple":
		return ComplexityLow
	case "moderate":
		return ComplexityMedium
	case "complex":
		return ComplexityHigh
	default:
		return ComplexityMedium
	}
}

// IsComplex 返回是否为复杂任务
func (tc *TaskComplexity) IsComplex() bool {
	return tc.Level == "complex" || tc.Score >= 60
}

// IsSimple 返回是否为简单任务
func (tc *TaskComplexity) IsSimple() bool {
	return tc.Level == "simple" || tc.Score < 30
}

// RecommendTierForComplexity 根据复杂度推荐最佳层级
func (tc *TaskComplexity) RecommendTierForComplexity() ModelTier {
	switch tc.Level {
	case "simple":
		return TierLocal // 简单任务用本地模型即可
	case "moderate":
		return TierAPI   // 中等任务用 API
	case "complex":
		return TierAPI   // 复杂任务用最强的 API
	default:
		return TierAPI
	}
}

// AssessTaskComplexity 评估任务复杂度以决定最优模型
func (sr *SmartRouter) AssessTaskComplexity(userMessage string) TaskComplexity {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	msgLen := len(userMessage)
	var score float64
	var level string
	var reason string
	var tokensNeeded int

	// 1. 基于消息长度评估
	score += float64(msgLen) / 10
	if msgLen > 5000 {
		score += 30
		reason = "长上下文需求"
		tokensNeeded = msgLen * 2 // 宽松估算
	} else if msgLen > 1000 {
		score += 15
		reason = "中等上下文需求"
		tokensNeeded = msgLen * 3
	} else {
		reason = "短上下文"
		tokensNeeded = msgLen * 4
	}

	// 2. 基于关键词评估
	lower := strings.ToLower(userMessage)
	complexKeywords := []string{
		"重写", "重构", "迁移", "优化", "性能", "设计模式",
		"架构", "安全", "加密", "并发", "分布式",
		"rewrite", "refactor", "migrate", "optimize", "architecture",
		"security", "concurrent", "distributed", "machine learning",
		"神经网络", "算法", "大项目",
	}
	simpleKeywords := []string{
		"简单", "快速", "修改", "fix", "typo", "变量名",
		"注释", "格式", "简单修复",
	}

	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			score += 20
		}
	}
	for _, kw := range simpleKeywords {
		if strings.Contains(lower, kw) {
			score -= 15
		}
	}

	// 3. 基于任务类型评估
	category := classifyTask(userMessage)
	switch category {
	case CategoryCodeEdit:
		score += 15
		tokensNeeded += 2000
	case CategoryDebug:
		score += 10
		tokensNeeded += 1000
	case CategoryResearch:
		score += 25
		tokensNeeded += 3000
	case CategoryCodeReview:
		score += 5
		tokensNeeded += 1500
	}

	// 4. 判断复杂度级别
	if score >= 60 {
		level = "complex"
		tokensNeeded += 3000
	} else if score >= 30 {
		level = "moderate"
	} else {
		level = "simple"
		tokensNeeded = min(tokensNeeded, 2000)
	}

	return TaskComplexity{
		Level:        level,
		Score:        score,
		TokensNeeded: tokensNeeded,
		Reason:       reason,
	}
}

// RouteWithComplexity 结合任务复杂度和硬件进行智能路由
func (sr *SmartRouter) RouteWithComplexity(userMessage string, providerScores map[string]float64) *TierRecommendation {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	category := classifyTask(userMessage)
	complexity := sr.assessTaskComplexityInternal(userMessage)
	hw := sr.getHardwareProfile()

	// 1. 计算本地模型分数（考虑 GPU 和任务复杂度）
	localScore := sr.calculateLocalScore(hw, complexity)

	// 2. 计算 API 模型分数（不受硬件限制）
	apiScore := sr.calculateAPIScore(complexity, providerScores)

	// 3. 计算 Web 模型分数
	webScore := sr.calculateWebScore(complexity, providerScores)

	// 4. 根据复杂度动态调整权重
	tierScores := map[ModelTier]float64{
		TierLocal: localScore,
		TierAPI:   apiScore,
		TierWeb:   webScore,
	}

	// 5. 复杂度敏感调整
	switch complexity.Level {
	case "simple":
		// 简单任务优先本地
		tierScores[TierLocal] *= 1.3
		tierScores[TierAPI] *= 0.8
	case "complex":
		// 复杂任务优先 API
		tierScores[TierAPI] *= 1.4
		tierScores[TierLocal] *= 0.7
		if complexity.TokensNeeded > 8000 {
			tierScores[TierWeb] *= 1.2 // 长上下文任务用 Web
		}
	}

	// 6. 找最佳 tier
	bestTier := TierAPI
	bestScore := tierScores[TierAPI]
	var warnings []string

	for tier, score := range tierScores {
		if score > bestScore {
			bestScore = score
			bestTier = tier
		}
	}

	// 7. 生成警告
	if tierScores[TierLocal] < 30 {
		if !hw.HasGPU && complexity.Level == "complex" {
			warnings = append(warnings, "复杂任务需要 GPU 支持，当前无 GPU")
		}
		if hw.AvailableRAMGB < 8 {
			warnings = append(warnings, fmt.Sprintf("内存不足 (%.1fGB)，本地模型可能性能受限", hw.AvailableRAMGB))
		}
	}

	// 8. 失败切换
	if sr.HasRecentFailure(bestTier) {
		for _, fallback := range sr.config.FallbackOrder {
			if fallback != bestTier && !sr.HasRecentFailure(fallback) {
				bestTier = fallback
				warnings = append(warnings, fmt.Sprintf("%s 最近失败，切换到 %s", bestTier, fallback))
				break
			}
		}
	}

	reason := sr.generateComplexityReason(bestTier, category, complexity, hw)

	return &TierRecommendation{
		Tier:     bestTier,
		Reason:   reason,
		Score:    bestScore,
		Warnings: warnings,
	}
}

// calculateLocalScore 计算本地模型分数
func (sr *SmartRouter) calculateLocalScore(hw *HardwareProfile, complexity TaskComplexity) float64 {
	// 基础分：内存充足度
	var score float64
	availableRAMRatio := hw.AvailableRAMGB / hw.TotalRAMGB

	if availableRAMRatio > 0.5 {
		score = 70
	} else if availableRAMRatio > 0.25 {
		score = 50
	} else {
		score = 20
	}

	// GPU 加成
	if hw.HasGPU {
		if hw.GPUMemoryGB >= 16 {
			score += 30
		} else if hw.GPUMemoryGB >= 8 {
			score += 20
		} else {
			score += 10
		}
		// GPU 利用率考虑
		if hw.GPUUtilization > 80 {
			score -= 20
		}
	}

	// 内存充足度调整
	if complexity.Level == "complex" {
		if hw.AvailableRAMGB < 16 {
			score -= 30
		} else if hw.AvailableRAMGB >= 32 {
			score += 10
		}
	}

	return score
}

// calculateAPIScore 计算 API 模型分数
func (sr *SmartRouter) calculateAPIScore(complexity TaskComplexity, providerScores map[string]float64) float64 {
	// 基础分：稳定性
	score := 60.0

	// Provider 可用性加成
	for providerID, pScore := range providerScores {
		if strings.Contains(providerID, "claude") || strings.Contains(providerID, "anthropic") {
			score += pScore * 0.5
			break
		}
	}

	// 复杂度加成
	if complexity.Level == "complex" {
		score += 20
	}

	return score
}

// calculateWebScore 计算 Web 模型分数
func (sr *SmartRouter) calculateWebScore(complexity TaskComplexity, providerScores map[string]float64) float64 {
	// 基础分
	score := 40.0

	// 长上下文任务加成
	if complexity.TokensNeeded > 5000 {
		score += 15
	}

	// 研究任务加成
	if complexity.Level == "complex" && classifyTask("") == CategoryResearch {
		score += 10
	}

	return score
}

// assessTaskComplexityInternal 内部任务复杂度评估
func (sr *SmartRouter) assessTaskComplexityInternal(msg string) TaskComplexity {
	msgLen := len(msg)
	var score float64

	score += float64(msgLen) / 10
	lower := strings.ToLower(msg)

	complexKeywords := []string{
		"重写", "重构", "迁移", "优化", "性能", "架构",
		"rewrite", "refactor", "architecture",
	}
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			score += 20
		}
	}

	tokensNeeded := msgLen * 3
	level := "simple"
	if score >= 60 {
		level = "complex"
		tokensNeeded += 3000
	} else if score >= 30 {
		level = "moderate"
	}

	return TaskComplexity{
		Level:        level,
		Score:        score,
		TokensNeeded: tokensNeeded,
	}
}

// generateComplexityReason 生成复杂度感知的路由原因
func (sr *SmartRouter) generateComplexityReason(tier ModelTier, category TaskCategory, complexity TaskComplexity, hw *HardwareProfile) string {
	complexityStr := map[string]string{"simple": "简单", "moderate": "中等", "complex": "复杂"}[complexity.Level]

	switch tier {
	case TierLocal:
		if hw.HasGPU {
			return fmt.Sprintf("使用本地 GPU 模型处理%s任务 (复杂度: %s, %s)", category, complexityStr, complexity.Reason)
		}
		return fmt.Sprintf("使用本地模型处理%s任务 (复杂度: %s, %s)", category, complexityStr, complexity.Reason)
	case TierAPI:
		return fmt.Sprintf("使用云端 API 处理%s任务 (复杂度: %s，需要 %d tokens)", category, complexityStr, complexity.TokensNeeded)
	case TierWeb:
		return fmt.Sprintf("使用 Web AI 处理%s任务 (复杂度: %s)", category, complexityStr)
	default:
		return "自动选择最佳 Provider"
	}
}

// TierRecommendation Provider 层级推荐
type TierRecommendation struct {
	Tier       ModelTier `json:"tier"`
	ProviderID string    `json:"providerId,omitempty"`
	Model      string    `json:"model,omitempty"`
	Reason     string    `json:"reason"`
	Score      float64   `json:"score"` // 0-100, higher is better
	Warnings   []string  `json:"warnings,omitempty"`
}

// TaskComplexityLevel 任务复杂度枚举（已在文件开头定义）

// RouteWithContext 增强版路由，考虑任务复杂度
func (sr *SmartRouter) RouteWithContext(userMessage string, providerScores map[string]float64, complexity TaskComplexityLevel) *TierRecommendation {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	category := classifyTask(userMessage)
	hw := sr.getHardwareProfile()

	// 1. 基础层级分数计算
	tierScores := sr.calculateTierScores(category, hw, providerScores)

	// 2. 根据任务复杂度调整
	tierScores = sr.adjustForComplexity(tierScores, complexity, hw)

	// 3. 找到最佳层级
	bestTier := sr.selectBestTier(tierScores)

	// 4. 生成警告和建议
	warnings := sr.generateWarnings(bestTier, hw, category)

	// 5. 检查失败历史
	bestTier = sr.checkFailureHistory(bestTier)

	reason := sr.generateReason(bestTier, category, hw)
	score := tierScores[bestTier]

	return &TierRecommendation{
		Tier:     bestTier,
		Reason:   reason,
		Score:    score,
		Warnings: warnings,
	}
}

// calculateTierScores 计算各层级基础分数
func (sr *SmartRouter) calculateTierScores(category TaskCategory, hw *HardwareProfile, providerScores map[string]float64) map[ModelTier]float64 {
	tierScores := map[ModelTier]float64{
		TierLocal: 30, // 基础分数
		TierAPI:   50,
		TierWeb:   40,
	}

	// 本地模型可用性检查
	localAvailable := sr.isLocalModelViable()
	if localAvailable {
		// 内存余量加成
		memRatio := hw.AvailableRAMGB / hw.TotalRAMGB
		tierScores[TierLocal] += memRatio * 25

		// GPU 加成
		if hw.HasGPU {
			tierScores[TierLocal] += 20
			// GPU 利用率调整
			if hw.GPUUtilization > 0 && hw.GPUUtilization < 80 {
				tierScores[TierLocal] += 10 // GPU 空闲时更有价值
			}
		}
	}

	// Provider 可用性调整
	for providerID, score := range providerScores {
		switch {
		case strings.Contains(providerID, "claude"):
			tierScores[TierAPI] += score * 0.25
		case strings.Contains(providerID, "lm-studio"), strings.Contains(providerID, "ollama"):
			tierScores[TierLocal] += score * 0.25
		case strings.Contains(providerID, "deepseek"), strings.Contains(providerID, "openai"):
			tierScores[TierAPI] += score * 0.2
		}
	}

	// 任务类型权重
	switch category {
	case CategoryCodeEdit, CategoryDebug:
		tierScores[TierAPI] *= 1.2 // API 模型更擅长代码
		tierScores[TierLocal] *= 1.0
	case CategoryResearch:
		tierScores[TierLocal] *= 1.1
		tierScores[TierWeb] *= 1.2 // Web 搜索研究更强
	case CategoryCodeReview:
		tierScores[TierLocal] *= 1.2 // 本地模型适合代码审查
	case CategoryChat:
		tierScores[TierLocal] *= 1.3 // 对话任务本地模型足够
	}

	return tierScores
}

// adjustForComplexity 根据复杂度调整分数
func (sr *SmartRouter) adjustForComplexity(scores map[ModelTier]float64, complexity TaskComplexityLevel, hw *HardwareProfile) map[ModelTier]float64 {
	switch complexity {
	case 1: // ComplexityLow
		// 简单任务：优先本地
		scores[TierLocal] *= 1.3
		scores[TierAPI] *= 0.9
	case 2: // ComplexityMedium
		// 中等任务：平衡
		scores[TierAPI] *= 1.1
	case 3: // ComplexityHigh
		// 复杂任务：需要更强的模型
		scores[TierAPI] *= 1.3
		if !hw.HasGPU {
			scores[TierLocal] *= 0.7 // 无 GPU 的本地模型不适合复杂任务
		}
	case 4: // ComplexityExpert
		// 专家任务：只使用最强模型
		scores[TierLocal] *= 0.5 // 本地模型通常不够
		scores[TierAPI] *= 1.5
		scores[TierWeb] *= 1.2
	}

	return scores
}

// selectBestTier 选择最佳层级
func (sr *SmartRouter) selectBestTier(scores map[ModelTier]float64) ModelTier {
	bestTier := TierAPI
	bestScore := scores[TierAPI]

	for tier, score := range scores {
		if score > bestScore {
			bestScore = score
			bestTier = tier
		}
	}

	return bestTier
}

// generateWarnings 生成警告信息
func (sr *SmartRouter) generateWarnings(tier ModelTier, hw *HardwareProfile, category TaskCategory) []string {
	var warnings []string

	if tier == TierLocal {
		if !hw.HasGPU {
			warnings = append(warnings, "本地 CPU 模型运行较慢，建议用于简单任务")
		}
		if hw.UsedRAMPercent > 80 {
			warnings = append(warnings, fmt.Sprintf("系统内存使用率较高 (%.0f%%)", hw.UsedRAMPercent))
		}
		if hw.CPUPercent > 80 {
			warnings = append(warnings, fmt.Sprintf("CPU 使用率较高 (%.0f%%)", hw.CPUPercent))
		}
	}

	if tier == TierAPI {
		if sr.HasRecentFailure(TierAPI) {
			warnings = append(warnings, "API 服务最近出现错误，可能不稳定")
		}
	}

	if category == CategoryCodeEdit && tier == TierLocal {
		warnings = append(warnings, "代码编辑任务建议使用更强的模型")
	}

	return warnings
}

// checkFailureHistory 检查失败历史并降级
func (sr *SmartRouter) checkFailureHistory(tier ModelTier) ModelTier {
	if !sr.HasRecentFailure(tier) {
		return tier
	}

	// 按优先级尝试降级
	order := []ModelTier{TierAPI, TierWeb, TierLocal}
	if tier == TierLocal {
		order = []ModelTier{TierAPI, TierWeb}
	}

	for _, fallback := range order {
		if fallback != tier && !sr.HasRecentFailure(fallback) {
			return fallback
		}
	}

	return tier // 无可用降级
}

// SmartRouterConfig holds the configuration for the Smart Router.
type SmartRouterConfig struct {
	AutoDowngrade       bool                   `json:"autoDowngrade"`       // Enable auto-downgrade on failure (API/Web fallback)
	PreferredTier       ModelTier              `json:"preferredTier"`       // User's preferred tier
	FallbackOrder       []ModelTier            `json:"fallbackOrder"`       // Order to try on failure
	TotalMemoryGB       float64                `json:"totalMemoryGB"`       // Total system RAM in GB (auto-detected)
	AvailableMemoryGB   float64                `json:"availableMemoryGB"`   // Currently available RAM in GB
	GPUMemoryGB         float64                `json:"gpuMemoryGB"`         // GPU VRAM in GB (from TIANCAN_GPU_MEMORY_GB env)
	CPUCores            int                    `json:"cpuCores"`            // Number of CPU cores (auto-detected)
	Strategy            ModelSelectionStrategy `json:"strategy"`            // Model selection strategy based on hardware
	SystemLoadThreshold float64                `json:"systemLoadThreshold"` // Memory usage % threshold for killing LM Studio (default 0.9)
	App                 *application.App       `json:"-"`                   // For emitting events to frontend
}

// DefaultConfig returns the default Smart Router configuration with hardware detection.
func DefaultConfig() SmartRouterConfig {
	cfg := SmartRouterConfig{
		AutoDowngrade:       true,
		PreferredTier:       TierLocal,
		FallbackOrder:       []ModelTier{TierLocal, TierAPI, TierWeb},
		SystemLoadThreshold: 0.9, // Kill LM Studio only when system is critically low
	}
	cfg.detectHardware()
	return cfg
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
// based on task category, user preferences, hardware, and failure history.
// For local models: hardware-aware gating (grey out models that won't fit in RAM).
// When ALL local models are unavailable: kill LM Studio to free resources.
// For API/Web tiers: simple failure-based fallback only.
type SmartRouter struct {
	mu              sync.RWMutex
	config          SmartRouterConfig
	failures        []FailureRecord      // recent failure records (last 10)
	modelStatuses   []LocalModelStatus   // cached model availability from last check
	hwMonitor       *HardwareMonitor     // 实时硬件监控
	loadHistory     []LoadSnapshot       // 历史负载数据用于预测
	providerLatency map[string]float64   // 各 provider 的延迟记录
	app             *application.App     // Wails 应用实例
}

// LoadSnapshot 负载历史快照
type LoadSnapshot struct {
	Timestamp   time.Time `json:"timestamp"`
	CPUPercent  float64   `json:"cpuPercent"`
	RAMPercent  float64   `json:"ramPercent"`
	GPUMemUsed  float64   `json:"gpuMemUsedGB"`
	QueueLength int       `json:"queueLength"`
}

// StartHardwareMonitoring 启动硬件监控
func (sr *SmartRouter) StartHardwareMonitoring() {
	if sr.hwMonitor == nil {
		sr.hwMonitor = NewHardwareMonitor()
	}
	sr.hwMonitor.StartMonitoring()
}

// StopHardwareMonitoring 停止硬件监控
func (sr *SmartRouter) StopHardwareMonitoring() {
	if sr.hwMonitor != nil {
		sr.hwMonitor.StopMonitoring()
	}
}

// GetRealTimeHardware 返回实时硬件状态
func (sr *SmartRouter) GetRealTimeHardware() *HardwareProfile {
	if sr.hwMonitor != nil {
		return sr.hwMonitor.GetProfile()
	}
	return sr.getHardwareProfile()
}

// PredictLoad 预测未来负载（用于主动调度）
func (sr *SmartRouter) PredictLoad(lookaheadSeconds int) (cpu, ram float64) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	if len(sr.loadHistory) < 3 {
		// 数据不足，返回当前值
		hw := sr.getHardwareProfile()
		return hw.CPUPercent, hw.UsedRAMPercent
	}

	// 简单移动平均预测
	var cpuSum, ramSum float64
	recentCount := min(len(sr.loadHistory), 5)
	for i := len(sr.loadHistory) - recentCount; i < len(sr.loadHistory); i++ {
		cpuSum += sr.loadHistory[i].CPUPercent
		ramSum += sr.loadHistory[i].RAMPercent
	}
	return cpuSum / float64(recentCount), ramSum / float64(recentCount)
}

// RecordLoad 记录当前负载快照
func (sr *SmartRouter) RecordLoad(queueLength int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	hw := sr.getHardwareProfile()
	snapshot := LoadSnapshot{
		Timestamp:   time.Now(),
		CPUPercent:  hw.CPUPercent,
		RAMPercent:  hw.UsedRAMPercent,
		GPUMemUsed:  hw.GPUMemoryGB * 0.5, // 估算
		QueueLength: queueLength,
	}
	sr.loadHistory = append(sr.loadHistory, snapshot)

	// 保持历史不超过 100 条
	if len(sr.loadHistory) > 100 {
		sr.loadHistory = sr.loadHistory[len(sr.loadHistory)-100:]
	}
}

// RecordProviderLatency 记录 provider 响应延迟
func (sr *SmartRouter) RecordProviderLatency(providerID string, latencyMs float64) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.providerLatency == nil {
		sr.providerLatency = make(map[string]float64)
	}
	// 指数移动平均
	if prev, ok := sr.providerLatency[providerID]; ok {
		sr.providerLatency[providerID] = prev*0.7 + latencyMs*0.3
	} else {
		sr.providerLatency[providerID] = latencyMs
	}
}

// GetProviderLatency 获取 provider 平均延迟
func (sr *SmartRouter) GetProviderLatency(providerID string) float64 {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.providerLatency[providerID]
}

// NewSmartRouter creates a new Smart Router with the given config.
func NewSmartRouter(cfg SmartRouterConfig) *SmartRouter {
	return &SmartRouter{
		config:   cfg,
		failures: make([]FailureRecord, 0, 10),
	}
}

// Route determines the best model tier for a given user message.
func (sr *SmartRouter) Route(userMessage string) RouteResult {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	category := classifyTask(userMessage)
	confidence := taskConfidence(userMessage, category)
	tier := sr.config.PreferredTier

	if tier == TierLocal {
		hasAvailable := false
		for _, ms := range sr.modelStatuses {
			if ms.Available {
				hasAvailable = true
				break
			}
		}

		if !hasAvailable || sr.isSystemCriticallyOverloaded() {
			if sr.config.AutoDowngrade {
				for _, fallback := range sr.config.FallbackOrder {
					if fallback != TierLocal && !sr.HasRecentFailure(fallback) {
						reason := "系统资源不足，无法运行本地模型"
						if sr.isSystemCriticallyOverloaded() {
							reason = fmt.Sprintf("系统内存占用超过%.0f%%，本地模型无法运行", sr.config.SystemLoadThreshold*100)
						}
						return RouteResult{
							Tier:       fallback,
							Category:   category,
							Confidence: confidence,
							Reason:     reason,
							Fallback:   true,
						}
					}
				}
			}
		}
	}

	if sr.config.AutoDowngrade && sr.HasRecentFailure(tier) {
		for _, fallback := range sr.config.FallbackOrder {
			if fallback != tier && !sr.HasRecentFailure(fallback) {
				return RouteResult{
					Tier:       fallback,
					Category:   category,
					Confidence: confidence,
					Reason:     fmt.Sprintf("%s 最近出错，切换到 %s", tier, fallback),
					Fallback:   true,
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

// RouteWithProviders 根据可用 Provider 智能路由请求
func (sr *SmartRouter) RouteWithProviders(userMessage string, providerScores map[string]float64) *TierRecommendation {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	category := classifyTask(userMessage)
	hw := sr.getHardwareProfile()

	// 1. 检查系统资源是否允许本地模型
	localAvailable := sr.isLocalModelViable()
	localScore := 0.0
	if localAvailable {
		localScore = 50 + (hw.AvailableRAMGB / hw.TotalRAMGB * 30) // 基础分 + 内存余量
		if hw.HasGPU {
			localScore += 20 // GPU 加成
		}
	}

	// 2. 计算各层级分数
	tierScores := map[ModelTier]float64{
		TierLocal: localScore,
		TierAPI:   60, // API 通常稳定
		TierWeb:   40, // Web 可能需要登录
	}

	// 3. 根据 Provider 可用性调整分数
	for providerID, score := range providerScores {
		// 简单映射：Claude Code/Desktop → TierAPI, 本地服务 → TierLocal
		if strings.Contains(providerID, "claude-code") || strings.Contains(providerID, "claude-desktop") {
			tierScores[TierAPI] += score * 0.3
		} else if strings.Contains(providerID, "lm-studio") || strings.Contains(providerID, "ollama") {
			tierScores[TierLocal] += score * 0.3
		}
	}

	// 4. 根据任务类型调整权重
	switch category {
	case CategoryCodeEdit, CategoryDebug:
		// 代码编辑需要高质量模型
		tierScores[TierAPI] *= 1.2
		tierScores[TierLocal] *= 1.0
	case CategoryResearch:
		// 研究任务可以用本地模型
		tierScores[TierLocal] *= 1.1
		tierScores[TierWeb] *= 1.2
	case CategoryChat, CategoryCodeReview:
		// 对话和代码审查本地模型足够
		tierScores[TierLocal] *= 1.2
	}

	// 5. 找到最佳层级
	bestTier := TierAPI
	bestScore := tierScores[TierAPI]
	var warnings []string

	for tier, score := range tierScores {
		if score > bestScore {
			bestScore = score
			bestTier = tier
		}
	}

	// 6. 生成警告信息
	if !localAvailable {
		warnings = append(warnings, "本地模型不可用：系统资源不足")
	} else if hw.UsedRAMPercent > 80 {
		warnings = append(warnings, fmt.Sprintf("系统内存使用率较高 (%.0f%%)，本地模型可能较慢", hw.UsedRAMPercent))
	}
	if hw.CPUPercent > 80 {
		warnings = append(warnings, fmt.Sprintf("CPU 使用率较高 (%.0f%%)", hw.CPUPercent))
	}

	// 7. 检查最近的失败
	if sr.HasRecentFailure(bestTier) {
		for _, fallback := range sr.config.FallbackOrder {
			if fallback != bestTier && !sr.HasRecentFailure(fallback) {
				bestTier = fallback
				warnings = append(warnings, fmt.Sprintf("%s 最近失败，自动切换到 %s", bestTier, fallback))
				break
			}
		}
	}

	reason := sr.generateReason(bestTier, category, hw)

	return &TierRecommendation{
		Tier:     bestTier,
		Reason:   reason,
		Score:    bestScore,
		Warnings: warnings,
	}
}

// RouteWithComplexityByLevel 根据任务复杂度级别智能路由请求
// 这是核心方法，综合考虑任务复杂度、硬件配置和 Provider 可用性
func (sr *SmartRouter) RouteWithComplexityByLevel(userMessage string, providerScores map[string]float64, complexity TaskComplexityLevel) *TierRecommendation {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	_ = classifyTask(userMessage) // 保留用于未来扩展
	hw := sr.getHardwareProfile()

	// 1. 分析任务复杂度（如果未指定，自动分析）
	if complexity == 0 {
		complexity = sr.analyzeTaskComplexity(userMessage)
	}

	// 2. 评估各 Provider 可用性和分数
	providerScoresResult := sr.evaluateProviders(providerScores, hw)

	// 3. 根据复杂度选择最佳 Provider
	var tier ModelTier
	var score float64
	var reason string
	var warnings []string

	switch complexity {
	case ComplexityLow:
		// 简单任务：优先本地/小模型
		tier, score, reason, warnings = sr.selectForLowComplexity(providerScoresResult, hw)
	case ComplexityMedium:
		// 中等任务：平衡质量和资源
		tier, score, reason, warnings = sr.selectForMediumComplexity(providerScoresResult, hw)
	case ComplexityHigh:
		// 复杂任务：优先高质量
		tier, score, reason, warnings = sr.selectForHighComplexity(providerScoresResult, hw)
	case ComplexityExpert:
		// 专家级任务：必须使用最强模型
		tier, score, reason, warnings = sr.selectForExpertComplexity(providerScoresResult, hw)
	}

	// 4. 应用硬件约束
	if tier == TierLocal && !sr.isLocalModelViable() {
		warnings = append(warnings, "本地模型不可用，降级到 API")
		tier = TierAPI
		score = 40
		reason = "硬件限制，切换到 API"
	}

	// 5. 检查失败历史
	if sr.HasRecentFailure(tier) {
		for _, fallback := range sr.config.FallbackOrder {
			if fallback != tier && !sr.HasRecentFailure(fallback) {
				warnings = append(warnings, fmt.Sprintf("%s 最近失败，自动切换", tier))
				tier = fallback
				reason += " (自动降级)"
				break
			}
		}
	}

	return &TierRecommendation{
		Tier:     tier,
		Reason:   fmt.Sprintf("[%s] %s", complexity.String(), reason),
		Score:    score,
		Warnings: warnings,
	}
}

// ProviderScore 描述每个 Provider 的评估结果
type ProviderScore struct {
	ProviderID string
	Tier       ModelTier
	Score      float64
	LatencyMs  float64
	Available  bool
}

// evaluateProviders 评估所有 Provider 的状态
func (sr *SmartRouter) evaluateProviders(providerScores map[string]float64, hw *HardwareProfile) map[ModelTier]*ProviderScore {
	result := map[ModelTier]*ProviderScore{
		TierLocal: {Tier: TierLocal, Score: 0, Available: false},
		TierAPI:   {Tier: TierAPI, Score: 50, Available: true},
		TierWeb:   {Tier: TierWeb, Score: 30, Available: true},
	}

	// 评估本地模型
	if sr.isLocalModelViable() {
		result[TierLocal].Available = true
		result[TierLocal].Score = 50 + (hw.AvailableRAMGB/hw.TotalRAMGB)*30
		if hw.HasGPU {
			result[TierLocal].Score += 20
		}
		// 从 providerScores 获取本地模型评分
		for id, s := range providerScores {
			if strings.Contains(id, "lm-studio") || strings.Contains(id, "ollama") {
				result[TierLocal].Score += s * 0.5
				result[TierLocal].ProviderID = id
			}
		}
	}

	// 评估 API
	for id, s := range providerScores {
		if strings.Contains(id, "openai") || strings.Contains(id, "anthropic") ||
			strings.Contains(id, "deepseek") || strings.Contains(id, "claude") {
			result[TierAPI].Score += s * 0.5
			if result[TierAPI].ProviderID == "" {
				result[TierAPI].ProviderID = id
			}
		}
	}

	// 评估 Web
	for id, s := range providerScores {
		if strings.Contains(id, "web") || strings.Contains(id, "browser") {
			result[TierWeb].Score += s * 0.5
			if result[TierWeb].ProviderID == "" {
				result[TierWeb].ProviderID = id
			}
		}
	}

	// 应用延迟惩罚
	for tier, p := range result {
		if latency := sr.providerLatency[p.ProviderID]; latency > 0 {
			latencyPenalty := min(latency/1000, 0.3) // 最多 30% 惩罚
			result[tier].Score *= (1 - latencyPenalty)
		}
	}

	return result
}

// selectForLowComplexity 低复杂度任务选择
func (sr *SmartRouter) selectForLowComplexity(providers map[ModelTier]*ProviderScore, hw *HardwareProfile) (ModelTier, float64, string, []string) {
	if providers[TierLocal].Available && providers[TierLocal].Score > 50 {
		return TierLocal, providers[TierLocal].Score,
			"简单任务使用本地模型响应更快", nil
	}
	return TierAPI, providers[TierAPI].Score,
		"本地模型不可用，使用 API", nil
}

// selectForMediumComplexity 中等复杂度任务选择
func (sr *SmartRouter) selectForMediumComplexity(providers map[ModelTier]*ProviderScore, hw *HardwareProfile) (ModelTier, float64, string, []string) {
	var warnings []string

	// 本地模型足够好就用
	if providers[TierLocal].Available && providers[TierLocal].Score > 60 {
		return TierLocal, providers[TierLocal].Score,
			"中等复杂度任务使用本地模型", warnings
	}

	// 否则用 API
	if !providers[TierAPI].Available {
		warnings = append(warnings, "API 不可用，考虑使用 Web")
	}
	return TierAPI, providers[TierAPI].Score,
		"中等复杂度使用 API 质量更稳定", warnings
}

// selectForHighComplexity 高复杂度任务选择
func (sr *SmartRouter) selectForHighComplexity(providers map[ModelTier]*ProviderScore, hw *HardwareProfile) (ModelTier, float64, string, []string) {
	var warnings []string

	// 高复杂度优先 API
	if providers[TierAPI].Score > providers[TierLocal].Score {
		return TierAPI, providers[TierAPI].Score,
			"高复杂度任务需要高质量模型", warnings
	}

	// 本地 GPU 足够强也行
	if hw.HasGPU && hw.GPUMemoryGB >= 8 {
		return TierLocal, providers[TierLocal].Score,
			"本地 GPU 足够强，高复杂度任务使用本地模型", warnings
	}

	warnings = append(warnings, "建议使用云端 API 以获得最佳效果")
	return TierAPI, providers[TierAPI].Score,
		"高复杂度任务使用 API", warnings
}

// selectForExpertComplexity 专家级任务选择
func (sr *SmartRouter) selectForExpertComplexity(providers map[ModelTier]*ProviderScore, hw *HardwareProfile) (ModelTier, float64, string, []string) {
	var warnings []string

	// 专家级任务必须使用最强模型
	// 检查是否有高端 API
	if providers[TierAPI].ProviderID != "" {
		return TierAPI, providers[TierAPI].Score + 20,
			"专家级任务使用最强 API 模型", warnings
	}

	// Web 可能更好（Claude Web 等）
	if providers[TierWeb].Available {
		warnings = append(warnings, "API 不可用，降级到 Web")
		return TierWeb, providers[TierWeb].Score,
			"专家级任务使用 Web AI", warnings
	}

	// 最后检查本地
	if providers[TierLocal].Available && hw.HasGPU && hw.GPUMemoryGB >= 16 {
		return TierLocal, providers[TierLocal].Score,
			"本地 GPU 足够强，专家级任务使用本地模型", warnings
	}

	warnings = append(warnings, "警告：当前硬件可能无法满足专家级任务需求")
	return TierAPI, 20,
		"硬件限制，建议升级硬件以获得最佳效果", warnings
}

// analyzeTaskComplexity 自动分析任务复杂度
func (sr *SmartRouter) analyzeTaskComplexity(message string) TaskComplexityLevel {
	lower := strings.ToLower(message)

	// 专家级关键词
	expertKeywords := []string{"安全", "安全漏洞", "security", "漏洞", "vulnerability",
		"性能优化", "performance optimization", "架构设计", "architecture",
		"重构大型系统", "迁移", "migration"}
	for _, kw := range expertKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityExpert
		}
	}

	// 高复杂度关键词
	highKeywords := []string{"重构", "refactor", "重写", "rewrite",
		"复杂", "complex", "多文件", "multi-file",
		"设计模式", "design pattern", "算法", "algorithm",
		"并发", "concurrency", "分布式", "distributed"}
	for _, kw := range highKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityHigh
		}
	}

	// 中等复杂度关键词
	mediumKeywords := []string{"修改", "fix", "修复", "add", "添加",
		"实现", "implement", "创建", "create",
		"多个", "several", "多个文件"}
	for _, kw := range mediumKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityMedium
		}
	}

	// 默认低复杂度
	return ComplexityLow
}

// GetRecommendedModelsForComplexity 返回适合特定复杂度的模型推荐
func (sr *SmartRouter) GetRecommendedModelsForComplexity(complexity TaskComplexityLevel) []string {
	switch complexity {
	case ComplexityLow:
		return []string{
			"llama3.2:1b", "qwen2.5:0.5b", "phi3-mini",
			"gpt-4o-mini", "claude-3-haiku",
		}
	case ComplexityMedium:
		return []string{
			"llama3.1:8b", "qwen2.5:7b", "mistral-nemo",
			"gpt-4o", "claude-3-sonnet",
		}
	case ComplexityHigh:
		return []string{
			"llama3.1:70b", "qwen2.5-coder:32b", "codellama:34b",
			"gpt-4o", "claude-3.5-sonnet",
		}
	case ComplexityExpert:
		return []string{
			"claude-opus-4", "gpt-4-turbo", "gemini-pro",
			"claude-3.5-sonnet", "deepseek-coder-v2",
		}
	default:
		return []string{"claude-3-sonnet", "gpt-4o"}
	}
}

// RecommendProvider 根据硬件推荐最佳 Provider
func (sr *SmartRouter) RecommendProvider(hw *HardwareProfile) string {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	if hw == nil {
		hw = sr.getHardwareProfile()
	}

	// 根据内存推荐
	switch {
	case hw.AvailableRAMGB >= 32 && hw.HasGPU:
		return "lm-studio" // 大内存 + GPU 用 LM Studio
	case hw.AvailableRAMGB >= 16:
		return "ollama" // 中等内存用 Ollama
	case hw.AvailableRAMGB >= 8:
		return "ollama-small" // 小内存用小模型
	default:
		return "api" // 资源不足用 API
	}
}

// GetHardwareProfile 返回当前硬件配置
func (sr *SmartRouter) GetHardwareProfile() *HardwareProfile {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.getHardwareProfile()
}

// getHardwareProfile 内部方法，获取硬件配置
func (sr *SmartRouter) getHardwareProfile() *HardwareProfile {
	var cpuPercent float64
	if percents, err := cpu.Percent(time.Second, false); err == nil && len(percents) > 0 {
		cpuPercent = percents[0]
	}

	return &HardwareProfile{
		CPUCores:       sr.config.CPUCores,
		CPUPercent:     cpuPercent,
		TotalRAMGB:     sr.config.TotalMemoryGB,
		AvailableRAMGB: sr.config.AvailableMemoryGB,
		UsedRAMPercent: sr.calculateUsedRAMPercent(),
		GPUMemoryGB:    sr.config.GPUMemoryGB,
		HasGPU:         sr.config.GPUMemoryGB > 0,
		OS:             runtime.GOOS,
	}
}

// calculateUsedRAMPercent 计算已用内存百分比
func (sr *SmartRouter) calculateUsedRAMPercent() float64 {
	if sr.config.TotalMemoryGB > 0 && sr.config.AvailableMemoryGB > 0 {
		used := sr.config.TotalMemoryGB - sr.config.AvailableMemoryGB
		return (used / sr.config.TotalMemoryGB) * 100
	}
	return 0
}

// isLocalModelViable 检查本地模型是否可行
func (sr *SmartRouter) isLocalModelViable() bool {
	// 检查是否有可用模型
	for _, ms := range sr.modelStatuses {
		if ms.Available {
			return true
		}
	}

	// 检查系统资源
	if sr.isSystemCriticallyOverloaded() {
		return false
	}

	// 检查是否有足够内存（至少 4GB 可用）
	if sr.config.AvailableMemoryGB < 4 {
		return false
	}

	return false
}

// generateReason 生成路由原因说明
func (sr *SmartRouter) generateReason(tier ModelTier, category TaskCategory, hw *HardwareProfile) string {
	switch tier {
	case TierLocal:
		if hw.HasGPU {
			return fmt.Sprintf("使用本地 GPU 模型 (%.0fGB VRAM)，适合 %s 任务", hw.GPUMemoryGB, category)
		}
		return fmt.Sprintf("使用本地 CPU 模型 (%.1fGB 可用内存)，适合 %s 任务", hw.AvailableRAMGB, category)
	case TierAPI:
		return fmt.Sprintf("使用云端 API，适合 %s 任务", category)
	case TierWeb:
		return fmt.Sprintf("使用 Web AI，适合 %s 任务", category)
	default:
		return "自动选择最佳 Provider"
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

// UpdateModelStatuses updates the cached local model availability from AI service.
func (sr *SmartRouter) UpdateModelStatuses(statuses []LocalModelStatus) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.modelStatuses = statuses

	// Emit event to frontend so model dropdown can grey out unavailable models
	if sr.config.App != nil {
		sr.config.App.Event.Emit("router:local-models-updated", statuses)
	}

	// Check if ALL local models are unavailable → should kill LM Studio
	allUnavailable := true
	for _, ms := range statuses {
		if ms.Available {
			allUnavailable = false
			break
		}
	}
	if allUnavailable && len(statuses) > 0 && sr.config.App != nil {
		sr.config.App.Event.Emit("router:kill-lm-studio", "所有本地模型均不可用，建议关闭 LM Studio 释放内存")
	}
}

// GetModelStatuses returns cached local model availability.
func (sr *SmartRouter) GetModelStatuses() []LocalModelStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	result := make([]LocalModelStatus, len(sr.modelStatuses))
	copy(result, sr.modelStatuses)
	return result
}

// RefreshHardwareState re-detects system memory and updates model statuses.
func (sr *SmartRouter) RefreshHardwareState() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.config.detectHardware()

	// Re-evaluate model availability with new memory info
	for i := range sr.modelStatuses {
		ms := &sr.modelStatuses[i]
		ms.Available = true
		if ms.MinRAMGB > 0 && sr.config.AvailableMemoryGB > 0 && ms.MinRAMGB > sr.config.AvailableMemoryGB {
			ms.Available = false
			ms.DisableReason = fmt.Sprintf("需要 %.0fGB 内存，当前可用 %.0fGB", ms.MinRAMGB, sr.config.AvailableMemoryGB)
		}
	}

	if sr.config.App != nil {
		sr.config.App.Event.Emit("router:local-models-updated", sr.modelStatuses)
	}
}

// HasRecentFailure checks if a tier has had a failure in the last 5 minutes.
func (sr *SmartRouter) HasRecentFailure(tier ModelTier) bool {
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, f := range sr.failures {
		if f.Tier == tier && f.Timestamp.After(cutoff) {
			return true
		}
	}
	return false
}

// classifyTask uses keyword matching to determine the task category.
func classifyTask(msg string) TaskCategory {
	lower := strings.ToLower(msg)
	keywords := loadRouteKeywords()

	for _, kw := range keywords[CategoryCodeEdit] {
		if strings.Contains(lower, kw) {
			return CategoryCodeEdit
		}
	}
	for _, kw := range keywords[CategoryResearch] {
		if strings.Contains(lower, kw) {
			return CategoryResearch
		}
	}
	for _, kw := range keywords[CategoryDebug] {
		if strings.Contains(lower, kw) {
			return CategoryDebug
		}
	}
	for _, kw := range keywords[CategoryCodeReview] {
		if strings.Contains(lower, kw) {
			return CategoryCodeReview
		}
	}

	return CategoryChat
}

// taskConfidence returns a confidence score (0-1) for the classification.
func taskConfidence(msg string, category TaskCategory) float64 {
	if category == CategoryChat {
		return 0.5
	}
	msgLen := len(msg)
	if msgLen < 10 {
		return 0.3
	}
	if msgLen > 100 {
		return 0.8
	}
	return 0.6
}

// loadRouteKeywords loads task classification keywords from TIANCAN_ROUTE_KEYWORDS env var.
var routeKeywordsCache map[TaskCategory][]string
var routeKeywordsOnce sync.Once

func loadRouteKeywords() map[TaskCategory][]string {
	routeKeywordsOnce.Do(func() {
		routeKeywordsCache = defaultKeywords()
		if envVal := envconfig.EnvString("TIANCAN_ROUTE_KEYWORDS"); envVal != "" {
			var parsed map[TaskCategory][]string
			if err := json.Unmarshal([]byte(envVal), &parsed); err == nil {
				routeKeywordsCache = parsed
			}
		}
	})
	return routeKeywordsCache
}

func defaultKeywords() map[TaskCategory][]string {
	return map[TaskCategory][]string{
		CategoryCodeEdit:   {"修改", "改", "写", "创建", "添加", "删除", "重构", "实现", "fix", "write", "create", "edit", "modify", "delete", "refactor", "implement"},
		CategoryResearch:   {"研究", "搜索", "查找资料", "调研", "research", "search", "investigate", "look up"},
		CategoryDebug:      {"调试", "断点", "报错", "错误", "bug", "debug", "breakpoint", "error", "crash", "trace"},
		CategoryCodeReview: {"审查", "检查", "分析", "解释", "review", "check", "analyze", "explain", "read"},
	}
}

// detectHardware populates hardware fields from system info.
func (c *SmartRouterConfig) detectHardware() {
	if c.CPUCores == 0 {
		c.CPUCores = runtime.NumCPU()
	}
	if c.TotalMemoryGB == 0 {
		if vmStat, err := mem.VirtualMemory(); err == nil {
			c.TotalMemoryGB = float64(vmStat.Total) / 1024 / 1024 / 1024
			c.AvailableMemoryGB = float64(vmStat.Available) / 1024 / 1024 / 1024
		}
	}
	// Determine strategy based on RAM
	if c.Strategy == "" {
		if c.TotalMemoryGB >= 16 {
			c.Strategy = StrategySingleLarge
		} else {
			c.Strategy = StrategySmallCombo
		}
	}
	// GPU memory: read from TIANCAN_GPU_MEMORY_GB env if not set
	if c.GPUMemoryGB == 0 {
		if v := envconfig.EnvFloat("TIANCAN_GPU_MEMORY_GB", 0); v > 0 {
			c.GPUMemoryGB = v
		}
	}
}

// isSystemCriticallyOverloaded checks if memory usage is critically high.
func (sr *SmartRouter) isSystemCriticallyOverloaded() bool {
	if sr.config.SystemLoadThreshold <= 0 {
		sr.config.SystemLoadThreshold = 0.9
	}
	if vmStat, err := mem.VirtualMemory(); err == nil {
		return vmStat.UsedPercent/100.0 >= sr.config.SystemLoadThreshold
	}
	return false
}

// GetSystemStatus 返回系统状态摘要
func (sr *SmartRouter) GetSystemStatus() map[string]interface{} {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	hw := sr.getHardwareProfile()
	localModelsCount := 0
	availableModelsCount := 0
	for _, ms := range sr.modelStatuses {
		localModelsCount++
		if ms.Available {
			availableModelsCount++
		}
	}

	return map[string]interface{}{
		"hardware":             hw,
		"localModelsTotal":     localModelsCount,
		"localModelsAvailable": availableModelsCount,
		"strategy":             sr.config.Strategy,
		"autoDowngrade":        sr.config.AutoDowngrade,
		"preferredTier":        sr.config.PreferredTier,
		"recentFailures":       len(sr.failures),
	}
}

// GetDetailedHardwareStatus 返回详细的硬件状态（用于前端仪表盘）
func (sr *SmartRouter) GetDetailedHardwareStatus() map[string]interface{} {
	hw := sr.GetRealTimeHardware()

	// 获取预测负载
	predictedCPU, predictedRAM := sr.PredictLoad(30) // 30秒后的预测

	return map[string]interface{}{
		"current": hw,
		"predicted": map[string]interface{}{
			"cpuPercent":  predictedCPU,
			"ramPercent":  predictedRAM,
			"lookaheadS":  30,
		},
		"recommendation": sr.RecommendProvider(hw),
		"localModels":    sr.GetModelStatuses(),
	}
}

// RouteWithDetails 带完整上下文信息的路由（用于调试和展示）
func (sr *SmartRouter) RouteWithDetails(userMessage string, providerScores map[string]float64) map[string]interface{} {
	result := sr.RouteWithProviders(userMessage, providerScores)
	complexity := sr.AssessTaskComplexity(userMessage)
	hw := sr.GetRealTimeHardware()

	return map[string]interface{}{
		"route":      result,
		"complexity": complexity,
		"hardware":   hw,
		"category":   classifyTask(userMessage),
	}
}

// GetProviderHealthStatus 返回各 provider 的健康状态
func (sr *SmartRouter) GetProviderHealthStatus() map[string]interface{} {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	health := make(map[string]interface{})
	for providerID, latency := range sr.providerLatency {
		status := "healthy"
		if latency > 5000 {
			status = "degraded"
		}
		if latency > 10000 {
			status = "unhealthy"
		}
		health[providerID] = map[string]interface{}{
			"latencyMs": latency,
			"status":    status,
		}
	}

	// 添加本地模型状态
	localModels := sr.GetModelStatuses()
	var availableLocal, totalLocal int
	for _, m := range localModels {
		totalLocal++
		if m.Available {
			availableLocal++
		}
	}

	health["local"] = map[string]interface{}{
		"total":     totalLocal,
		"available": availableLocal,
		"status":    "healthy",
	}
	if availableLocal == 0 && totalLocal > 0 {
		health["local"].(map[string]interface{})["status"] = "degraded"
	}

	return health
}

// IsSystemHealthy 检查系统是否健康（用于自动降级决策）
func (sr *SmartRouter) IsSystemHealthy() bool {
	hw := sr.GetRealTimeHardware()

	// 检查内存
	if hw.UsedRAMPercent > 90 {
		return false
	}

	// 检查 CPU
	if hw.CPUPercent > 95 {
		return false
	}

	// 检查 GPU
	if hw.HasGPU && hw.GPUUtilization > 95 {
		return false
	}

	return true
}

// GetMemoryPressureLevel 返回内存压力等级 (0-100)
func (sr *SmartRouter) GetMemoryPressureLevel() int {
	hw := sr.GetRealTimeHardware()

	// 计算内存压力
	basePressure := hw.UsedRAMPercent

	// 如果有本地模型运行，增加压力感知
	if hw.HasGPU && hw.GPUMemoryGB > 0 {
		// GPU 内存使用也计入压力
		estimatedUsage := (hw.GPUMemoryGB * 0.8) / 24 * 100 // 假设 24GB 是满载
		basePressure = (basePressure + estimatedUsage) / 2
	}

	return int(basePressure)
}
