package roles

import (
	"fmt"
	"strings"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// Built-in agent definitions

var (
	// RouterAgent routes complex tasks to specialized agents.
	RouterAgent = types.AgentDefinition{
		AgentType:      types.RoleRouter,
		SystemPrompt:   routerPrompt,
		MaxTurns:       5,
		PermissionMode: "plan",
		Description:    "Route complex tasks to specialized agents",
		WhenToUse:      "Use when a task is complex enough to benefit from decomposition into subtasks handled by specialized agents",
	}

	// CoderAgent implements code changes.
	CoderAgent = types.AgentDefinition{
		AgentType:       types.RoleCoder,
		SystemPrompt:    coderPrompt,
		MaxTurns:        30,
		DisallowedTools: []string{"agent"},
		PermissionMode:  "auto",
		Description:     "Implement code changes",
		WhenToUse:       "Use for implementing code changes, writing new features, fixing bugs",
	}

	// ReviewerAgent reviews code changes for quality.
	ReviewerAgent = types.AgentDefinition{
		AgentType:       types.RoleReviewer,
		SystemPrompt:    reviewerPrompt,
		MaxTurns:        10,
		DisallowedTools: []string{"file_write", "file_edit", "bash"},
		PermissionMode:  "auto",
		Description:     "Review code changes for quality and correctness",
		WhenToUse:       "Use to review code changes before finalizing, check for bugs, style issues, and best practices",
	}

	// VerifierAgent verifies implementation correctness.
	VerifierAgent = types.AgentDefinition{
		AgentType:       types.RoleVerifier,
		SystemPrompt:    verifierPrompt,
		MaxTurns:        15,
		DisallowedTools: []string{"file_write", "file_edit"},
		PermissionMode:  "auto",
		Background:      true,
		Description:     "Verify implementation correctness by running tests and checks",
		WhenToUse:       "Use after non-trivial tasks (3+ file edits, backend/API changes) to verify correctness before reporting completion",
	}

	// GeneralAgent is the default general-purpose agent.
	GeneralAgent = types.AgentDefinition{
		AgentType:      types.RoleGeneral,
		SystemPrompt:   generalPrompt,
		MaxTurns:       30,
		PermissionMode: "auto",
		Description:    "General purpose agent for any task",
		WhenToUse:      "Default agent for tasks that don't require specialization",
	}
)

const routerPrompt = `You are a task router. Your job is to decompose complex tasks into subtasks and assign them to the appropriate specialized agents.

Available agent types:
- coder: For implementing code changes, writing new features, fixing bugs
- reviewer: For reviewing code changes for quality and correctness
- verifier: For verifying implementation correctness by running tests and checks

When routing:
1. Analyze the task to determine if it needs decomposition
2. Break complex tasks into 2-5 subtasks
3. Assign each subtask to the most appropriate agent type
4. Specify the order (sequential or parallel) for subtask execution
5. Provide clear, focused prompts for each subtask

Output format - for each subtask, output:
<subtask>
agent_type: [coder|reviewer|verifier]
prompt: [detailed prompt for the agent]
parallel: [true|false]
</subtask>

If the task is simple enough for a single agent, just say:
<direct>task can be handled by a single [agent_type] agent</direct>`

const coderPrompt = `You are a coding specialist. Your job is to implement code changes accurately and efficiently.

Guidelines:
- Read relevant files before making changes
- Make minimal, focused changes
- Follow existing code style and conventions
- Add appropriate error handling
- Test your changes mentally before submitting
- If unsure about requirements, ask for clarification

When editing files:
- Preserve existing functionality
- Don't add unnecessary complexity
- Keep changes scoped to the task`

const reviewerPrompt = `You are a code reviewer. Your job is to review code changes for quality, correctness, and best practices.

Review checklist:
1. **Correctness**: Does the code do what it's supposed to?
2. **Edge cases**: Are edge cases handled properly?
3. **Error handling**: Are errors handled appropriately?
4. **Style**: Does the code follow project conventions?
5. **Performance**: Are there any obvious performance issues?
6. **Security**: Are there any security concerns?
7. **Maintainability**: Is the code readable and maintainable?

Output format:
### Review Summary
[Brief summary of changes reviewed]

### Issues Found
[List any issues, categorized by severity: Critical/Major/Minor/Suggestion]

### Verdict
APPROVE / REQUEST_CHANGES / COMMENT`

const verifierPrompt = `You are a verification specialist. Your job is not to confirm the implementation works — it's to try to break it.

=== CRITICAL: DO NOT MODIFY THE PROJECT ===
You are STRICTLY PROHIBITED from:
- Creating, modifying, or deleting any files IN THE PROJECT DIRECTORY
- Installing dependencies or packages
- Running git write operations (add, commit, push)

You MAY write ephemeral test scripts to a temp directory.

=== VERIFICATION STRATEGY ===
1. Read the project's TIANCAN.md / README for build/test commands
2. Run the build (if applicable). A broken build is an automatic FAIL.
3. Run the project's test suite (if it has one). Failing tests are an automatic FAIL.
4. Run linters/type-checkers if configured
5. Check for regressions in related code

=== RECOGNIZE YOUR OWN RATIONALIZATIONS ===
- "The code looks correct based on my reading" — reading is not verification. Run it.
- "The implementer's tests already pass" — verify independently.
- "This is probably fine" — probably is not verified. Run it.

=== OUTPUT FORMAT ===
Every check MUST follow this structure:
### Check: [what you're verifying]
**Command run:** [exact command]
**Output observed:** [actual output]
**Result: PASS** (or FAIL — with Expected vs Actual)

End with exactly:
VERDICT: PASS
or
VERDICT: FAIL
or
VERDICT: PARTIAL`

const generalPrompt = `You are a general-purpose AI coding assistant. Follow the user's instructions carefully and use the available tools to accomplish the task.

Guidelines:
- Read files before modifying them
- Make minimal, focused changes
- Verify your work when possible
- Ask for clarification when unsure
- Report what you did and any issues found`

// GetAgentDefinition returns the agent definition for a given role.
func GetAgentDefinition(role types.AgentRole) (types.AgentDefinition, error) {
	switch role {
	case types.RoleRouter:
		return RouterAgent, nil
	case types.RoleCoder:
		return CoderAgent, nil
	case types.RoleReviewer:
		return ReviewerAgent, nil
	case types.RoleVerifier:
		return VerifierAgent, nil
	case types.RoleGeneral:
		return GeneralAgent, nil
	default:
		return GeneralAgent, fmt.Errorf("unknown agent role: %s, using general", role)
	}
}

// ShouldRoute determines if a task is complex enough to warrant routing.
func ShouldRoute(taskPrompt string) bool {
	// Heuristic: tasks with multiple requirements benefit from routing
	indicators := []string{
		" and ", " then ", " also ", " additionally ",
		"first", "second", "third",
		"refactor", "migrate", "port",
		"multi", "several", "various",
		"同时", "然后", "还要", "另外",
	}
	count := 0
	lower := strings.ToLower(taskPrompt)
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			count++
		}
	}
	return count >= 2
}

// ParseSubtasks extracts subtask assignments from router output.
func ParseSubtasks(routerOutput string) []SubtaskAssignment {
	var tasks []SubtaskAssignment
	segments := strings.Split(routerOutput, "<subtask>")
	for _, seg := range segments {
		if !strings.Contains(seg, "</subtask>") {
			continue
		}
		content := seg[:strings.Index(seg, "</subtask>")]
		task := SubtaskAssignment{}

		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "agent_type:") {
				task.AgentType = strings.TrimSpace(strings.TrimPrefix(line, "agent_type:"))
			} else if strings.HasPrefix(line, "prompt:") {
				task.Prompt = strings.TrimSpace(strings.TrimPrefix(line, "prompt:"))
			} else if strings.HasPrefix(line, "parallel:") {
				task.Parallel = strings.TrimSpace(strings.TrimPrefix(line, "parallel:")) == "true"
			}
		}

		if task.Prompt != "" {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

// SubtaskAssignment represents a parsed subtask from the router.
type SubtaskAssignment struct {
	AgentType string `json:"agentType"`
	Prompt    string `json:"prompt"`
	Parallel  bool   `json:"parallel"`
}
