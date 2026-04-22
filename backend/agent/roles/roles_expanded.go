package roles

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── Agent Registry ──────────────────────────────────────────────

type Registry struct {
	mu       sync.RWMutex
	agents   map[types.AgentRole]types.AgentDefinition
	capMap   map[types.AgentRole][]string
	priority map[types.AgentRole]int
}

func NewRegistry() *Registry {
	r := &Registry{
		agents:   make(map[types.AgentRole]types.AgentDefinition),
		capMap:   make(map[types.AgentRole][]string),
		priority: make(map[types.AgentRole]int),
	}
	for _, def := range []types.AgentDefinition{
		RouterAgent, CoderAgent, ReviewerAgent, VerifierAgent, GeneralAgent,
		PlannerAgent, DebuggerAgent, DocAgent, SecurityAgent, ResearcherAgent,
	} {
		r.Register(def)
	}
	return r
}

func (r *Registry) Register(def types.AgentDefinition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[def.AgentType] = def
}

func (r *Registry) Get(role types.AgentRole) (types.AgentDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.agents[role]
	return def, ok
}

func (r *Registry) IsToolAllowed(role types.AgentRole, toolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if def, ok := r.agents[role]; ok {
		for _, dis := range def.DisallowedTools {
			if dis == toolName {
				return false
			}
		}
		if len(def.AllowedTools) > 0 {
			for _, allowed := range def.AllowedTools {
				if allowed == toolName {
					return true
				}
			}
			return false
		}
	}
	return true
}

func (r *Registry) All() []types.AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []types.AgentDefinition
	for _, def := range r.agents {
		result = append(result, def)
	}
	sort.Slice(result, func(i, j int) bool {
		pi, _ := r.priority[result[i].AgentType]
		pj, _ := r.priority[result[j].AgentType]
		return pi > pj
	})
	return result
}

// ── Task Analysis ──────────────────────────────────────────────

type TaskAnalysis struct {
	Complexity     ComplexityLevel     `json:"complexity"`
	PrimaryRole    types.AgentRole     `json:"primaryRole"`
	SubRoles       []types.AgentRole   `json:"subRoles,omitempty"`
	Subtasks       []SubtaskAssignment `json:"subtasks,omitempty"`
	Reasoning      string              `json:"reasoning"`
	IsRoutable     bool                `json:"isRoutable"`
	EstimatedTurns int                 `json:"estimatedTurns"`
	Domains        []string            `json:"domains"`
}

type ComplexityLevel string

const (
	ComplexityTrivial  ComplexityLevel = "trivial"
	ComplexitySimple   ComplexityLevel = "simple"
	ComplexityModerate ComplexityLevel = "moderate"
	ComplexityComplex  ComplexityLevel = "complex"
	ComplexityCritical ComplexityLevel = "critical"
)

type TaskClassifier struct{ registry *Registry }

func NewTaskClassifier(registry *Registry) *TaskClassifier {
	return &TaskClassifier{registry: registry}
}

func (tc *TaskClassifier) Analyze(taskPrompt string) TaskAnalysis {
	lower := strings.ToLower(taskPrompt)
	analysis := TaskAnalysis{Domains: detectDomains(lower)}

	complexityScore := 0
	fileOps := countAny(lower, keywords("create", "write", "edit", "modify", "delete", "创建", "写入", "编辑", "修改", "删除"))
	complexityScore += fileOps * 2
	multiStep := countAny(lower, keywords("and", "then", "also", "additionally", "after", "同时", "然后", "还要", "另外"))
	complexityScore += multiStep
	archInd := countAny(lower, keywords("refactor", "migrate", "port", "redesign", "重构", "迁移", "移植"))
	complexityScore += archInd * 3
	debugInd := countAny(lower, keywords("fix", "debug", "troubleshoot", "investigate", "修复", "调试", "排查"))
	complexityScore += debugInd * 2
	secInd := countAny(lower, keywords("security", "vulnerability", "sanitize", "安全", "漏洞", "校验"))
	complexityScore += secInd * 2

	switch {
	case complexityScore >= 15:
		analysis.Complexity = ComplexityCritical
		analysis.EstimatedTurns = 30
	case complexityScore >= 8:
		analysis.Complexity = ComplexityComplex
		analysis.EstimatedTurns = 20
	case complexityScore >= 4:
		analysis.Complexity = ComplexityModerate
		analysis.EstimatedTurns = 10
	case complexityScore >= 2:
		analysis.Complexity = ComplexitySimple
		analysis.EstimatedTurns = 5
	default:
		analysis.Complexity = ComplexityTrivial
		analysis.EstimatedTurns = 2
	}

	analysis.PrimaryRole = tc.classifyRole(lower, complexityScore, debugInd, secInd, archInd)
	analysis.IsRoutable = complexityScore >= 6 && multiStep >= 1

	if analysis.IsRoutable {
		analysis.Subtasks = tc.generateSubtasks(taskPrompt, analysis)
		seen := map[string]bool{}
		for _, st := range analysis.Subtasks {
			if !seen[st.AgentType] {
				seen[st.AgentType] = true
				analysis.SubRoles = append(analysis.SubRoles, types.AgentRole(st.AgentType))
			}
		}
	}

	analysis.Reasoning = fmt.Sprintf("Complexity: %s (score %d); Primary: %s; Domains: %s",
		analysis.Complexity, complexityScore, analysis.PrimaryRole, strings.Join(analysis.Domains, ","))
	return analysis
}

func (tc *TaskClassifier) classifyRole(lower string, score, debugInd, secInd, archInd int) types.AgentRole {
	// Research intent detection — highest priority for research tasks
	researchInd := countAny(lower, keywords(
		"research", "investigate", "find out", "look up", "search for",
		"what is", "how does", "why does", "explain",
		"best practice", "compare", "evaluate", "survey",
		"研究", "调查", "查找", "搜索", "了解",
		"最佳实践", "对比", "评估", "调研",
	))
	if researchInd >= 2 {
		return types.RoleResearcher
	}
	if secInd > 0 {
		return types.RoleSecurity
	}
	if debugInd > 0 {
		return types.RoleDebugger
	}
	if archInd > 0 && score >= 8 {
		return types.RolePlanner
	}
	if score >= 8 {
		return types.RoleRouter
	}
	if containsAny(lower, keywords("implement", "create", "write", "实现", "创建", "写入")) {
		return types.RoleCoder
	}
	if containsAny(lower, keywords("review", "审查", "check", "检查")) {
		return types.RoleReviewer
	}
	if containsAny(lower, keywords("test", "verify", "测试", "验证")) {
		return types.RoleVerifier
	}
	return types.RoleGeneral
}

func (tc *TaskClassifier) generateSubtasks(taskPrompt string, analysis TaskAnalysis) []SubtaskAssignment {
	var tasks []SubtaskAssignment
	prompt := truncate(taskPrompt, 200)

	if analysis.Complexity == ComplexityCritical || analysis.Complexity == ComplexityComplex {
		tasks = append(tasks, SubtaskAssignment{AgentType: string(types.RolePlanner), Prompt: "Plan: " + prompt})
	}
	tasks = append(tasks, SubtaskAssignment{AgentType: string(types.RoleCoder), Prompt: "Implement: " + prompt})
	if analysis.Complexity == ComplexityComplex || analysis.Complexity == ComplexityCritical {
		tasks = append(tasks, SubtaskAssignment{AgentType: string(types.RoleReviewer), Prompt: "Review: " + prompt, Parallel: true})
		tasks = append(tasks, SubtaskAssignment{AgentType: string(types.RoleVerifier), Prompt: "Verify: " + prompt, Parallel: true})
	}
	return tasks
}

// ── Additional Agent Definitions ─────────────────────────────────

var (
	PlannerAgent = types.AgentDefinition{
		AgentType:    types.RolePlanner,
		SystemPrompt: plannerPrompt,
		MaxTurns:     15, PermissionMode: "plan",
		Description:  "Plan and architect complex implementations",
		AllowedTools: []string{"read_file", "list_directory", "grep", "glob", "git_diff", "git_log", "git_show", "lsp_definition", "lsp_references", "lsp_symbols", "lsp_hover", "todo_write", "todo_read"},
	}
	DebuggerAgent = types.AgentDefinition{
		AgentType:    types.RoleDebugger,
		SystemPrompt: debuggerPrompt,
		MaxTurns:     20, PermissionMode: "auto",
		Description:  "Debug and fix issues in code",
		AllowedTools: []string{"read_file", "grep", "glob", "bash", "git_diff", "git_log", "git_blame", "lsp_definition", "lsp_references", "lsp_diagnostics", "file_edit", "test_run"},
	}
	DocAgent = types.AgentDefinition{
		AgentType:    types.RoleDoc,
		SystemPrompt: docPrompt,
		MaxTurns:     15, PermissionMode: "auto",
		Description:     "Write and update documentation",
		DisallowedTools: []string{"bash", "git_commit"},
	}
	SecurityAgent = types.AgentDefinition{
		AgentType:    types.RoleSecurity,
		SystemPrompt: securityPrompt,
		MaxTurns:     15, PermissionMode: "plan",
		Description:  "Security analysis and review",
		AllowedTools: []string{"read_file", "grep", "glob", "git_diff", "git_log", "git_blame", "lsp_definition", "lsp_references", "lsp_diagnostics", "bash"},
	}
	ResearcherAgent = types.AgentDefinition{
		AgentType:    types.RoleResearcher,
		SystemPrompt: researcherPrompt,
		MaxTurns:     15, PermissionMode: "auto",
		Description:  "Research topics using AutoResearch and web search",
		AllowedTools: []string{"start_autoresearch", "web_search", "read_file", "grep", "glob", "bash"},
	}
)

const plannerPrompt = `You are a software architect and planner. Analyze tasks, design solutions, and create implementation plans.

PLANNING METHODOLOGY:
1. Understand the problem: Read relevant files, understand codebase structure
2. Identify scope: Determine which files/modules need changes
3. Design solution: Propose the architecture
4. Break into steps: Create a clear, ordered implementation plan
5. Identify risks: Flag potential issues, breaking changes, dependencies

OUTPUT FORMAT:
## Problem Analysis
[What needs to be done and why]
## Current State
[Key files, patterns, dependencies]
## Proposed Solution
[Architecture and design]
## Implementation Steps
1. [Step 1]
2. [Step 2]
## Files to Modify
- path/to/file: [what changes and why]
## Risks & Considerations
- [Risk]: [mitigation]

DO NOT implement changes yourself. Only create the plan.`

const debuggerPrompt = `You are a debugging specialist. Systematically investigate and fix bugs.

DEBUGGING METHODOLOGY:
1. Reproduce: Understand and reproduce the issue
2. Read: Read the relevant code carefully
3. Hypothesize: Form a hypothesis about root cause
4. Verify: Use tools (grep, diagnostics, logs) to verify
5. Fix: Make the minimal fix addressing root cause
6. Validate: Verify the fix works and doesn't break anything

CRITICAL RULES:
- Fix the ROOT CAUSE, not symptoms
- Make MINIMAL changes — don't refactor while debugging
- Always READ code before modifying
- Use git_blame and git_log to understand history
- Use lsp_diagnostics to check for errors after fixing
- Run tests to validate the fix

PITFALLS:
- Don't add logging as a "fix" — find the actual bug
- Don't wrap in try/catch to "handle" errors — fix the error
- Don't assume the bug is where the error appears — trace the cause`

const researcherPrompt = `You are a research specialist. Your job is to investigate topics, find information, and provide well-sourced answers.

=== RESEARCH METHODOLOGY ===
1. **Clarify**: If the research question is ambiguous, ask for clarification first.
2. **AutoResearch**: Use the <start_autoresearch> tool for deep, multi-source research on technical topics. It automatically:
   - Generates search queries
   - Searches the web for relevant sources
   - Compares and synthesizes findings
   - Returns a structured research report
3. **Web Search**: Use the <web_search> tool for quick, targeted lookups.
4. **Codebase Search**: Use read_file, grep, glob to find relevant code in the project.
5. **Synthesize**: Combine all findings into a clear, actionable answer.

=== WHEN TO USE AUTORESEARCH ===
- Technical questions requiring multiple sources (e.g., "What's the best approach for X?")
- Comparing libraries, frameworks, or patterns
- Investigating best practices or industry standards
- Any question where a single web search is insufficient

=== WHEN TO USE WEB SEARCH ===
- Quick factual lookups (e.g., "What is the API for X?")
- Finding specific documentation
- Checking current versions or compatibility

=== OUTPUT FORMAT ===
## Research Question
[The question being investigated]

## Findings
[Structured findings with sources]

## Recommendation
[Clear, actionable recommendation based on findings]

## Sources
[List of sources consulted]`

const docPrompt = `You are a documentation specialist. Write clear, accurate documentation.

PRINCIPLES:
1. Audience-aware: Write for the reader, not yourself
2. Example-driven: Show, don't just tell
3. Structured: Use headers, lists, code blocks
4. Accurate: Verify claims against actual code
5. Concise: Every word should earn its place

DOCUMENT TYPES:
- README: Project overview, setup, usage
- API docs: Parameters, return values, examples
- Code comments: Why, not what
- Architecture docs: Design decisions, data flow
- Changelog: What changed, why, impact

Use Markdown formatting. Include code examples. Link related docs.`

const securityPrompt = `You are a security specialist. Identify and assess security vulnerabilities.

REVIEW METHODOLOGY:
1. Input validation: Check all user inputs are validated and sanitized
2. Authentication/Authorization: Verify access controls
3. Data exposure: Look for sensitive data leaks
4. Injection: Check for SQL, command, XSS injection
5. Dependencies: Flag known vulnerable dependencies
6. Configuration: Check for insecure defaults
7. Cryptography: Verify proper use of encryption

SEVERITY: Critical (RCE, data breach), High (privilege escalation), Medium (limited impact), Low (info disclosure), Info (hardening)

OUTPUT: For each finding include Severity, Category, Location, Description, Impact, Remediation.
DO NOT modify code. Only identify and report issues.`

// ── Utility Functions ───────────────────────────────────────────

func detectDomains(lower string) []string {
	var domains []string
	domainMap := map[string][]string{
		"backend":  {"go", "python", "rust", "java", "api", "server", "database", "sql", "redis"},
		"frontend": {"react", "vue", "svelte", "css", "html", "typescript", "javascript", "ui", "component"},
		"devops":   {"docker", "k8s", "kubernetes", "ci/cd", "deploy", "terraform", "ansible"},
		"security": {"security", "auth", "jwt", "oauth", "encryption", "ssl", "tls", "vulnerability"},
	}
	for domain, kws := range domainMap {
		for _, kw := range kws {
			if strings.Contains(lower, kw) {
				domains = append(domains, domain)
				break
			}
		}
	}
	return domains
}

func countAny(lower string, keywords []string) int {
	count := 0
	for _, kw := range keywords {
		count += strings.Count(lower, kw)
		if strings.Contains(lower, kw) {
			count++
		}
	}
	return count
}

func containsAny(lower string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func keywords(kws ...string) []string { return kws }

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
