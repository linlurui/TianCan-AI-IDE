import React, { useMemo, useState, useEffect, useRef, useCallback } from "react";
import { useTranslation } from "../i18n";
import {
  ChevronRight, ChevronDown, ScanSearch, Hash, Circle, Lock,
  Globe, Braces, Layers, FileCode, FileText, File,
  Box, Minus, Zap, BookOpen, Loader2, AlertTriangle, ListTree, Network,
} from "lucide-react";
import mammoth from "mammoth";
import * as XLSX from "xlsx";
import JSZip from "jszip";
import { ReadFileAsBase64 } from "../bindings/filesystem";
import { DocumentSymbol, SYMBOL_KIND_ICON, getActiveLspClient, getLspClient } from "../lsp/client";
import CallHierarchyPanel from "./CallHierarchyPanel";

interface AnalysisPanelProps {
  filePath: string | null;
  content: string | null;
  onGoToLine?: (line: number) => void;
  rootPath?: string | null;
  langId?: string | null;
  cursorLine?: number;
  cursorCol?: number;
  onOpenFile?: (path: string, line: number) => void;
  lspPort?: number;
  monaco?: any;
}

type FileCat = "html" | "css" | "ts" | "js" | "go" | "python" | "java" | "rust" | "cpp" | "php" | "ruby" | "vue" | "markdown" | "json" | "makefile" | "pdf" | "word" | "excel" | "pptx" | "other";

function getCategory(ext: string): FileCat {
  const m: Record<string, FileCat> = {
    html: "html", htm: "html", xml: "html",
    css: "css", scss: "css", less: "css", sass: "css",
    ts: "ts", tsx: "ts",
    js: "js", jsx: "js",
    go: "go",
    py: "python",
    java: "java", kt: "java", scala: "java",
    rs: "rust",
    c: "cpp", cpp: "cpp", cc: "cpp", h: "cpp", hpp: "cpp",
    php: "php",
    rb: "ruby",
    vue: "vue",
    md: "markdown", mdx: "markdown",
    json: "json",
    pdf: "pdf",
    doc: "word", docx: "word",
    xls: "excel", xlsx: "excel", csv: "excel",
    ppt: "pptx", pptx: "pptx",
    makefile: "makefile",
  };
  // Handle filenames without extension (Makefile, Dockerfile, etc.)
  const lower = ext.toLowerCase();
  if (m[lower]) return m[lower];
  if (lower === "makefile" || lower === "gnumakefile") return "makefile";
  return "other";
}

// ── HTML ──────────────────────────────────────────────────────

interface HtmlNode { tag: string; attrs: string; line: number; children: HtmlNode[] }

/**
 * Parse HTML and annotate every node with its source line number.
 * Strategy: scan source lines for opening-tag patterns, build an ordered
 * queue, then walk the DOMParser tree and pop the matching entry per node.
 * lineOffset shifts all line numbers (used for Vue template blocks).
 */
function parseHtmlWithLines(content: string, lineOffset = 0): HtmlNode | null {
  // Build ordered queue: [{tag, line}, ...] from source text
  const queue: { tag: string; line: number }[] = [];
  content.split("\n").forEach((ln, i) => {
    const re = /<([a-zA-Z][a-zA-Z0-9-]*)/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(ln)) !== null) {
      queue.push({ tag: m[1].toLowerCase(), line: i + 1 + lineOffset });
    }
  });

  let qi = 0;

  function assignLines(el: Element): HtmlNode {
    const tag = el.tagName.toLowerCase();
    let lineNum = 0;
    for (let i = qi; i < queue.length; i++) {
      if (queue[i].tag === tag) {
        lineNum = queue[i].line;
        qi = i + 1;
        break;
      }
    }
    const id = el.getAttribute("id");
    const cls = el.getAttribute("class");
    let attrs = "";
    if (id) attrs += `#${id}`;
    if (cls) attrs += cls.split(/\s+/).filter(Boolean).map((c) => `.${c}`).join("");
    return { tag, attrs, line: lineNum, children: Array.from(el.children).map(assignLines) };
  }

  try {
    const doc = new DOMParser().parseFromString(content, "text/html");
    return assignLines(doc.documentElement);
  } catch { return null; }
}

// ── CSS ──────────────────────────────────────────────────────

type CssType = "element" | "class" | "id" | "pseudo" | "media" | "keyframes" | "other";
interface CssRule { selector: string; propCount: number; type: CssType; line: number }

function parseCss(content: string, lineOffset = 0): CssRule[] {
  const rules: CssRule[] = [];
  // Replace comments with whitespace of same length to preserve char positions
  const cleaned = content.replace(/\/\*[\s\S]*?\*\//g, (m) => " ".repeat(m.length));
  // Build line-start positions for pos→line lookup
  const lineStarts: number[] = [0];
  for (let i = 0; i < cleaned.length; i++) {
    if (cleaned[i] === "\n") lineStarts.push(i + 1);
  }
  const posToLine = (pos: number): number => {
    let lo = 0, hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= pos) lo = mid; else hi = mid - 1;
    }
    return lo + 1 + lineOffset;
  };
  const re = /([^{}]+?)\s*\{([^{}]*)\}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(cleaned)) !== null) {
    const sel = m[1].trim().replace(/\s+/g, " ");
    const props = (m[2].match(/:\s*[^;{}]+;/g) ?? []).length;
    if (!sel || sel.length > 200) continue;
    let type: CssType = "element";
    if (/^@media|^@supports/.test(sel)) type = "media";
    else if (/^@keyframes/.test(sel)) type = "keyframes";
    else if (/#/.test(sel)) type = "id";
    else if (/\./.test(sel)) type = "class";
    else if (/:/.test(sel)) type = "pseudo";
    else if (/^[a-zA-Z*]/.test(sel)) type = "element";
    else type = "other";
    rules.push({ selector: sel, propCount: props, type, line: posToLine(m.index) });
  }
  return rules;
}

// ── Code symbols ─────────────────────────────────────────────

type Vis = "public" | "private" | "protected";
type SymKind = "class" | "interface" | "function" | "method" | "constructor";

interface Sym {
  name: string; kind: SymKind; vis: Vis;
  isStatic: boolean; isAsync: boolean; line: number;
  children?: Sym[];
}

const SKIP_WORDS = new Set(["if","else","for","while","switch","return","const","let","var","new","this","super","throw","try","catch","import","export","default","from","case","break","continue","typeof","instanceof"]);

function parseTs(content: string): Sym[] {
  const lines = content.split("\n");
  const top: Sym[] = [];
  let cur: Sym | null = null;
  let depth = 0;
  let classBodyDepth = -1; // depth AFTER class opening brace

  lines.forEach((line, i) => {
    const t = line.trim();
    const depthBefore = depth;
    depth += (t.match(/\{/g) ?? []).length;
    depth -= (t.match(/\}/g) ?? []).length;

    // Exit class scope when depth falls below class body level
    if (cur && depth < classBodyDepth) { cur = null; classBodyDepth = -1; }

    const classM = t.match(/^(?:export\s+)?(?:abstract\s+)?class\s+(\w+)/);
    if (classM) {
      cur = { name: classM[1], kind: "class", vis: "public", isStatic: false, isAsync: false, line: i + 1, children: [] };
      classBodyDepth = t.includes("{") ? depth : depth + 1;
      top.push(cur);
      return;
    }
    const ifaceM = t.match(/^(?:export\s+)?interface\s+(\w+)/);
    if (ifaceM) {
      const s: Sym = { name: ifaceM[1], kind: "interface", vis: "public", isStatic: false, isAsync: false, line: i + 1, children: [] };
      top.push(s); cur = s;
      classBodyDepth = t.includes("{") ? depth : depth + 1;
      return;
    }
    if (!cur) {
      const fnM = t.match(/^(?:export\s+)?(?:async\s+)?function\s+(\w+)/);
      if (fnM) { top.push({ name: fnM[1], kind: "function", vis: t.includes("export") ? "public" : "private", isStatic: false, isAsync: t.includes("async"), line: i + 1 }); return; }
      const arM = t.match(/^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|\w+)\s*=>/);
      if (arM) { top.push({ name: arM[1], kind: "function", vis: t.includes("export") ? "public" : "private", isStatic: false, isAsync: t.includes("async"), line: i + 1 }); return; }
    } else if (depthBefore === classBodyDepth) {
      // Only detect members at the direct class body level (not inside method bodies)
      if (/^constructor\s*\(/.test(t)) {
        cur.children!.push({ name: "constructor", kind: "constructor", vis: "public", isStatic: false, isAsync: false, line: i + 1 });
        return;
      }
      const mm = t.match(/^(?:(public|private|protected)\s+)?(?:(static)\s+)?(?:(async)\s+)?(\w+)\s*[(<]/);
      if (mm) {
        const [, vis, st, async_, name] = mm;
        if (name && !SKIP_WORDS.has(name) && !/^[A-Z_]{2,}$/.test(name)) {
          cur.children!.push({ name, kind: "method", vis: (vis as Vis) || "public", isStatic: !!st, isAsync: !!async_, line: i + 1 });
        }
      }
    }
  });
  return top;
}

const JAVA_KW = new Set(["if","else","for","while","do","switch","case","try","catch","finally","return","throw","new","this","super","import","package","extends","implements","instanceof"]);

function parseJava(content: string): Sym[] {
  const lines = content.split("\n");
  const top: Sym[] = [];
  let cur: Sym | null = null;
  let depth = 0;
  let classBodyDepth = -1;

  lines.forEach((line, i) => {
    const t = line.trim();
    // Skip blank lines, single-line comments, javadoc lines, annotations, package/import
    if (!t || t.startsWith("//") || t.startsWith("*") || t.startsWith("/*") || t.startsWith("@") || t.startsWith("import ") || t.startsWith("package ")) return;

    const depthBefore = depth;
    depth += (t.match(/\{/g) ?? []).length;
    depth -= (t.match(/\}/g) ?? []).length;

    // Exit class scope
    if (cur !== null && depth < classBodyDepth) { cur = null; classBodyDepth = -1; }

    // Class / Interface / Enum
    const clsM = t.match(/\b(class|interface|enum)\s+(\w+)/);
    if (clsM && !JAVA_KW.has(clsM[2])) {
      const vis: Vis = t.includes("private") ? "private" : t.includes("protected") ? "protected" : "public";
      const kind: SymKind = clsM[1] === "interface" ? "interface" : "class";
      const s: Sym = { name: clsM[2], kind, vis, isStatic: t.includes("static"), isAsync: false, line: i + 1, children: [] };
      top.push(s);
      cur = s;
      classBodyDepth = t.includes("{") ? depth : depth + 1;
      return;
    }

    if (!cur) return;
    // Only detect members at direct class body depth
    if (depthBefore !== classBodyDepth) return;

    // Constructor: ClassName(
    const ctorRe = new RegExp(`^(?:(?:public|private|protected)\\s+)?${cur.name}\\s*\\(`);
    if (ctorRe.test(t)) {
      const vis: Vis = t.includes("private") ? "private" : t.includes("protected") ? "protected" : "public";
      cur.children!.push({ name: cur.name, kind: "constructor", vis, isStatic: false, isAsync: false, line: i + 1 });
      return;
    }

    // Method: one or more Java modifiers then returnType then name(
    const methM = t.match(/^((?:(?:public|private|protected|static|final|synchronized|abstract|native|default|strictfp)\s+)+)(?:[\w<>\[\].,?\s]+?\s+)?(\w+)\s*\(/);
    if (methM) {
      const name = methM[2];
      if (!JAVA_KW.has(name) && name !== cur.name && /^[a-z_$]/.test(name)) {
        const mods = methM[1];
        const vis: Vis = mods.includes("private") ? "private" : mods.includes("protected") ? "protected" : "public";
        cur.children!.push({ name, kind: "method", vis, isStatic: mods.includes("static"), isAsync: false, line: i + 1 });
      }
    }
  });
  return top;
}

function parseGo(content: string): Sym[] {
  const lines = content.split("\n");
  const top: Sym[] = [];
  const byType = new Map<string, Sym>();
  lines.forEach((line, i) => {
    const t = line.trim();
    const structM = t.match(/^type\s+(\w+)\s+struct/);
    if (structM) {
      const s: Sym = { name: structM[1], kind: "class", vis: /^[A-Z]/.test(structM[1]) ? "public" : "private", isStatic: false, isAsync: false, line: i + 1, children: [] };
      top.push(s); byType.set(structM[1], s); return;
    }
    const ifM = t.match(/^type\s+(\w+)\s+interface/);
    if (ifM) { const s: Sym = { name: ifM[1], kind: "interface", vis: /^[A-Z]/.test(ifM[1]) ? "public" : "private", isStatic: false, isAsync: false, line: i + 1, children: [] }; top.push(s); return; }
    const methM = t.match(/^func\s+\(\w+\s+\*?(\w+)\)\s+(\w+)\s*\(/);
    if (methM) {
      const [, typeName, fn] = methM;
      const parent = byType.get(typeName);
      const s: Sym = { name: fn, kind: "method", vis: /^[A-Z]/.test(fn) ? "public" : "private", isStatic: false, isAsync: false, line: i + 1 };
      if (parent?.children) parent.children.push(s); else top.push(s);
      return;
    }
    const fnM = t.match(/^func\s+(\w+)\s*\(/);
    if (fnM) top.push({ name: fnM[1], kind: "function", vis: /^[A-Z]/.test(fnM[1]) ? "public" : "private", isStatic: false, isAsync: false, line: i + 1 });
  });
  return top;
}

function parsePython(content: string): Sym[] {
  const lines = content.split("\n");
  const top: Sym[] = [];
  let cur: Sym | null = null;
  lines.forEach((line, i) => {
    const clsM = line.match(/^class\s+(\w+)/);
    if (clsM) {
      cur = { name: clsM[1], kind: "class", vis: clsM[1].startsWith("_") ? "private" : "public", isStatic: false, isAsync: false, line: i + 1, children: [] };
      top.push(cur); return;
    }
    const defM = line.match(/^(\s*)(?:async\s+)?def\s+(\w+)/);
    if (defM) {
      const [, indent, name] = defM;
      const vis: Vis = name.startsWith("__") && !name.endsWith("__") ? "private" : name.startsWith("_") ? "protected" : "public";
      const s: Sym = { name, kind: indent.length > 0 ? "method" : "function", vis, isStatic: false, isAsync: line.includes("async"), line: i + 1 };
      if (indent.length > 0 && cur?.children) cur.children.push(s);
      else { if (indent.length === 0) cur = null; top.push(s); }
    }
  });
  return top;
}

// ── Go struct-literal call blocks ───────────────────────────

interface GoMapKey {
  key: string;
  line: number;
}

interface GoCallField {
  name: string;
  kind: "map" | "func" | "other";
  keys?: GoMapKey[];
  line: number;
}

interface GoCallBlock {
  callName: string;
  line: number;
  fields: GoCallField[];
}

function parseGoCallBlocks(content: string): GoCallBlock[] {
  const lines = content.split("\n");
  const blocks: GoCallBlock[] = [];

  for (let i = 0; i < lines.length; i++) {
    const t = lines[i].trim();
    // Match: identifier.identifier(&Type{ or identifier(&Type{
    const m = t.match(/^(\w+(?:\.\w+)*)\s*\(\s*&?\s*\w+\s*\{/);
    if (!m) continue;

    const callName = m[1];
    let depth = 0;
    for (const ch of t) { if (ch === "{") depth++; else if (ch === "}") depth--; }
    if (depth <= 0) continue; // single-line call, skip

    const fields: GoCallField[] = [];
    let currentMapField: GoCallField | null = null;

    for (let j = i + 1; j < lines.length; j++) {
      const lt = lines[j].trim();
      if (!lt) continue;

      const depthBefore = depth;
      for (const ch of lt) { if (ch === "{") depth++; else if (ch === "}") depth--; }
      if (depth <= 0) break;

      if (depthBefore === 1) {
        const fm = lt.match(/^(\w+)\s*:\s*(.*)$/);
        if (fm) {
          const [, fname, rest] = fm;
          currentMapField = null;
          if (/^func[\s(\[]/.test(rest.trim())) {
            fields.push({ name: fname, kind: "func", line: j + 1 });
          } else if (rest.includes("{")) {
            const f: GoCallField = { name: fname, kind: "map", keys: [], line: j + 1 };
            fields.push(f);
            currentMapField = f;
          } else {
            fields.push({ name: fname, kind: "other", line: j + 1 });
          }
        }
      } else if (depthBefore === 2 && currentMapField?.kind === "map") {
        const km = lt.match(/^"([^"]+)"\s*:/);
        if (km) currentMapField.keys!.push({ key: km[1], line: j + 1 });
      }
      if (depth < 2 && currentMapField) currentMapField = null;
    }

    if (fields.length > 0) blocks.push({ callName, line: i + 1, fields });
  }
  return blocks;
}

function parseGeneric(content: string): Sym[] {
  const lines = content.split("\n");
  const top: Sym[] = [];
  let cur: Sym | null = null;
  lines.forEach((line, i) => {
    const t = line.trim();
    const clsM = t.match(/(?:^|(?:public|private|internal)\s+)(?:class|struct|impl|interface|enum)\s+(\w+)/);
    if (clsM) {
      const vis: Vis = t.includes("private") ? "private" : "public";
      cur = { name: clsM[1], kind: t.includes("interface") ? "interface" : "class", vis, isStatic: false, isAsync: false, line: i + 1, children: [] };
      top.push(cur); return;
    }
    // Rust fn / PHP function
    const fnM = t.match(/^(?:pub(?:\(crate\))?\s+)?(?:async\s+)?fn\s+(\w+)|^(?:function\s+)(\w+)/);
    if (fnM) {
      const name = fnM[1] || fnM[2];
      const s: Sym = { name, kind: cur ? "method" : "function", vis: t.startsWith("pub") ? "public" : "private", isStatic: false, isAsync: t.includes("async"), line: i + 1 };
      if (cur?.children) cur.children.push(s); else top.push(s);
    }
  });
  return top;
}

function getSymbols(content: string, cat: FileCat): Sym[] {
  if (cat === "ts" || cat === "js") return parseTs(content);
  if (cat === "go") return parseGo(content);
  if (cat === "python") return parsePython(content);
  if (cat === "java") return parseJava(content);
  if (cat === "makefile") return []; // handled by parseMakefile separately
  return parseGeneric(content);
}

// ── Makefile ──────────────────────────────────────────────────

interface MakeTarget {
  name: string;
  deps: string[];
  line: number;
  isPhony: boolean;
  recipe: string[];
}

function parseMakefile(content: string): MakeTarget[] {
  const targets: MakeTarget[] = [];
  const phonySet = new Set<string>();
  const lines = content.split("\n");

  // First pass: collect .PHONY targets
  for (const line of lines) {
    const t = line.trim();
    if (/^\.PHONY\s*:/.test(t)) {
      const parts = t.replace(/^\.PHONY\s*:\s*/, "").split(/\s+/).filter(Boolean);
      parts.forEach((p) => phonySet.add(p));
    }
  }

  // Second pass: collect targets
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // Match target: dep1 dep2 (skip lines that start with tab = recipe)
    // Also skip assignment lines (VAR = ..., VAR := ..., VAR ?= ..., VAR += ...)
    const m = line.match(/^([a-zA-Z_][\w.-]*)\s*:\s*([^\#]*?)(\s*\#.*)?$/);
    if (!m) continue;
    const name = m[1];
    // Skip if it looks like an assignment (e.g. "VAR := value")
    if (/^\w+\s*[:?+]?=/.test(line)) continue;
    // Skip include/undef/export/unexport lines
    if (/^(include|undef|export|unexport)\b/.test(name)) continue;
    const depStr = m[2].trim();
    const deps = depStr ? depStr.split(/\s+/).filter(Boolean) : [];
    const recipe: string[] = [];
    // Collect recipe lines (lines starting with tab)
    for (let j = i + 1; j < lines.length; j++) {
      if (lines[j].startsWith("\t")) {
        recipe.push(lines[j].substring(1).trim());
      } else if (lines[j].trim() === "") {
        continue; // blank line between recipe lines
      } else {
        break;
      }
    }
    targets.push({ name, deps, line: i + 1, isPhony: phonySet.has(name), recipe });
  }

  return targets;
}

// ── Vue SFC ───────────────────────────────────────────────────

interface VueSfc { template: HtmlNode | null; symbols: Sym[]; cssRules: CssRule[] }

function parseVue(content: string): VueSfc {
  const templateM = content.match(/<template[^>]*>([\s\S]*?)<\/template>/i);
  const scriptM = content.match(/<script[^>]*>([\s\S]*?)<\/script>/i);
  const styleM = content.match(/<style[^>]*>([\s\S]*?)<\/style>/i);

  let template: HtmlNode | null = null;
  if (templateM && templateM.index !== undefined) {
    const offset = content.slice(0, templateM.index).split("\n").length;
    template = parseHtmlWithLines(templateM[1], offset);
    // unwrap: DOMParser adds <html><head><body>; grab the first meaningful child
    if (template) {
      const body = template.children.find((c) => c.tag === "body") ?? template;
      template = body.children[0] ?? null;
    }
  }
  const symbols = scriptM ? parseTs(scriptM[1]) : [];
  let cssRules: CssRule[] = [];
  if (styleM && styleM.index !== undefined) {
    const styleOffset = content.slice(0, styleM.index).split("\n").length;
    cssRules = parseCss(styleM[1], styleOffset);
  }
  return { template, symbols, cssRules };
}

// ── Markdown renderer ─────────────────────────────────────────

function renderMarkdown(raw: string): string {
  const codeBlocks: string[] = [];
  const inlineCodes: string[] = [];

  let s = raw.replace(/```([\w]*)\n?([\s\S]*?)```/g, (_, lang, code) => {
    const esc = code.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    codeBlocks.push(`<pre class="md-pre"><code class="md-code-block lang-${lang}">${esc.trimEnd()}</code></pre>`);
    return `\x00CB${codeBlocks.length - 1}\x00`;
  });
  s = s.replace(/`([^`\n]+)`/g, (_, c) => {
    const esc = c.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    inlineCodes.push(`<code class="md-code">${esc}</code>`);
    return `\x00IC${inlineCodes.length - 1}\x00`;
  });

  s = s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

  s = s.replace(/^#{6} (.+)$/gm, "<h6>$1</h6>");
  s = s.replace(/^#{5} (.+)$/gm, "<h5>$1</h5>");
  s = s.replace(/^#{4} (.+)$/gm, "<h4>$1</h4>");
  s = s.replace(/^### (.+)$/gm, "<h3>$1</h3>");
  s = s.replace(/^## (.+)$/gm, "<h2>$1</h2>");
  s = s.replace(/^# (.+)$/gm, "<h1>$1</h1>");
  s = s.replace(/^[-*]{3,}$/gm, "<hr>");
  s = s.replace(/^&gt; (.+)$/gm, "<blockquote>$1</blockquote>");
  s = s.replace(/\*\*\*(.+?)\*\*\*/g, "<strong><em>$1</em></strong>");
  s = s.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/\*(.+?)\*/g, "<em>$1</em>");
  s = s.replace(/~~(.+?)~~/g, "<del>$1</del>");
  s = s.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, "<img alt=\"$1\" src=\"$2\" style=\"max-width:100%\">");
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, "<a href=\"$2\" target=\"_blank\" rel=\"noopener\">$1</a>");
  s = s.replace(/^[-*+] (.+)$/gm, "<li>$1</li>");
  s = s.replace(/^\d+\. (.+)$/gm, "<li>$1</li>");
  s = s.replace(/(<li>[\s\S]+?<\/li>\n?)+/g, (m) => `<ul>${m}</ul>`);

  s = s.split(/\n\n+/).map((block) => {
    block = block.trim();
    if (!block) return "";
    if (/^<(h[1-6]|ul|ol|li|blockquote|hr|pre|img)/.test(block) || block.startsWith("\x00CB")) return block;
    return `<p>${block.replace(/\n/g, "<br>")}</p>`;
  }).join("\n");

  s = s.replace(/\x00CB(\d+)\x00/g, (_, i) => codeBlocks[+i]);
  s = s.replace(/\x00IC(\d+)\x00/g, (_, i) => inlineCodes[+i]);
  return s;
}

// ── UI Components ─────────────────────────────────────────────

function VisIcon({ vis, isStatic }: { vis: Vis; isStatic: boolean }) {
  if (vis === "private") return <Lock size={10} className="ana-vis-icon private" />;
  if (vis === "protected") return <Lock size={10} className="ana-vis-icon protected" />;
  return isStatic ? <Minus size={10} className="ana-vis-icon static" /> : <Circle size={10} className="ana-vis-icon public" />;
}

function HtmlNodeView({ node, depth = 0, onGoToLine }: { node: HtmlNode; depth?: number; onGoToLine?: (line: number) => void }) {
  const [open, setOpen] = useState(depth < 3);
  const { t } = useTranslation();
  const hasChildren = node.children.length > 0;
  const handleClick = () => {
    if (hasChildren) setOpen(!open);
    if (node.line > 0) onGoToLine?.(node.line);
  };
  return (
    <div className="ana-tree-node" style={{ paddingLeft: depth * 12 }}>
      <div className="ana-tree-row clickable" onClick={handleClick} title={node.line > 0 ? t("analysis.line", { n: node.line }) : undefined}>
        <span className="ana-tree-arrow">
          {hasChildren ? (open ? <ChevronDown size={10} /> : <ChevronRight size={10} />) : <span style={{ width: 10 }} />}
        </span>
        <span className="ana-tag-name">&lt;{node.tag}&gt;</span>
        {node.attrs && <span className="ana-tag-attrs">{node.attrs}</span>}
        {hasChildren && <span className="ana-count">{node.children.length}</span>}
        {node.line > 0 && <span className="ana-line-num">{node.line}</span>}
      </div>
      {open && hasChildren && node.children.map((c, i) => (
        <HtmlNodeView key={i} node={c} depth={depth + 1} onGoToLine={onGoToLine} />
      ))}
    </div>
  );
}

function CssGroup({ label, rules, icon, onGoToLine }: { label: string; rules: CssRule[]; icon: React.ReactNode; onGoToLine?: (l: number) => void }) {
  const [open, setOpen] = useState(true);
  const { t } = useTranslation();
  if (rules.length === 0) return null;
  return (
    <div className="ana-section">
      <div className="ana-section-header" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
        {icon}
        <span>{label}</span>
        <span className="ana-count">{rules.length}</span>
      </div>
      {open && (
        <div className="ana-section-body">
          {rules.map((r, i) => (
            <div
              key={i}
              className={`ana-css-rule${onGoToLine ? " clickable" : ""}`}
              onClick={() => r.line > 0 && onGoToLine?.(r.line)}
              title={r.line > 0 ? t("analysis.line", { n: r.line }) : undefined}
            >
              <span className="ana-css-sel">{r.selector}</span>
              {r.propCount > 0 && <span className="ana-count">{r.propCount}</span>}
              {r.line > 0 && <span className="ana-line-num">{r.line}</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function GoCallFieldView({ field, depth, onGoToLine }: { field: GoCallField; depth: number; onGoToLine?: (l: number) => void }) {
  const [open, setOpen] = useState(true);
  const { t } = useTranslation();
  const hasKeys = field.kind === "map" && (field.keys?.length ?? 0) > 0;
  return (
    <div className="ana-tree-node" style={{ paddingLeft: depth * 12 }}>
      <div
        className="ana-tree-row clickable"
        onClick={() => { if (hasKeys) setOpen(!open); if (field.line > 0) onGoToLine?.(field.line); }}
        title={field.line > 0 ? t("analysis.line", { n: field.line }) : undefined}
      >
        <span className="ana-tree-arrow">
          {hasKeys ? (open ? <ChevronDown size={10} /> : <ChevronRight size={10} />) : <span style={{ width: 10 }} />}
        </span>
        {field.kind === "func" ? <Zap size={12} className="ana-kind-icon method" /> : <Box size={12} className="ana-kind-icon class" />}
        <span className="ana-sym-name public">{field.name}</span>
        {field.kind === "func" && <span className="ana-badge kind">func</span>}
        {hasKeys && <span className="ana-count">{field.keys!.length}</span>}
        {field.line > 0 && <span className="ana-line-num">{field.line}</span>}
      </div>
      {open && hasKeys && field.keys!.map((k, ki) => (
        <div key={ki} className="ana-tree-node" style={{ paddingLeft: (depth + 1) * 12 }}>
          <div
            className={`ana-tree-row${k.line > 0 ? " clickable" : ""}`}
            onClick={() => k.line > 0 && onGoToLine?.(k.line)}
            title={k.line > 0 ? t("analysis.line", { n: k.line }) : undefined}
          >
            <span className="ana-tree-arrow"><span style={{ width: 10 }} /></span>
            <span className="json-key">&quot;{k.key}&quot;</span>
            {k.line > 0 && <span className="ana-line-num">{k.line}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

function GoCallBlockView({ block, onGoToLine }: { block: GoCallBlock; onGoToLine?: (l: number) => void }) {
  const [open, setOpen] = useState(true);
  const { t } = useTranslation();
  return (
    <div className="ana-tree-node">
      <div
        className="ana-tree-row clickable"
        onClick={() => { setOpen(!open); if (block.line > 0) onGoToLine?.(block.line); }}
        title={t("analysis.line", { n: block.line })}
      >
        <span className="ana-tree-arrow">{open ? <ChevronDown size={10} /> : <ChevronRight size={10} />}</span>
        <Braces size={12} className="ana-kind-icon function" />
        <span className="ana-sym-name public">{block.callName}</span>
        <span className="ana-count">{block.fields.length}</span>
        <span className="ana-line-num">{block.line}</span>
      </div>
      {open && block.fields.map((f, fi) => (
        <GoCallFieldView key={fi} field={f} depth={1} onGoToLine={onGoToLine} />
      ))}
    </div>
  );
}

function SymbolView({ sym, depth = 0, onGoToLine }: { sym: Sym; depth?: number; onGoToLine?: (line: number) => void }) {
  const [open, setOpen] = useState(true);
  const { t } = useTranslation();
  const hasChildren = sym.children && sym.children.length > 0;
  const KindIcon = sym.kind === "class" ? Box
    : sym.kind === "interface" ? Layers
    : sym.kind === "function" ? Zap
    : sym.kind === "constructor" ? BookOpen
    : Braces;
  const handleRowClick = () => {
    if (hasChildren) setOpen(!open);
    if (sym.line > 0) onGoToLine?.(sym.line);
  };
  return (
    <div className="ana-tree-node" style={{ paddingLeft: depth * 12 }}>
      <div className="ana-tree-row clickable" onClick={handleRowClick} title={t("analysis.line", { n: sym.line })}>
        <span className="ana-tree-arrow">
          {hasChildren ? (open ? <ChevronDown size={10} /> : <ChevronRight size={10} />) : <span style={{ width: 10 }} />}
        </span>
        <KindIcon size={12} className={`ana-kind-icon ${sym.kind}`} />
        <VisIcon vis={sym.vis} isStatic={sym.isStatic} />
        <span className={`ana-sym-name ${sym.vis}`}>{sym.name}</span>
        {sym.isAsync && <span className="ana-badge async">async</span>}
        {sym.isStatic && <span className="ana-badge static">static</span>}
        {sym.kind === "function" || sym.kind === "method" ? <span className="ana-badge kind">fn</span> : null}
        <span className="ana-line-num">{sym.line}</span>
      </div>
      {open && hasChildren && sym.children!.map((c, i) => (
        <SymbolView key={i} sym={c} depth={depth + 1} onGoToLine={onGoToLine} />
      ))}
    </div>
  );
}

// ── JSON Tree ─────────────────────────────────────────────────

type JsonVal = string | number | boolean | null | JsonVal[] | { [k: string]: JsonVal };

function jsonType(v: JsonVal): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  return typeof v;
}

function jsonPreview(v: JsonVal): string {
  if (v === null) return "null";
  if (Array.isArray(v)) return `[${v.length}]`;
  if (typeof v === "object") return `{${Object.keys(v).length}}`;
  if (typeof v === "string") return `"${v.length > 40 ? v.slice(0, 40) + "…" : v}"`;
  return String(v);
}

/** Build a map of JSON key name → first line number (1-based) by scanning raw text. */
function buildJsonLineMap(content: string): Map<string, number> {
  const map = new Map<string, number>();
  const lines = content.split("\n");
  const keyRe = /^\s*"([^"\\]+)"\s*:/;
  lines.forEach((line, i) => {
    const m = line.match(keyRe);
    if (m && !map.has(m[1])) map.set(m[1], i + 1);
  });
  return map;
}

function JsonNodeView({
  label, value, depth = 0, lineMap, onGoToLine,
}: {
  label: string; value: JsonVal; depth?: number;
  lineMap: Map<string, number>; onGoToLine?: (line: number) => void;
}) {
  const isComplex = typeof value === "object" && value !== null;
  const [open, setOpen] = useState(depth < 2);
  const { t } = useTranslation();
  const type = jsonType(value);
  const line = lineMap.get(label) ?? 0;
  const children = isComplex
    ? Array.isArray(value)
      ? value.map((v, i) => ({ key: String(i), val: v as JsonVal }))
      : Object.entries(value as Record<string, JsonVal>).map(([k, v]) => ({ key: k, val: v as JsonVal }))
    : [];

  const handleClick = () => {
    if (isComplex) setOpen(!open);
    if (line > 0) onGoToLine?.(line);
  };

  return (
    <div className="ana-tree-node" style={{ paddingLeft: depth * 12 }}>
      <div className="ana-tree-row clickable" onClick={handleClick} title={line > 0 ? t("analysis.line", { n: line }) : undefined}>
        <span className="ana-tree-arrow">
          {isComplex ? (open ? <ChevronDown size={10} /> : <ChevronRight size={10} />) : <span style={{ width: 10 }} />}
        </span>
        <span className="json-key">{label}</span>
        <span className={`json-type ${type}`}>{type}</span>
        {!open && isComplex && <span className="json-preview">{jsonPreview(value)}</span>}
        {!isComplex && <span className={`json-val ${type}`}>{jsonPreview(value)}</span>}
        {line > 0 && <span className="ana-line-num">{line}</span>}
      </div>
      {open && children.map(({ key, val }) => (
        <JsonNodeView key={key} label={key} value={val} depth={depth + 1} lineMap={lineMap} onGoToLine={onGoToLine} />
      ))}
    </div>
  );
}

// ── Vue SFC Panel ─────────────────────────────────────────────

type VueSfcTab = "template" | "script" | "style";
function VueSfcPanel({ sfc, onGoToLine }: { sfc: VueSfc; onGoToLine?: (line: number) => void }) {
  const [tab, setTab] = useState<VueSfcTab>("template");
  const { t } = useTranslation();

  const tabs: { id: VueSfcTab; label: string; icon: React.ReactNode; count?: number }[] = [
    { id: "template", label: t("analysis.vueTemplate"), icon: <Globe size={11} /> },
    { id: "script",   label: t("analysis.vueScript"), icon: <FileCode size={11} />, count: sfc.symbols.length },
    { id: "style",    label: t("analysis.vueStyle"), icon: <FileText size={11} />, count: sfc.cssRules.length },
  ];

  return (
    <div className="vue-sfc-panel">
      <div className="vue-sfc-tabs">
        {tabs.map((t) => (
          <button
            key={t.id}
            className={`vue-sfc-tab${tab === t.id ? " active" : ""}`}
            onClick={() => setTab(t.id)}
          >
            {t.icon}
            <span>{t.label}</span>
            {t.count != null && t.count > 0 && <span className="vue-sfc-tab-badge">{t.count}</span>}
          </button>
        ))}
      </div>

      <div className="vue-sfc-body">
        {/* 模版 */}
        {tab === "template" && (
          sfc.template ? (
            <div className="ana-section-body">
              <HtmlNodeView node={sfc.template} depth={0} onGoToLine={onGoToLine} />
            </div>
          ) : (
            <div className="analysis-empty-inline">{t("analysis.noHtmlBlock")}</div>
          )
        )}

        {/* 代码 */}
        {tab === "script" && (
          sfc.symbols.length > 0 ? (
            <div className="ana-section-body">
              {sfc.symbols.map((s, i) => <SymbolView key={i} sym={s} onGoToLine={onGoToLine} />)}
            </div>
          ) : (
            <div className="analysis-empty-inline">{t("analysis.noScriptBlock")}</div>
          )
        )}

        {/* 样式 */}
        {tab === "style" && (
          sfc.cssRules.length > 0 ? (
            <div className="ana-section-body">
              <CssGroup label={t("analysis.cssElement")}  rules={sfc.cssRules.filter(r => r.type === "element")}  icon={<FileText size={11} />} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssClass")}    rules={sfc.cssRules.filter(r => r.type === "class")}    icon={<span className="ana-dot">.</span>} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssId")}   rules={sfc.cssRules.filter(r => r.type === "id")}       icon={<Hash size={11} />} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssPseudo")} rules={sfc.cssRules.filter(r => r.type === "pseudo")} icon={<span className="ana-dot">:</span>} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssMedia")}    rules={sfc.cssRules.filter(r => r.type === "media")}    icon={<span className="ana-dot">@</span>} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssKeyframes")} rules={sfc.cssRules.filter(r => r.type === "keyframes" || r.type === "other")} icon={<span className="ana-dot">%</span>} onGoToLine={onGoToLine} />
            </div>
          ) : (
            <div className="analysis-empty-inline">{t("analysis.noStyleBlock")}</div>
          )
        )}
      </div>
    </div>
  );
}

function LspSymbolNode({ sym, depth = 0, onGoToLine }: { sym: DocumentSymbol; depth?: number; onGoToLine?: (line: number) => void }) {
  const [open, setOpen] = useState(true);
  const { t } = useTranslation();
  const hasChildren = sym.children && sym.children.length > 0;
  const icon = SYMBOL_KIND_ICON[sym.kind] ?? "🔹";
  const line = sym.selectionRange.startLine + 1;
  return (
    <div className="ana-tree-node" style={{ paddingLeft: depth * 12 }}>
      <div
        className="ana-tree-row clickable"
        onClick={() => { if (hasChildren) setOpen(!open); onGoToLine?.(line); }}
        title={t("analysis.line", { n: line })}
      >
        <span className="ana-tree-arrow">
          {hasChildren ? (open ? <ChevronDown size={10} /> : <ChevronRight size={10} />) : <span style={{ width: 10 }} />}
        </span>
        <span style={{ fontSize: 12, lineHeight: 1 }}>{icon}</span>
        <span className="ana-sym-name public">{sym.name}</span>
        {sym.detail && <span className="ana-tag-attrs">{sym.detail}</span>}
        <span className="ana-line-num">{line}</span>
      </div>
      {open && hasChildren && sym.children!.map((c, i) => (
        <LspSymbolNode key={i} sym={c} depth={depth + 1} onGoToLine={onGoToLine} />
      ))}
    </div>
  );
}

// ── Main Component ────────────────────────────────────────────

export default function AnalysisPanel({ filePath, content, onGoToLine, rootPath, langId, cursorLine = 1, cursorCol = 1, onOpenFile, lspPort, monaco }: AnalysisPanelProps) {
  const { t } = useTranslation();
  const fileName = filePath?.split("/").pop() ?? "";
  const ext = fileName.includes(".") ? fileName.split(".").pop()?.toLowerCase() ?? "" : fileName.toLowerCase();
  const cat = getCategory(ext);
  const lineCount = content?.split("\n").length ?? 0;

  // ── LSP 大纲 & 调用层次 ───────────────────────────────────────
  const [lspOpen, setLspOpen]         = useState(true);
  const [callHierOpen, setCallHierOpen] = useState(true);
  const [lspSymbols, setLspSymbols]   = useState<DocumentSymbol[]>([]);
  // "waiting" = 等待 client 就绪, "loading" = 正在请求, "done" = 完成, "unsupported" = 不支持
  const [lspState, setLspState]       = useState<"waiting" | "loading" | "done" | "unsupported">("waiting");
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (retryRef.current) clearTimeout(retryRef.current);
    setLspSymbols([]);
    setLspState("waiting");

    if (!filePath || !rootPath || !langId || langId === "plaintext") {
      setLspState("unsupported");
      return;
    }

    let cancelled = false;

    const tryFetch = async (attempt: number) => {
      if (cancelled) return;

      // 先看 Editor 是否已经建好了 client
      let client = getActiveLspClient(langId, rootPath);
      console.log(`[LSP大纲] attempt=${attempt} lspPort=${lspPort} monaco=${!!monaco} activeClient=${!!client}`);

      // 没有就用 lspPort + monaco 自己建（等 initialize 完成后才能查询）
      if (!client && lspPort && monaco) {
        console.log(`[LSP大纲] 创建新 client lang=${langId} rootPath=${rootPath}`);
        client = getLspClient(lspPort, langId, rootPath, monaco);
        const uri = filePath.startsWith("file://") ? filePath : `file://${filePath}`;
        client.openDocument(uri, langId, content ?? "");
      }

      if (!client) {
        console.log(`[LSP大纲] 无 client，重试 attempt=${attempt}`);
        if (attempt < 40) retryRef.current = setTimeout(() => tryFetch(attempt + 1), 500);
        else { console.warn("[LSP大纲] 放弃：无 client"); setLspState("unsupported"); }
        return;
      }

      // client 存在但可能还在 initialize，等它 ready
      if (!client.isReady()) {
        console.log(`[LSP大纲] client 未 ready，重试 attempt=${attempt}`);
        if (attempt < 40) retryRef.current = setTimeout(() => tryFetch(attempt + 1), 500);
        else { console.warn("[LSP大纲] 放弃：client 未 ready"); setLspState("unsupported"); }
        return;
      }

      if (cancelled) return;
      setLspState("loading");
      const uri = filePath.startsWith("file://") ? filePath : `file://${filePath}`;

      // 订阅后续更新
      const prev = client.onSymbolsUpdated;
      client.onSymbolsUpdated = (u, syms) => {
        if (u === uri && !cancelled) setLspSymbols(syms);
        prev?.(u, syms);
      };

      const cached = client.getCachedSymbols(uri);
      if (cached.length > 0 && !cancelled) setLspSymbols(cached);

      const fresh = await client.fetchDocumentSymbols(uri).catch(() => [] as DocumentSymbol[]);
      if (!cancelled) { setLspSymbols(fresh); setLspState("done"); }
    };

    tryFetch(0);
    return () => {
      cancelled = true;
      if (retryRef.current) clearTimeout(retryRef.current);
    };
  }, [filePath, langId, rootPath, lspPort, monaco]);

  const htmlTree   = useMemo(() => cat === "html" && content ? parseHtmlWithLines(content) : null, [cat, content]);
  const cssRules   = useMemo(() => cat === "css" && content ? parseCss(content) : [], [cat, content]);
  const symbols    = useMemo(() => {
    if (!content || cat === "html" || cat === "css" || cat === "vue" || cat === "markdown" || cat === "json" || cat === "other") return [];
    return getSymbols(content, cat);
  }, [cat, content]);
  const goCallBlocks = useMemo(() => (
    cat === "go" && content ? parseGoCallBlocks(content) : []
  ), [cat, content]);
  const vueSfc     = useMemo(() => cat === "vue" && content ? parseVue(content) : null, [cat, content]);
  const mdHtml     = useMemo(() => cat === "markdown" && content ? renderMarkdown(content) : null, [cat, content]);
  const makeTargets = useMemo(() => cat === "makefile" && content ? parseMakefile(content) : [], [cat, content]);

  const [previewData, setPreviewData] = useState<string | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);

  useEffect(() => {
    const binaryCats = new Set<FileCat>(["pdf", "word", "excel", "pptx"]);
    if (!filePath || !binaryCats.has(cat)) {
      setPreviewData(null); setPreviewError(null); return;
    }
    let cancelled = false;
    setPreviewLoading(true); setPreviewData(null); setPreviewError(null);
    ReadFileAsBase64(filePath).then(async (b64: string) => {
      if (cancelled) return;
      if (cat === "pdf") {
        setPreviewData(b64);
      } else if (cat === "word") {
        const buf = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)).buffer;
        const result = await mammoth.convertToHtml({ arrayBuffer: buf });
        if (!cancelled) setPreviewData(result.value);
      } else if (cat === "pptx") {
        const buf = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)).buffer;
        const zip = await JSZip.loadAsync(buf);
        const slideFiles = Object.keys(zip.files)
          .filter((f) => /^ppt\/slides\/slide\d+\.xml$/.test(f))
          .sort((a, b) => {
            const na = parseInt(a.match(/\d+/)?.[0] ?? "0");
            const nb = parseInt(b.match(/\d+/)?.[0] ?? "0");
            return na - nb;
          });
        const slides = await Promise.all(slideFiles.map(async (f, idx) => {
          const xml = await zip.files[f].async("text");
          const texts = [...xml.matchAll(/<a:t[^>]*>([^<]+)<\/a:t>/g)]
            .map((m) => m[1].trim()).filter(Boolean);
          return `<div class="pptx-slide"><div class="pptx-slide-num">${t("analysis.slidePage", { n: idx + 1 })}</div><div class="pptx-slide-body">${texts.map((t) => `<p>${t}</p>`).join("") || `<span class="pptx-empty">${t("analysis.noSlideText")}</span>`}</div></div>`;
        }));
        if (!cancelled) setPreviewData(slides.join("") || `<div class="pptx-empty-all">${t("analysis.noTextContent")}</div>`);
      } else if (cat === "excel") {
        if (ext === "csv") {
          const text = atob(b64);
          const wb = XLSX.read(text, { type: "string" });
          const ws = wb.Sheets[wb.SheetNames[0]];
          setPreviewData(XLSX.utils.sheet_to_html(ws));
        } else {
          const wb = XLSX.read(b64, { type: "base64" });
          const sheetHtmls = wb.SheetNames.map((name) => {
            const ws = wb.Sheets[name];
            return `<h4 class="xlsx-sheet-title">${name}</h4>${XLSX.utils.sheet_to_html(ws)}`;
          });
          if (!cancelled) setPreviewData(sheetHtmls.join("<hr>"));
        }
      }
    }).catch((e: unknown) => {
      if (!cancelled) setPreviewError(String(e));
    }).finally(() => {
      if (!cancelled) setPreviewLoading(false);
    });
    return () => { cancelled = true; };
  }, [filePath, cat]);

  if (!filePath || (!content && cat !== "pdf" && cat !== "word" && cat !== "excel" && cat !== "pptx")) {
    return (
      <div className="analysis-empty">
        <ScanSearch size={32} strokeWidth={1} />
        <span>{t("analysis.selectFile")}</span>
      </div>
    );
  }

  const catLabels: Record<FileCat, string> = {
    html: t("analysis.html"), css: t("analysis.css"), ts: t("analysis.ts"), js: t("analysis.js"),
    go: t("analysis.go"), python: t("analysis.python"), java: t("analysis.java"), rust: t("analysis.rust"),
    cpp: t("analysis.cpp"), php: t("analysis.php"), ruby: t("analysis.ruby"),
    vue: t("analysis.vue"), markdown: t("analysis.markdown"), json: t("analysis.json"), makefile: t("analysis.makefile"),
    pdf: t("analysis.pdf"), word: t("analysis.word"), excel: t("analysis.excel"), pptx: t("analysis.pptx"), other: t("analysis.textFile"),
  };

  const FileIcon = cat === "html" ? FileText : cat === "css" ? FileText : ["ts","js"].includes(cat) ? FileCode : File;

  return (
    <div className="analysis-panel">
      <div className="analysis-file-header">
        <FileIcon size={14} className="analysis-file-icon" />
        <span className="analysis-file-name">{fileName}</span>
        <span className="analysis-file-badge">{catLabels[cat]}</span>
      </div>
      {cat !== "pdf" && cat !== "word" && cat !== "excel" && (
        <div className="analysis-file-meta">{lineCount} {t("analysis.lines")}</div>
      )}

      <div className="analysis-body">
        {/* ── HTML ── */}
        {cat === "html" && (
          htmlTree ? (
            <div className="ana-section">
              <div className="ana-section-header active">
                <Globe size={11} />
                <span>{t("analysis.htmlStructure")}</span>
              </div>
              <div className="ana-section-body">
                <HtmlNodeView node={htmlTree} depth={0} onGoToLine={onGoToLine} />
              </div>
            </div>
          ) : <div className="analysis-empty-inline">{t("analysis.parseFail")}</div>
        )}

        {/* ── CSS ── */}
        {cat === "css" && (
          cssRules.length > 0 ? (
            <>
              <CssGroup label={t("analysis.cssElement")} rules={cssRules.filter(r => r.type === "element")} icon={<FileText size={11} />} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssClass")} rules={cssRules.filter(r => r.type === "class")} icon={<span className="ana-dot">.</span>} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssId")} rules={cssRules.filter(r => r.type === "id")} icon={<Hash size={11} />} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssPseudo")} rules={cssRules.filter(r => r.type === "pseudo")} icon={<span className="ana-dot">:</span>} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssMedia")} rules={cssRules.filter(r => r.type === "media")} icon={<span className="ana-dot">@</span>} onGoToLine={onGoToLine} />
              <CssGroup label={t("analysis.cssKeyframes")} rules={cssRules.filter(r => r.type === "keyframes" || r.type === "other")} icon={<span className="ana-dot">%</span>} onGoToLine={onGoToLine} />
            </>
          ) : <div className="analysis-empty-inline">{t("analysis.noCssRules")}</div>
        )}

        {/* ── JSON ── */}
        {cat === "json" && (() => {
          if (!content) return <div className="analysis-empty-inline">{t("analysis.emptyFile")}</div>;
          try {
            const parsed = JSON.parse(content) as JsonVal;
            return (
              <div className="ana-section">
                <div className="ana-section-header active">
                  <Braces size={11} />
                  <span>{t("analysis.jsonTree")}</span>
                </div>
                <div className="ana-section-body">
                  <JsonNodeView label="root" value={parsed} depth={0} lineMap={buildJsonLineMap(content!)} onGoToLine={onGoToLine} />
                </div>
              </div>
            );
          } catch (e) {
            return <div className="analysis-empty-inline"><AlertTriangle size={14} /><span>{t("analysis.jsonParseFail")}: {String(e)}</span></div>;
          }
        })()}

        {/* ── PPT / PPTX ── */}
        {cat === "pptx" && (
          previewLoading ? (
            <div className="binary-preview-loading"><Loader2 size={20} className="spin" /><span>{t("analysis.parsingSlides")}</span></div>
          ) : previewError ? (
            <div className="binary-preview-error"><AlertTriangle size={16} /><span>{previewError}</span></div>
          ) : previewData ? (
            <div className="binary-pptx-preview" dangerouslySetInnerHTML={{ __html: previewData }} />
          ) : null
        )}

        {/* ── PDF ── */}
        {cat === "pdf" && (
          previewLoading ? (
            <div className="binary-preview-loading"><Loader2 size={20} className="spin" /><span>{t("analysis.loading")}</span></div>
          ) : previewError ? (
            <div className="binary-preview-error"><AlertTriangle size={16} /><span>{previewError}</span></div>
          ) : previewData ? (
            <embed
              src={`data:application/pdf;base64,${previewData}`}
              type="application/pdf"
              className="binary-pdf-embed"
            />
          ) : null
        )}

        {/* ── Word ── */}
        {cat === "word" && (
          previewLoading ? (
            <div className="binary-preview-loading"><Loader2 size={20} className="spin" /><span>{t("analysis.converting")}</span></div>
          ) : previewError ? (
            <div className="binary-preview-error"><AlertTriangle size={16} /><span>{previewError}</span></div>
          ) : previewData ? (
            <div className="binary-doc-preview" dangerouslySetInnerHTML={{ __html: previewData }} />
          ) : null
        )}

        {/* ── Excel / CSV ── */}
        {cat === "excel" && (
          previewLoading ? (
            <div className="binary-preview-loading"><Loader2 size={20} className="spin" /><span>{t("analysis.parsing")}</span></div>
          ) : previewError ? (
            <div className="binary-preview-error"><AlertTriangle size={16} /><span>{previewError}</span></div>
          ) : previewData ? (
            <div className="binary-excel-preview" dangerouslySetInnerHTML={{ __html: previewData }} />
          ) : null
        )}

        {/* ── Code ── */}
        {!["html","css","vue","markdown","other","pdf","word","excel"].includes(cat) && (
          symbols.length > 0 ? (
            <div className="ana-section">
              <div className="ana-section-header active">
                <FileCode size={11} />
                <span>{t("analysis.symbols")}</span>
                <span className="ana-count">{symbols.length}</span>
              </div>
              <div className="ana-section-body">
                {symbols.map((s, i) => <SymbolView key={i} sym={s} onGoToLine={onGoToLine} />)}
              </div>
            </div>
          ) : goCallBlocks.length === 0 ? (
            <div className="analysis-empty-inline">{t("analysis.noSymbols")}</div>
          ) : null
        )}

        {/* ── Go struct-literal call blocks ── */}
        {cat === "go" && goCallBlocks.length > 0 && (
          <div className="ana-section">
            <div className="ana-section-header active">
              <Braces size={11} />
              <span>{t("analysis.structCalls")}</span>
              <span className="ana-count">{goCallBlocks.length}</span>
            </div>
            <div className="ana-section-body">
              {goCallBlocks.map((b, i) => <GoCallBlockView key={i} block={b} onGoToLine={onGoToLine} />)}
            </div>
          </div>
        )}

        {/* ── Vue SFC ── */}
        {cat === "vue" && vueSfc && (
          <VueSfcPanel sfc={vueSfc} onGoToLine={onGoToLine} />
        )}

        {/* ── Markdown preview ── */}
        {cat === "markdown" && mdHtml && (
          <div
            className="md-preview"
            dangerouslySetInnerHTML={{ __html: mdHtml }}
          />
        )}

        {/* ── Makefile ── */}
        {cat === "makefile" && (
          makeTargets.length > 0 ? (
            <div className="ana-section">
              <div className="ana-section-header active">
                <ListTree size={11} />
                <span>Targets</span>
                <span className="ana-count">{makeTargets.length}</span>
              </div>
              <div className="ana-section-body">
                {makeTargets.map((t, i) => {
                  const [open, setOpen] = useState(true);
                  const hasDeps = t.deps.length > 0;
                  const hasRecipe = t.recipe.length > 0;
                  return (
                    <div key={i} className="ana-tree-node" style={{ paddingLeft: 0 }}>
                      <div
                        className="ana-tree-row clickable"
                        onClick={() => { if (hasDeps || hasRecipe) setOpen(!open); if (t.line > 0) onGoToLine?.(t.line); }}
                        title={t.line > 0 ? `Line ${t.line}` : undefined}
                      >
                        <span className="ana-tree-arrow">
                          {(hasDeps || hasRecipe) ? (open ? <ChevronDown size={10} /> : <ChevronRight size={10} />) : <span style={{ width: 10 }} />}
                        </span>
                        <Zap size={12} className="ana-kind-icon function" />
                        <span className="ana-sym-name public">{t.name}</span>
                        {t.isPhony && <span className="ana-badge kind">phony</span>}
                        {hasDeps && <span className="ana-count">{t.deps.length}</span>}
                        <span className="ana-line-num">{t.line}</span>
                      </div>
                      {open && (hasDeps || hasRecipe) && (
                        <div style={{ paddingLeft: 12 }}>
                          {hasDeps && (
                            <div className="ana-tree-node">
                              <div className="ana-tree-row">
                                <span className="ana-tree-arrow"><span style={{ width: 10 }} /></span>
                                <span className="ana-badge kind" style={{ marginRight: 4 }}>deps</span>
                                {t.deps.map((d, di) => (
                                  <span key={di} className="ana-tag-attrs" style={{ marginRight: 4 }}>{d}</span>
                                ))}
                              </div>
                            </div>
                          )}
                          {hasRecipe && t.recipe.map((r, ri) => (
                            <div key={ri} className="ana-tree-node">
                              <div className="ana-tree-row" style={{ opacity: 0.7 }}>
                                <span className="ana-tree-arrow"><span style={{ width: 10 }} /></span>
                                <span className="ana-badge kind" style={{ marginRight: 4 }}>→</span>
                                <span style={{ fontFamily: "monospace", fontSize: 11 }}>{r}</span>
                              </div>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          ) : <div className="analysis-empty-inline">{t("analysis.noTargets")}</div>
        )}

        {cat === "other" && (
          <div className="analysis-empty-inline">
            <File size={24} strokeWidth={1} />
            <span>{t("analysis.binaryText")}</span>
          </div>
        )}

        {/* ── LSP 大纲 ── */}
        {langId && langId !== "plaintext" && (
          <div className="ana-section">
            <div
              className={`ana-section-header${lspOpen ? " active" : ""}`}
              onClick={() => setLspOpen(!lspOpen)}
            >
              {lspOpen ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
              <ListTree size={11} />
              <span>{t("analysis.lspOutline")}</span>
              {(lspState === "waiting" || lspState === "loading") ? (
                <Loader2 size={11} className="spin" />
              ) : (
                <span className="ana-count">{lspSymbols.length}</span>
              )}
            </div>
            {lspOpen && (
              <div className="ana-section-body">
                {(lspState === "waiting" || lspState === "loading") && (
                  <div className="analysis-empty-inline">
                    <Loader2 size={14} className="spin" />
                    <span>{t("analysis.waitingLsp")}</span>
                  </div>
                )}
                {lspState === "unsupported" && (
                  <div className="analysis-empty-inline">{t("analysis.lspNotSupported")}</div>
                )}
                {lspState === "done" && lspSymbols.length === 0 && (
                  <div className="analysis-empty-inline">{t("analysis.noLspSymbols")}</div>
                )}
                {lspState === "done" && lspSymbols.length > 0 && lspSymbols.map((s, i) => (
                  <LspSymbolNode key={i} sym={s} onGoToLine={onGoToLine} />
                ))}
              </div>
            )}
          </div>
        )}

        {/* ── 调用层次 ── */}
        {langId && langId !== "plaintext" && (
          <div className="ana-section">
            <div
              className={`ana-section-header${callHierOpen ? " active" : ""}`}
              onClick={() => setCallHierOpen(!callHierOpen)}
            >
              {callHierOpen ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
              <Network size={11} />
              <span>{t("analysis.callHierarchy")}</span>
            </div>
            {callHierOpen && (
              <div className="ana-section-body" style={{ padding: 0 }}>
                <CallHierarchyPanel
                  activeFile={filePath}
                  rootPath={rootPath ?? null}
                  langId={langId}
                  cursorLine={cursorLine}
                  cursorCol={cursorCol}
                  onOpenFile={onOpenFile ?? (() => {})}
                  lspPort={lspPort}
                  monaco={monaco}
                />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
