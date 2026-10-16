package browser

import (
	"regexp"
	"strings"
)

// isTrustedHardWall documents the single security invariant enforced here:
//
//	The browser tool MUST NEVER cause isTrusted=false (synthetic) input events
//	to fire inside a page. Real automation input (rod's CDP Mouse/Keyboard,
//	Element.Click/Type/Fill) arrives at the browser as trusted events. The only
//	escape hatch is the generic `evaluate` action (arbitrary page JS). This guard
//	blocks any script that synthesizes input by dispatching DOM events, so a
//	synthetic event can never reach the page through this tool.
//
// IsSyntheticInputScript reports whether s looks like it would synthesize input
// events (isTrusted=false) if executed in a page. The second return value is a
// human-readable reason, empty when the script is considered safe.
//
// The check is intentionally conservative: it normalizes the script (lower-cased,
// comments stripped) and matches event-synthesis idioms. Legitimate read-only
// evaluate scripts (DOM queries, JSON.parse, arithmetic, string ops) must pass.
func IsSyntheticInputScript(s string) (bool, string) {
	norm := normalizeScript(s)
	if reason := matchSynthesis(norm); reason != "" {
		return true, reason
	}
	return false, ""
}

// normalizeScript lower-cases the script and removes string literals and
// comments before matching, so the matcher is not trivially evaded by casing,
// by hiding a call inside a comment, or by quoting a token in a string literal.
func normalizeScript(s string) string {
	// Remove string literals first so tokens inside quotes can't trip the matcher.
	noStrings := stringLiteralRE.ReplaceAllString(s, " ")
	// Strip /* ... */ block comments.
	noBlock := blockCommentRE.ReplaceAllString(noStrings, " ")
	// Strip // line comments.
	noLine := lineCommentRE.ReplaceAllString(noBlock, " ")
	return strings.ToLower(noLine)
}

var (
	// stringLiteralRE matches 'single', "double", and `template` literals
	// (including escaped quotes), so quoted tokens are ignored by the matcher.
	stringLiteralRE = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|` + "`" + `(?:[^` + "`" + `\\]|\\.)*` + "`")
	blockCommentRE  = regexp.MustCompile(`/\*.*?\*/`)
	lineCommentRE   = regexp.MustCompile(`//[^\n]*`)
)

// synthesisMatchers: each is a (compiled) pattern plus a stable reason string.
// Patterns are matched against the normalized (lower-cased, comment-stripped) script.
var synthesisMatchers = []struct {
	re     *regexp.Regexp
	reason string
}{
	// Generic dispatch: el.dispatchEvent(new Event(...)). The canonical
	// synthetic-input primitive.
	{regexp.MustCompile(`[^a-z_]dispatchevent\s*\(`), "calls dispatchEvent() which synthesizes an isTrusted=false event"},
	// Deprecated legacy synthetic dispatch.
	{regexp.MustCompile(`createevent\s*\(`), "calls createEvent() which builds a synthetic event"},
	{regexp.MustCompile(`initevent\s*\(`), "calls initEvent() which initializes a synthetic event"},
	{regexp.MustCompile(`initmouseevent\s*\(`), "calls initMouseEvent() which initializes a synthetic mouse event"},
	{regexp.MustCompile(`initkeyboardevent\s*\(`), "calls initKeyboardEvent() which initializes a synthetic keyboard event"},
	// `new Event(...)` family — constructing and (typically) dispatching events.
	{regexp.MustCompile(`new\s*(mouse|keyboard|pointer|touch|wheel|custom|ui|focus|drag|input|composition|storage|message|hashchange|page|popstate)?event\s*\(`), "constructs a DOM event (new Event/...Event) which is isTrusted=false when dispatched"},
	// Element methods that fire events when called programmatically.
	// Preceded by a non-identifier char (e.g. '.', ' ', '(') to avoid matching
	// handler-style identifiers like onClick / onsubmit.
	{regexp.MustCompile(`[^a-z_](click|submit|requestsubmit|focus|blur|select|reset)\s*\(`), "invokes a DOM element method that dispatches a synthetic event (isTrusted=false)"},
}

func matchSynthesis(norm string) string {
	for _, m := range synthesisMatchers {
		if m.re.MatchString(norm) {
			return m.reason
		}
	}
	return ""
}
