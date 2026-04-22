import type * as Monaco from "monaco-editor";
import type { ExtensionContributes } from "../bindings/extension";

/**
 * Applies all static contributions from an installed extension to Monaco Editor.
 * Covers: color themes, language configurations, TextMate-style token rules, and snippets.
 */
export function applyExtensionContributes(
  monaco: typeof Monaco,
  contrib: ExtensionContributes
): void {
  applyThemes(monaco, contrib);
  applyLanguageConfigurations(monaco, contrib);
  applyGrammarTokenRules(monaco, contrib);
  applySnippets(monaco, contrib);
}

// --- Themes ---

function applyThemes(monaco: typeof Monaco, contrib: ExtensionContributes): void {
  for (const theme of contrib.themes ?? []) {
    if (!theme.data || !theme.label) continue;
    try {
      const raw = JSON.parse(stripJsonComments(theme.data));
      const themeId = labelToId(theme.label);
      const base = uiThemeToBase(theme.uiTheme);
      const tokenRules = convertTokenColors(raw.tokenColors ?? raw["editor.tokenColorCustomizations"]?.textMateRules ?? []);
      const colors = raw.colors ?? {};
      monaco.editor.defineTheme(themeId, {
        base,
        inherit: true,
        rules: tokenRules,
        colors,
      });
    } catch {
      // malformed theme JSON – skip
    }
  }
}

function uiThemeToBase(uiTheme: string): Monaco.editor.BuiltinTheme {
  if (uiTheme === "vs") return "vs";
  if (uiTheme === "hc-black") return "hc-black";
  return "vs-dark";
}

function labelToId(label: string): string {
  return label.toLowerCase().replace(/\s+/g, "-").replace(/[^a-z0-9-]/g, "");
}

function convertTokenColors(tokenColors: any[]): Monaco.editor.ITokenThemeRule[] {
  const rules: Monaco.editor.ITokenThemeRule[] = [];
  for (const tc of tokenColors) {
    if (!tc.settings) continue;
    const scopes: string[] = typeof tc.scope === "string"
      ? tc.scope.split(",").map((s: string) => s.trim())
      : Array.isArray(tc.scope) ? tc.scope : [];
    for (const scope of scopes) {
      const rule: Monaco.editor.ITokenThemeRule = { token: scope };
      if (tc.settings.foreground) rule.foreground = tc.settings.foreground.replace("#", "");
      if (tc.settings.background) rule.background = tc.settings.background.replace("#", "");
      if (tc.settings.fontStyle) rule.fontStyle = tc.settings.fontStyle;
      rules.push(rule);
    }
  }
  return rules;
}

// --- Language Configurations ---

function applyLanguageConfigurations(monaco: typeof Monaco, contrib: ExtensionContributes): void {
  for (const lang of contrib.languages ?? []) {
    if (!lang.configuration || !lang.id) continue;
    try {
      const cfg = JSON.parse(stripJsonComments(lang.configuration));
      const langCfg: Monaco.languages.LanguageConfiguration = {};
      if (cfg.comments) langCfg.comments = cfg.comments;
      if (cfg.brackets) langCfg.brackets = cfg.brackets;
      if (cfg.autoClosingPairs) langCfg.autoClosingPairs = cfg.autoClosingPairs;
      if (cfg.surroundingPairs) langCfg.surroundingPairs = cfg.surroundingPairs;
      if (cfg.wordPattern) {
        try { langCfg.wordPattern = new RegExp(cfg.wordPattern); } catch { /* skip */ }
      }
      if (cfg.indentationRules) langCfg.indentationRules = {
        increaseIndentPattern: safeRegex(cfg.indentationRules.increaseIndentPattern),
        decreaseIndentPattern: safeRegex(cfg.indentationRules.decreaseIndentPattern),
      };
      // Register or update language
      const existing = monaco.languages.getLanguages().find((l) => l.id === lang.id);
      if (existing) {
        monaco.languages.setLanguageConfiguration(lang.id, langCfg);
      } else {
        const extensions = lang.extensions ?? [];
        const filenames = lang.filenames ?? [];
        monaco.languages.register({
          id: lang.id,
          extensions,
          filenames,
        });
        monaco.languages.setLanguageConfiguration(lang.id, langCfg);
      }
    } catch {
      // skip
    }
  }
}

function safeRegex(pattern: any): RegExp {
  if (!pattern) return /$/;
  try { return new RegExp(typeof pattern === "string" ? pattern : pattern.pattern); } catch { return /$/; }
}

// --- Token rules from TextMate grammars (best-effort approximation) ---
// Monaco doesn't support full TM grammars natively, but we can register
// basic token providers for languages not already known.

function applyGrammarTokenRules(monaco: typeof Monaco, contrib: ExtensionContributes): void {
  for (const grammar of contrib.grammars ?? []) {
    if (!grammar.data || !grammar.language) continue;
    try {
      const raw = JSON.parse(stripJsonComments(grammar.data));
      const langId = grammar.language;
      const langs = monaco.languages.getLanguages();
      const known = langs.find((l) => l.id === langId);
      if (!known) {
        monaco.languages.register({ id: langId });
      }
      const rules = buildSimpleTokenizer(raw);
      if (rules.length > 0) {
        monaco.languages.setMonarchTokensProvider(langId, {
          tokenizer: { root: rules as any },
        });
      }
    } catch {
      // skip malformed grammars
    }
  }
}

function buildSimpleTokenizer(grammar: any): [RegExp, string][] {
  const rules: [RegExp, string][] = [];
  const patterns: any[] = grammar.patterns ?? [];
  for (const p of patterns) {
    const match = p.match ?? p.begin;
    if (!match) continue;
    const name: string = p.name ?? "";
    const token = tmScopeToMonacoToken(name);
    if (token && match) {
      try {
        rules.push([new RegExp(match), token]);
      } catch {
        // skip invalid regex
      }
    }
  }
  return rules;
}

function tmScopeToMonacoToken(scope: string): string {
  if (!scope) return "";
  if (scope.includes("comment")) return "comment";
  if (scope.includes("string")) return "string";
  if (scope.includes("keyword")) return "keyword";
  if (scope.includes("number") || scope.includes("numeric")) return "number";
  if (scope.includes("constant")) return "number";
  if (scope.includes("entity.name.function")) return "identifier";
  if (scope.includes("entity.name.type")) return "type";
  if (scope.includes("storage.type")) return "keyword";
  if (scope.includes("support.type")) return "type";
  if (scope.includes("variable")) return "variable";
  if (scope.includes("punctuation")) return "delimiter";
  if (scope.includes("operator")) return "operator";
  return "";
}

// --- Snippets ---

function applySnippets(monaco: typeof Monaco, contrib: ExtensionContributes): void {
  for (const snippet of contrib.snippets ?? []) {
    if (!snippet.data || !snippet.language) continue;
    const langId = snippet.language;
    try {
      const raw = JSON.parse(stripJsonComments(snippet.data));
      const completions = buildSnippetCompletions(raw);
      if (completions.length === 0) continue;
      monaco.languages.registerCompletionItemProvider(langId, {
        provideCompletionItems: (model, position) => {
          const word = model.getWordUntilPosition(position);
          const range = {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          };
          return {
            suggestions: completions.map((c) => ({
              ...c,
              range,
              kind: monaco.languages.CompletionItemKind.Snippet,
            })),
          };
        },
      });
    } catch {
      // skip
    }
  }
}

function buildSnippetCompletions(raw: Record<string, any>): Omit<Monaco.languages.CompletionItem, "range">[] {
  const results: Omit<Monaco.languages.CompletionItem, "range">[] = [];
  for (const [name, def] of Object.entries(raw)) {
    if (!def || typeof def !== "object") continue;
    const prefix: string | string[] = def.prefix ?? name;
    const prefixes = Array.isArray(prefix) ? prefix : [prefix];
    const body: string[] = Array.isArray(def.body) ? def.body : [String(def.body ?? "")];
    const insertText = body.join("\n");
    for (const p of prefixes) {
      results.push({
        label: p,
        insertText,
        insertTextRules: 4 as Monaco.languages.CompletionItemInsertTextRule, // InsertAsSnippet
        documentation: def.description ?? name,
      } as Omit<Monaco.languages.CompletionItem, "range">);
    }
  }
  return results;
}

// --- Helpers ---

function stripJsonComments(json: string): string {
  // Remove single-line // comments and multi-line /* */ comments
  return json
    .replace(/\/\/[^\n]*/g, "")
    .replace(/\/\*[\s\S]*?\*\//g, "");
}
