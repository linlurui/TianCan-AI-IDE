import React, { useEffect, useRef } from "react";
import { useTranslation } from "../i18n";
import { Code2 } from "lucide-react";
import MonacoEditor, { OnMount } from "@monaco-editor/react";
import type * as Monaco from "monaco-editor";
import { getLspClient, disposeLspClient } from "../lsp/client";
import LspInstallBanner, { useLspCheck } from "./LspInstallBanner";

interface Tab {
  path: string;
  name: string;
  content: string;
  isDirty: boolean;
}

export interface Breakpoint {
  file: string;
  line: number;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
  logMessage?: string;
}

interface EditorProps {
  tabs: Tab[];
  activeTab: string | null;
  onTabSelect: (path: string) => void;
  onTabClose: (path: string) => void;
  onChange: (path: string, value: string) => void;
  onSave: (path: string) => void;
  onCursorChange?: (line: number, col: number) => void;
  lspPort?: number;
  rootPath?: string;
  onMonacoMount?: (monaco: any) => void;
  onEditorMount?: (editor: any) => void;
  renderSpecial?: (path: string) => React.ReactNode | null;
  breakpoints?: Breakpoint[];
  onBreakpointToggle?: (file: string, line: number) => void;
  pausedAt?: { file: string; line: number } | null;
  onEvaluateExpr?: (expr: string) => Promise<string | null>;
}

const LANG_MAP: Record<string, string> = {
  ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
  go: "go", py: "python", rs: "rust", java: "java", cpp: "cpp", c: "c",
  cs: "csharp", rb: "ruby", php: "php", swift: "swift", kt: "kotlin",
  json: "json", yaml: "yaml", yml: "yaml", toml: "toml", xml: "xml",
  html: "html", css: "css", scss: "scss", less: "less",
  vue: "html",
  md: "markdown", sh: "shell", bash: "shell", zsh: "shell",
  sql: "sql", graphql: "graphql", dockerfile: "dockerfile",
};

function extOf(path: string): string {
  const parts = path.split(".");
  return parts.length > 1 ? parts[parts.length - 1].toLowerCase() : "";
}

function langOf(path: string): string {
  const base = path.split("/").pop()?.toLowerCase() ?? "";
  if (base === "dockerfile") return "dockerfile";
  if (base === "makefile") return "makefile";
  return LANG_MAP[extOf(path)] ?? "plaintext";
}

export default function Editor({
  tabs, activeTab, onTabSelect, onTabClose, onChange, onSave, onCursorChange,
  lspPort, rootPath, onMonacoMount, onEditorMount, renderSpecial,
  breakpoints = [], onBreakpointToggle, pausedAt, onEvaluateExpr,
}: EditorProps) {
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const activeTabData = tabs.find((t) => t.path === activeTab);
  const openedUrisRef = useRef<Set<string>>(new Set());
  const bpDecorRef = useRef<Monaco.editor.IEditorDecorationsCollection | null>(null);
  const execDecorRef = useRef<Monaco.editor.IEditorDecorationsCollection | null>(null);
  const activeTabRef = useRef<string | null>(activeTab);
  const onEvaluateExprRef = useRef(onEvaluateExpr);
  useEffect(() => { activeTabRef.current = activeTab; }, [activeTab]);
  useEffect(() => { onEvaluateExprRef.current = onEvaluateExpr; }, [onEvaluateExpr]);

  const activeLang = activeTab ? langOf(activeTab) : null;
  const lspInstallStatus = useLspCheck(activeLang, lspPort ?? 0);

  const handleMount: OnMount = (editor, monaco) => {
    editorRef.current = editor;
    monacoRef.current = monaco;
    onMonacoMount?.(monaco);
    onEditorMount?.(editor);

    monaco.editor.defineTheme("tiancan-dark", {
      base: "vs-dark",
      inherit: true,
      rules: [
        { token: "comment", foreground: "6A9955" },
        { token: "keyword", foreground: "569CD6" },
        { token: "string", foreground: "CE9178" },
        { token: "number", foreground: "B5CEA8" },
        { token: "type", foreground: "4EC9B0" },
        { token: "function", foreground: "DCDCAA" },
      ],
      colors: {
        "editor.background": "#1e1e1e",
        "editor.foreground": "#d4d4d4",
        "editor.lineHighlightBackground": "#2a2d2e",
        "editor.selectionBackground": "#264f78",
        "editorCursor.foreground": "#aeafad",
        "editorLineNumber.foreground": "#858585",
        "editorLineNumber.activeForeground": "#c6c6c6",
        "editor.findMatchBackground": "#515c6a",
        "editor.findMatchHighlightBackground": "#314365",
        "editorWidget.background": "#252526",
        "editorSuggestWidget.background": "#252526",
        "editorSuggestWidget.selectedBackground": "#094771",
      },
    });
    monaco.editor.setTheme("tiancan-dark");

    editor.addCommand(
      monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS,
      () => {
        if (activeTab) onSave(activeTab);
      }
    );

    editor.onDidChangeCursorPosition((e) => {
      if (onCursorChange) {
        onCursorChange(e.position.lineNumber, e.position.column);
      }
    });

    editor.onMouseDown((e) => {
      const target = e.target;
      if (
        target.type === (monaco.editor.MouseTargetType as any).GUTTER_GLYPH_MARGIN ||
        target.type === (monaco.editor.MouseTargetType as any).GUTTER_LINE_NUMBERS
      ) {
        const line = target.position?.lineNumber;
        const model = editor.getModel();
        if (line && activeTabRef.current) {
          onBreakpointToggle?.(activeTabRef.current, line);
        }
      }
    });

    bpDecorRef.current = editor.createDecorationsCollection([]);
    execDecorRef.current = editor.createDecorationsCollection([]);

    // Hover provider: evaluate expression via DAP during debug pause
    const hoverDisposable = monaco.languages.registerHoverProvider("*", {
      provideHover: async (model, position) => {
        const evaluate = onEvaluateExprRef.current;
        if (!evaluate) return null;
        const word = model.getWordAtPosition(position);
        if (!word) return null;
        const result = await evaluate(word.word).catch(() => null);
        if (!result) return null;
        return {
          range: new monaco.Range(
            position.lineNumber, word.startColumn,
            position.lineNumber, word.endColumn,
          ),
          contents: [
            { value: `**${word.word}**` },
            { value: "```\n" + result + "\n```" },
          ],
        };
      },
    });
    // Dispose hover provider when editor is destroyed
    editor.onDidDispose(() => hoverDisposable.dispose());
  };

  useEffect(() => {
    if (editorRef.current && activeTab) {
      const model = editorRef.current.getModel();
      if (model && activeTabData) {
        const current = model.getValue();
        if (current !== activeTabData.content) {
          model.setValue(activeTabData.content);
        }
      }
    }
  }, [activeTab]);

  // Sync execution-line decoration when paused at a line in the active file
  useEffect(() => {
    const coll = execDecorRef.current;
    const monaco = monacoRef.current;
    if (!coll || !monaco) return;
    if (pausedAt && pausedAt.file === activeTab) {
      coll.set([{
        range: new monaco.Range(pausedAt.line, 1, pausedAt.line, 1),
        options: {
          isWholeLine: true,
          className: "editor-debug-current-line",
          glyphMarginClassName: "editor-debug-current-glyph",
          stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
          zIndex: 10,
        },
      }]);
    } else {
      coll.set([]);
    }
  }, [pausedAt, activeTab]);

  // Sync breakpoint decorations with current file
  useEffect(() => {
    const editor = editorRef.current;
    const monaco = monacoRef.current;
    const coll = bpDecorRef.current;
    if (!editor || !monaco || !coll || !activeTab) return;
    const model = editor.getModel();
    if (!model) return;
    const filePath = model.uri.fsPath || model.uri.path;
    const fileBreakpoints = breakpoints.filter((bp) =>
      bp.file === filePath || bp.file === activeTab
    );
    coll.set(fileBreakpoints.map((bp) => ({
      range: new monaco.Range(bp.line, 1, bp.line, 1),
      options: {
        glyphMarginClassName: "editor-breakpoint-glyph",
        stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
      },
    })));
  }, [breakpoints, activeTab]);

  // LSP: open document when a tab is first activated
  useEffect(() => {
    if (!lspPort || !rootPath || !activeTab || !activeTabData) return;
    const monaco = monacoRef.current;
    if (!monaco) return;
    const uri = activeTab.startsWith("file://") ? activeTab : `file://${activeTab}`;
    const langId = langOf(activeTab);
    if (langId === "plaintext") return;
    const client = getLspClient(lspPort, langId, rootPath, monaco);
    if (!openedUrisRef.current.has(uri)) {
      openedUrisRef.current.add(uri);
      client.openDocument(uri, langId, activeTabData.content);
    }
  }, [activeTab, lspPort, rootPath]);

  const { t } = useTranslation();

  if (tabs.length === 0) {
    return (
      <div className="editor-area">
        <div className="editor-placeholder">
          <div className="editor-placeholder-logo"><Code2 size={56} strokeWidth={0.8} /></div>
          <h2>{t("app.title")}</h2>
          <p>
            {t("editor.openHint")}<br />
            {t("editor.aiHint")}
          </p>
          <p style={{ color: "var(--text-muted)", fontSize: "11px" }}>
            {t("editor.shortcuts")}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="editor-area">
      {activeLang && (lspInstallStatus === "missing" || lspInstallStatus === "installing") && (
        <LspInstallBanner
          lang={activeLang}
          onInstalled={() => {
            if (!lspPort || !rootPath || !activeTab || !activeTabData) return;
            const monaco = monacoRef.current;
            if (!monaco) return;
            const uri = activeTab.startsWith("file://") ? activeTab : `file://${activeTab}`;
            // Dispose old (failed) client so getLspClient creates a fresh connection
            disposeLspClient(activeLang, rootPath);
            openedUrisRef.current.delete(uri);
            const client = getLspClient(lspPort, activeLang, rootPath, monaco);
            openedUrisRef.current.add(uri);
            client.openDocument(uri, activeLang, activeTabData.content);
          }}
        />
      )}
      <div className="editor-tabs">
        {tabs.map((tab) => (
          <div
            key={tab.path}
            className={`editor-tab ${activeTab === tab.path ? "active" : ""}`}
            onClick={() => onTabSelect(tab.path)}
            title={tab.path}
          >
            <span>{tab.name}{tab.isDirty ? " ●" : ""}</span>
            <span
              className="editor-tab-close"
              onClick={(e) => { e.stopPropagation(); onTabClose(tab.path); }}
            >
              ×
            </span>
          </div>
        ))}
      </div>

      {activeTabData && activeTab && activeTab.startsWith("__") && renderSpecial?.(activeTab) ? (
        <div className="editor-special-panel">
          {renderSpecial(activeTab)}
        </div>
      ) : activeTabData && (
        <div className="editor-monaco">
          <MonacoEditor
            height="100%"
            language={langOf(activeTabData.path)}
            value={activeTabData.content}
            onMount={handleMount}
            onChange={(val) => {
              if (activeTab) {
                onChange(activeTab, val ?? "");
                // Notify LSP of content change
                if (lspPort && rootPath) {
                  const monaco = monacoRef.current;
                  if (monaco) {
                    const langId = langOf(activeTab);
                    if (langId !== "plaintext") {
                      const uri = activeTab.startsWith("file://") ? activeTab : `file://${activeTab}`;
                      const client = getLspClient(lspPort, langId, rootPath, monaco);
                      client.changeDocument(uri, val ?? "");
                    }
                  }
                }
              }
            }}
            options={{
              fontSize: 14,
              fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", monospace',
              fontLigatures: true,
              lineHeight: 22,
              minimap: { enabled: true, scale: 1 },
              scrollBeyondLastLine: false,
              wordWrap: "off",
              tabSize: 2,
              renderWhitespace: "selection",
              bracketPairColorization: { enabled: true },
              guides: { bracketPairs: true },
              smoothScrolling: true,
              cursorBlinking: "smooth",
              cursorSmoothCaretAnimation: "on",
              padding: { top: 12, bottom: 12 },
              glyphMargin: true,
            }}
          />
        </div>
      )}
    </div>
  );
}
