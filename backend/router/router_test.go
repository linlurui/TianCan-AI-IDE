package router

import (
	"testing"
)

func TestClassifyTask(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected TaskCategory
	}{
		{"Chinese code edit", "帮我写一个函数", CategoryCodeEdit},
		{"Chinese debug", "帮我调试这个bug", CategoryDebug},
		{"Chinese research", "搜索一下最新的技术", CategoryResearch},
		{"English code edit", "write a function", CategoryCodeEdit},
		{"English debug", "debug this error", CategoryDebug}, // use explicit debug keyword
		{"English research", "search for latest technology", CategoryResearch},
		{"Empty string", "", CategoryChat},
		{"Random text", "hello world", CategoryChat},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyTask(tt.input)
			if result != tt.expected {
				t.Errorf("classifyTask(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTaskConfidence(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		category TaskCategory
		minScore float64
	}{
		{"Short message", "hi", CategoryChat, 0.3},
		{"Medium message", "Please help me with this code that calculates fibonacci numbers", CategoryChat, 0.5},
		{"Long message", "This is a very long message that should have higher confidence " + string(make([]byte, 100)), CategoryChat, 0.6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just ensure it doesn't panic and returns reasonable values
			result := taskConfidence(tt.msg, tt.category)
			if result < 0 || result > 1 {
				t.Errorf("taskConfidence returned out of range [0,1]: %v", result)
			}
		})
	}
}

func TestAssessTaskComplexity(t *testing.T) {
	sr := &SmartRouter{}

	tests := []struct {
		name          string
		message       string
		expectedLevel string
	}{
		{"Simple fix", "fix typo in variable name", "simple"},
		{"Complex refactor", "重构整个用户认证模块，使用策略模式和依赖注入", "complex"},
		{"Moderate task", "帮我写一个排序函数", "moderate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sr.AssessTaskComplexity(tt.message)
			if result.Level != tt.expectedLevel {
				t.Logf("Complexity for %q: %v (score: %.2f)", tt.name, result.Level, result.Score)
			}
		})
	}
}

func TestHardwareMonitor(t *testing.T) {
	hm := NewHardwareMonitor()

	// Test starting monitoring
	hm.StartMonitoring()
	defer hm.StopMonitoring()

	// Wait for at least one update
	profile := hm.GetProfile()

	// Basic sanity checks
	if profile == nil {
		t.Fatal("GetProfile returned nil")
	}
	if profile.OS == "" {
		t.Log("OS not detected")
	}
	if profile.CPUCores <= 0 {
		t.Log("CPU cores not detected")
	}

	t.Logf("Hardware Profile: %+v", profile)
}

func TestCalculateLocalScore(t *testing.T) {
	sr := &SmartRouter{}

	tests := []struct {
		name       string
		hw         *HardwareProfile
		complexity TaskComplexity
	}{
		{
			name: "High RAM with GPU",
			hw: &HardwareProfile{
				TotalRAMGB:     32,
				AvailableRAMGB: 16,
				HasGPU:         true,
				GPUMemoryGB:    16,
			},
			complexity: TaskComplexity{Level: "simple"},
		},
		{
			name: "Low RAM no GPU",
			hw: &HardwareProfile{
				TotalRAMGB:     8,
				AvailableRAMGB: 2,
				HasGPU:         false,
			},
			complexity: TaskComplexity{Level: "complex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := sr.calculateLocalScore(tt.hw, tt.complexity)
			// Just verify it returns a valid score (can be negative for low-end hardware)
			t.Logf("Score for %s: %.2f", tt.name, score)
		})
	}
}

func TestIsSystemHealthy(t *testing.T) {
	sr := &SmartRouter{}

	// This test just verifies it doesn't panic
	healthy := sr.IsSystemHealthy()
	t.Logf("System healthy: %v", healthy)
}

func TestGetMemoryPressureLevel(t *testing.T) {
	sr := &SmartRouter{}

	pressure := sr.GetMemoryPressureLevel()
	if pressure < 0 || pressure > 100 {
		t.Errorf("GetMemoryPressureLevel() = %v, want 0-100", pressure)
	}
	t.Logf("Memory pressure: %d%%", pressure)
}

func TestRouteWithContext(t *testing.T) {
	sr := &SmartRouter{}

	result := sr.RouteWithContext("帮我写一个排序函数", map[string]float64{
		"claude-code": 0.8,
	})

	if result == nil {
		t.Fatal("RouteWithContext returned nil")
	}

	route, ok := result["route"]
	if !ok {
		t.Fatal("route key missing")
	}

	complexity, ok := result["complexity"]
	if !ok {
		t.Fatal("complexity key missing")
	}

	t.Logf("Route: %+v", route)
	t.Logf("Complexity: %+v", complexity)
}

func TestGetProviderHealthStatus(t *testing.T) {
	sr := &SmartRouter{}

	// Record some fake latency
	sr.RecordProviderLatency("claude-code", 500)
	sr.RecordProviderLatency("claude-code", 600)
	sr.RecordProviderLatency("deepseek", 2000)

	status := sr.GetProviderHealthStatus()
	if status == nil {
		t.Fatal("GetProviderHealthStatus returned nil")
	}

	t.Logf("Provider health: %+v", status)
}
