package recall

// 概念归一（轻量本体）。
//
// 为什么需要它：纯字面 n-gram 对"同义改写"完全无能为力 —— "把会话消息持久化到磁盘"
// 和 "聊天记录要保存到硬盘" 共享的字面 token 几乎为零，余弦相似度直接掉到 0，
// 召回就退化成了关键词匹配，这正是"语义检索"最容易名不副实的地方。
//
// 做法：把一组同义词映射到同一个概念 token（"c:<cluster>"），在 tokenize 时
// 额外追加。这样既保留精确匹配（原 token 仍在），又获得一层语义泛化：
// 持久化≈保存≈落盘、会话≈对话≈聊天、上下文≈context≈提示词 …
//
// 它不是神经网络，泛化能力有边界；但它零依赖、离线可用、行为可预测，并且与
// 远程神经 embedding 完全兼容（配置远程模型时，这层只是额外的一点增益）。
var conceptClusters = []struct {
	name string
	terms []string
}{
	{"session", []string{"会话", "对话", "聊天", "消息", "历史", "记录", "轮次", "上下文窗口", "conversation", "session", "chat", "message", "history", "dialog", "turn"}},
	{"persist", []string{"持久化", "保存", "落盘", "存储", "写入", "存入", "硬盘", "磁盘", "文件", "persist", "persistence", "save", "store", "storage", "disk", "write", "file"}},
	{"context", []string{"上下文", "提示词", "记忆", "装配", "context", "prompt", "memory", "pack"}},
	{"recall", []string{"召回", "检索", "搜索", "查询", "查找", "翻找", "回溯", "recall", "retrieve", "retrieval", "search", "query", "lookup"}},
	{"vector", []string{"向量", "嵌入", "语义", "相似度", "embedding", "embeddings", "vector", "semantic", "similarity", "cosine"}},
	{"segment", []string{"段落", "分片", "切分", "拆分", "分段", "解耦", "segment", "chunk", "split", "decouple"}},
	{"redline", []string{"红线", "约束", "禁止", "不能", "不要", "原则", "纪律", "规范", "redline", "constraint", "forbidden", "rule", "policy"}},
	{"requirement", []string{"需求", "待办", "任务", "目标", "requirement", "todo", "task", "goal", "feature"}},
	{"test", []string{"测试", "单测", "用例", "覆盖率", "反证", "test", "tests", "testing", "coverage", "unittest"}},
	{"build", []string{"编译", "构建", "打包", "发布", "build", "compile", "package", "release", "publish"}},
	{"vcs", []string{"提交", "推送", "分支", "仓库", "commit", "push", "branch", "repo", "repository", "git"}},
	{"index", []string{"索引", "锚点", "快照", "审计", "回退", "回滚", "index", "anchor", "snapshot", "audit", "rollback"}},
	{"tool", []string{"工具", "函数调用", "插件", "tool", "tools", "function", "plugin"}},
	{"model", []string{"模型", "大模型", "供应商", "llm", "model", "provider", "api"}},
	{"project", []string{"项目", "工程", "仓库根", "目录", "project", "workspace", "root", "directory"}},
}

var conceptIndex = buildConceptIndex()

func buildConceptIndex() map[string]string {
	m := make(map[string]string, 256)
	for _, c := range conceptClusters {
		for _, t := range c.terms {
			m[t] = c.name
		}
	}
	return m
}

// conceptTokens returns the concept token(s) for a raw surface form, if any.
// Both the exact form and its lower-cased version are checked, and CJK bigrams are
// additionally matched on their characters so "上下文窗口" still lands in the
// context cluster even though only "上下文" is listed.
func conceptTokens(surface string) []token {
	lower := []rune(surface)
	if len(lower) == 0 {
		return nil
	}
	var out []token
	seen := map[string]bool{}
	add := func(term string) {
		if c, ok := conceptIndex[term]; ok {
			key := "c:" + c
			if !seen[key] {
				seen[key] = true
				out = append(out, token{text: key, w: 1.0})
			}
		}
	}
	add(surface)
	if len(lower) >= 2 {
		add(string(lower[:2]))
	}
	if len(lower) >= 3 {
		add(string(lower[:3]))
	}
	return out
}

// Concepts reports the concept clusters present in a text. Exposed so the UI and
// tests can explain WHY two unrelated-looking sentences matched.
func Concepts(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tokenize(text) {
		if len(t.text) > 2 && t.text[:2] == "c:" {
			c := t.text[2:]
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}
