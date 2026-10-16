package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkill_RenderPreStep_BodyWins 反证前置：显式 Body 必须优先于 description/steps 派生。
func TestSkill_RenderPreStep_BodyWins(t *testing.T) {
	s := &Skill{
		Name:        "code-review",
		Description: "这个描述不应被单独使用",
		Body:        "请按以下步骤逐行审查代码",
	}
	got := s.RenderPreStep()
	if !strings.Contains(got, "请按以下步骤逐行审查代码") {
		t.Fatalf("RenderPreStep 必须包含 Body，实际: %q", got)
	}
	if !strings.Contains(got, "code-review") {
		t.Fatalf("RenderPreStep 必须带技能名，实际: %q", got)
	}
}

// TestSkill_RenderPreStep_Fallback 无 Body 时由 description+steps 派生（兼容老技能）。
func TestSkill_RenderPreStep_Fallback(t *testing.T) {
	s := &Skill{
		Name:        "deploy",
		Description: "部署到生产环境",
		Steps:       []SkillStep{{Tool: "bash", Args: map[string]interface{}{"command": "make deploy"}}},
	}
	got := s.RenderPreStep()
	if !strings.Contains(got, "部署到生产环境") {
		t.Fatalf("fallback 必须含 description，实际: %q", got)
	}
	if !strings.Contains(got, "bash") {
		t.Fatalf("fallback 必须含 step 的 tool，实际: %q", got)
	}
}

// TestManager_MatchTrigger 从 FSProvider 加载后按 trigger 命中；不命中返回空。
func TestManager_MatchTrigger(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	yaml := `
name: code-review
description: 代码审查
triggers:
  - review
  - 审查
steps:
  - tool: bash
    args:
      command: "git diff"
`
	if err := os.WriteFile(filepath.Join(skillDir, "code-review.skill.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(dir)
	if err := m.LoadAll(); err != nil {
		t.Fatal(err)
	}

	matched := m.MatchTrigger("请帮我 review 这段改动")
	if len(matched) != 1 || matched[0].Name != "code-review" {
		t.Fatalf("期望命中 code-review，实际: %v", matched)
	}

	if got := m.MatchTrigger("今天天气怎么样"); len(got) != 0 {
		t.Fatalf("不相关输入应无命中，实际: %v", got)
	}
}

// TestManager_MatchTrigger_Counterexample 反证：若 MatchTrigger 永远返回全部技能，
// 此测试必然失败——验证我们确实只返回命中项，而非整表。
func TestManager_MatchTrigger_Counterexample(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	_ = os.MkdirAll(skillDir, 0755)
	_ = os.WriteFile(filepath.Join(skillDir, "a.skill.yaml"),
		[]byte("name: alpha\ntriggers:\n  - alpha\nsteps:\n  - tool: bash\n    args: {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(skillDir, "b.skill.yaml"),
		[]byte("name: beta\ntriggers:\n  - beta\nsteps:\n  - tool: bash\n    args: {}\n"), 0644)

	m := NewManager(dir)
	_ = m.LoadAll()

	// 用 "beta" 查询，alpha 绝不该被命中
	got := m.MatchTrigger("beta")
	if len(got) != 1 || got[0].Name != "beta" {
		t.Fatalf("只应命中 beta，实际: %v", got)
	}
}
