package hierarchy

import (
	"os/exec"
	"testing"
)

func TestWorktreeManagerCreation(t *testing.T) {
	// Create a temp directory for testing
	tmpDir := t.TempDir()

	// Initialize git repo
	execCommand("git", "init", tmpDir)
	execCommand("git", "config", "user.email", "test@test.com", "-C", tmpDir)
	execCommand("git", "config", "user.name", "Test", "-C", tmpDir)

	// Create a WorktreeManager
	wtm := NewWorktreeManager(tmpDir)

	// List should be empty
	worktrees := wtm.ListWorktrees()
	if len(worktrees) != 0 {
		t.Errorf("Expected 0 worktrees, got %d", len(worktrees))
	}
}

func TestWorktreeState(t *testing.T) {
	state := WorktreeState{
		ID:        "test-worktree",
		Branch:    "feature/test",
		Path:      "/path/to/worktree",
		Status:    "active",
		ParentID:  "parent-agent-123",
	}

	if state.ID != "test-worktree" {
		t.Errorf("Expected ID 'test-worktree', got %s", state.ID)
	}
	if state.Status != "active" {
		t.Errorf("Expected status 'active', got %s", state.Status)
	}
}

func TestWorktreeConflictError(t *testing.T) {
	err := &WorktreeConflictError{
		BranchName: "feature/test",
		Output:     "Auto-merging file.txt\nCONFLICT (content): Merge conflict in file.txt",
		Message:    "Merge conflict detected",
	}

	expected := "Merge conflict detected"
	if err.Error() != expected {
		t.Errorf("Expected error message '%s', got '%s'", expected, err.Error())
	}
}

func TestScratchpadCoordination(t *testing.T) {
	tmpDir := t.TempDir()
	sc := NewScratchpadCoordination(tmpDir)

	// Test Write and Read
	err := sc.Write("key1", "value1")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	val, err := sc.Read("key1")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if val != "value1" {
		t.Errorf("Expected 'value1', got '%s'", val)
	}

	// Test List
	keys := sc.List()
	found := false
	for _, k := range keys {
		if k == "key1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("key1 not found in List()")
	}

	// Test non-existent key
	_, err = sc.Read("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent key")
	}
}

func TestCoordinatorBuiltInAgents(t *testing.T) {
	tmpDir := t.TempDir()
	execCommand("git", "init", tmpDir)

	coord := NewCoordinator(tmpDir, nil)

	// Check built-in agents exist
	agentTypes := []string{"general-purpose", "verify", "research", "fix"}
	for _, agentType := range agentTypes {
		def := coord.GetAgentDefinition(agentType)
		if def == nil {
			t.Errorf("Built-in agent %s not registered", agentType)
		} else {
			t.Logf("Agent %s: %s", agentType, def.Description)
		}
	}
}

func TestAgentDefinition(t *testing.T) {
	def := AgentDefinition{
		AgentType:       "test-agent",
		Description:     "A test agent",
		Tools:           []string{"read_file", "grep"},
		MaxTurns:        50,
		IsBackground:    false,
		Isolation:       "worktree",
	}

	if def.AgentType != "test-agent" {
		t.Errorf("Expected AgentType 'test-agent', got %s", def.AgentType)
	}
	if def.MaxTurns != 50 {
		t.Errorf("Expected MaxTurns 50, got %d", def.MaxTurns)
	}
	if def.Isolation != "worktree" {
		t.Errorf("Expected Isolation 'worktree', got %s", def.Isolation)
	}
}

func TestAgentSpawnConfig(t *testing.T) {
	config := AgentSpawnConfig{
		SubagentType: "verify",
		Prompt:       "Check the code for bugs",
		Description:  "Bug checker",
		Model:        "claude-3-sonnet",
		Cwd:          "/path/to/cwd",
		Isolation:    "worktree",
		IsAsync:      true,
	}

	if config.SubagentType != "verify" {
		t.Errorf("Expected SubagentType 'verify', got %s", config.SubagentType)
	}
	if !config.IsAsync {
		t.Error("Expected IsAsync to be true")
	}
}

func TestAgentResult(t *testing.T) {
	result := AgentResult{
		AgentID:     "agent-123",
		AgentType:   "general-purpose",
		Output:      "Task completed successfully",
		DurationMs:  1500,
		TotalTokens: 500,
		IsComplete:  true,
	}

	if result.AgentID != "agent-123" {
		t.Errorf("Expected AgentID 'agent-123', got %s", result.AgentID)
	}
	if !result.IsComplete {
		t.Error("Expected IsComplete to be true")
	}
	if result.DurationMs != 1500 {
		t.Errorf("Expected DurationMs 1500, got %d", result.DurationMs)
	}
}

func TestSubAgentProgress(t *testing.T) {
	progress := SubAgentProgress{
		AgentID:   "agent-456",
		AgentType: "research",
		Iteration: 3,
		ToolName:  "read_file",
		ToolArgs:  `{"path": "/test/file.go"}`,
		IsDone:    false,
	}

	if progress.Iteration != 3 {
		t.Errorf("Expected Iteration 3, got %d", progress.Iteration)
	}
	if progress.IsDone {
		t.Error("Expected IsDone to be false")
	}
}

func TestAggregatedResult(t *testing.T) {
	result := AggregatedResult{
		TotalAgents:  3,
		SuccessCount: 2,
		FailCount:    1,
		Summary:      "2 succeeded, 1 failed",
		SharedState: map[string]string{
			"findings": "Found 5 issues",
		},
	}

	if result.TotalAgents != 3 {
		t.Errorf("Expected TotalAgents 3, got %d", result.TotalAgents)
	}
	if result.SuccessCount != 2 {
		t.Errorf("Expected SuccessCount 2, got %d", result.SuccessCount)
	}
	if len(result.SharedState) != 1 {
		t.Errorf("Expected 1 shared state entry, got %d", len(result.SharedState))
	}
}

// Helper function to execute git commands
func execCommand(name string, args ...string) {
	execCommandInDir("", name, args...)
}

func execCommandInDir(dir, name string, args ...string) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Run()
}
