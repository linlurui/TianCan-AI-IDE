package conversation

import (
	"regexp"
	"strings"
)

// Extraction is what the rule-based extractor produced from a user utterance.
type Extraction struct {
	Redlines     []Candidate `json:"redlines,omitempty"`
	Requirements []Candidate `json:"requirements,omitempty"`
}

// Candidate is one extracted sentence with a confidence score in [0,1].
type Candidate struct {
	Text     string  `json:"text"`
	Score    float64 `json:"score"`
	Severity string  `json:"severity,omitempty"` // redlines only: "hard" | "soft"
	Rule     string  `json:"rule"`
}

// 红线信号：用户强调要注意、必须做、或绝不能做的事。
// 这些词一旦出现，句子几乎必然是约束而不是闲聊，所以给高分。
var (
	reHardNever = regexp.MustCompile(`(?i)(不要|不许|不能|不可以|不可|禁止|严禁|别再|绝不|绝对不|永远不要|任何时候都不|未经.{0,6}不得|宁可.{0,4}也不)`)
	reHardMust  = regexp.MustCompile(`(?i)(必须|一定要|务必|务请|记得(?:要)?|千万(?:要|别|不)|始终|一律|只能|仅限|仅限于)`)
	reSoft      = regexp.MustCompile(`(?i)(注意|小心|留意|建议|最好|尽量避免|优先|默认(?:要|用|走)|红线|原则|前提|约定|规范|纪律)`)
	reIsRedline = regexp.MustCompile(`(?i)(这是红线|记为红线?|记住[:：]|永远记住|以后都|以后(?:再|也)?不|以后都要)`)

	// 英文侧（提示词/代码注释里常见）
	reHardNeverEn = regexp.MustCompile(`(?i)\b(never|do not|don'?t|must not|forbidden|prohibited|no longer|under no circumstances)\b`)
	reHardMustEn  = regexp.MustCompile(`(?i)\b(must|always|make sure|be sure|required|ensure)\b`)
	reSoftEn      = regexp.MustCompile(`(?i)\b(note that|watch out|be careful|prefer|by default|convention|rule of thumb)\b`)
)

// 需求点信号：用户提出要做的事。
var (
	reReqCN = regexp.MustCompile(`(?i)(我要|我想|我需要|给我|帮我|请(?:帮我|你)?|加上|增加|实现|支持|做一个|做一下|改造|重构|优化|修复|解决|接入|集成|新增|希望|目标是|需求是|能不能做|可不可以做)`)
	reReqEn = regexp.MustCompile(`(?i)\b(we need|add support|implement|please add|let'?s (?:add|build|create)|should (?:be able to|support)|feature request)\b`)
)

// splitSentences splits an utterance into candidate sentences. It intentionally
// keeps the delimiters' positions irrelevant: we only need the sentence bodies.
func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	// Normalise newlines into sentence breaks so bullet lists split properly.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	re := regexp.MustCompile(`[。！？!?；;\n]+|[.]{2,}`)
	parts := re.Split(text, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// A very long "sentence" (a pasted blob) is split on commas too, otherwise
		// it would smuggle a paragraph into the red-line list.
		if len([]rune(p)) > 120 {
			for _, sub := range regexp.MustCompile(`[，,、]+`).Split(p, -1) {
				sub = strings.TrimSpace(sub)
				if sub != "" {
					out = append(out, sub)
				}
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// Extract scans a USER utterance for red lines and requirement points.
//
// It is deliberately recall-friendly but precision-guarded: a sentence only
// becomes a red line when it carries an explicit constraint marker, and obvious
// non-constraints (plain questions, chit-chat, code dumps) are filtered out. The
// result is meant to be confirmed/persisted by the caller (or by the LLM pass in
// the prompt layer) — it is a proposal, not a verdict.
func Extract(text string) Extraction {
	var out Extraction
	for _, s := range splitSentences(text) {
		rs := []rune(s)
		if len(rs) < 4 || len(rs) > 200 {
			continue
		}
		if looksLikeCode(s) || isPureQuestion(s) {
			continue
		}
		if c, ok := classifyRedline(s); ok {
			out.Redlines = append(out.Redlines, c)
			continue
		}
		if c, ok := classifyRequirement(s); ok {
			out.Requirements = append(out.Requirements, c)
		}
	}
	return out
}

func classifyRedline(s string) (Candidate, bool) {
	score := 0.0
	severity := "soft"
	rule := ""
	switch {
	case reIsRedline.MatchString(s):
		score, severity, rule = 0.95, "hard", "marked-redline"
	case reHardNever.MatchString(s):
		score, severity, rule = 0.9, "hard", "cn-never"
	case reHardNeverEn.MatchString(s):
		score, severity, rule = 0.9, "hard", "en-never"
	case reHardMust.MatchString(s):
		score, severity, rule = 0.8, "hard", "cn-must"
	case reHardMustEn.MatchString(s):
		score, severity, rule = 0.8, "hard", "en-must"
	case reSoft.MatchString(s):
		score, severity, rule = 0.6, "soft", "cn-note"
	case reSoftEn.MatchString(s):
		score, severity, rule = 0.6, "soft", "en-note"
	default:
		return Candidate{}, false
	}
	// Imperative markers add confidence; a benchmark "remember: never push" is a
	// red line even without a bare 不要.
	if strings.Contains(s, "！") || strings.Contains(s, "!") {
		score += 0.05
	}
	if score > 1 {
		score = 1
	}
	text := strings.TrimSpace(s)
	if severity == "hard" {
		// 硬约束才做前缀剥离："记住：不要推送" → "不要推送"，读起来才像一条规则。
		// 软提醒（"注意…"）保留原句，因为"注意"本身就是语义的一部分。
		text = normalise(s)
	}
	return Candidate{Text: text, Score: score, Severity: severity, Rule: rule}, true
}

func classifyRequirement(s string) (Candidate, bool) {
	score := 0.0
	rule := ""
	switch {
	case reReqCN.MatchString(s):
		score, rule = 0.7, "cn-req"
	case reReqEn.MatchString(s):
		score, rule = 0.7, "en-req"
	default:
		return Candidate{}, false
	}
	if len([]rune(s)) > 8 {
		score += 0.05
	}
	return Candidate{Text: normalise(s), Score: score, Rule: rule}, true
}

// looksLikeCode filters out pasted code / logs, which are full of "!" and "must"
// comments and would otherwise pollute the red-line list.
func looksLikeCode(s string) bool {
	markers := 0
	for _, m := range []string{"{", "}", "()", ";", "=>", "func ", "package ", "import ", "def ", "class ", "```", "<div", "http://", "https://"} {
		if strings.Contains(s, m) {
			markers++
		}
	}
	return markers >= 2
}

// isPureQuestion keeps "你能不能做X？" (a requirement) while dropping "这是什么？"
// (a plain question that carries no constraint).
func isPureQuestion(s string) bool {
	if !strings.Contains(s, "？") && !strings.Contains(s, "?") && !strings.HasSuffix(s, "吗") {
		return false
	}
	return !reReqCN.MatchString(s) && !reReqEn.MatchString(s)
}

// normalise trims filler so the stored red line reads like a rule, e.g.
// "你给我记住：不要推送" → "不要推送".
func normalise(s string) string {
	s = strings.TrimSpace(s)
	// Drop a leading subject/addressing prefix once.
	prefixes := []string{
		"你给我", "你给我记住", "给我记住", "你记住", "记住", "请注意", "注意", "另外", "还有", "顺便", "对了",
		"please", "make sure", "ensure",
	}
	lower := strings.ToLower(s)
	for _, p := range prefixes {
		lp := strings.ToLower(p)
		if strings.HasPrefix(lower, lp) {
			rest := strings.TrimSpace(s[len(p):])
			// Only strip when something meaningful remains.
			if len([]rune(rest)) >= 4 {
				s = rest
				lower = strings.ToLower(s)
				break
			}
		}
	}
	s = strings.TrimLeft(s, "：:，, ")
	return strings.TrimSpace(s)
}

// AutoIngest applies extraction to a user utterance and persists whatever passes
// the confidence threshold. It is the "自动录入" half of the red-line story: the
// user does not have to open a settings dialog for every constraint they state.
//
// minScore defaults to 0.75 (hard markers only) when <= 0.
func (s *Session) AutoIngest(text string, minScore float64) (Extraction, error) {
	if minScore <= 0 {
		minScore = 0.75
	}
	ex := Extract(text)
	var applied Extraction
	for _, c := range ex.Redlines {
		if c.Score < minScore {
			continue
		}
		if _, err := s.AddRedline(c.Text, "extracted", c.Severity); err == nil {
			applied.Redlines = append(applied.Redlines, c)
		}
	}
	for _, c := range ex.Requirements {
		if c.Score < minScore {
			continue
		}
		if _, err := s.AddRequirement(c.Text, "extracted"); err == nil {
			applied.Requirements = append(applied.Requirements, c)
		}
	}
	return applied, nil
}
