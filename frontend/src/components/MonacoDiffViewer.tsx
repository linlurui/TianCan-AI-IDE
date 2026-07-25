/**
 * MonacoDiffViewer - 使用 Monaco DiffEditor 显示代码修改前后对比
 *
 * 使用 monaco.editor.createDiffEditor API 创建真正的 diff 对比视图
 */
import React, { useEffect, useRef, useCallback } from "react";
import { ChevronUp, X, Check, XCircle } from "lucide-react";
import type * as Monaco from "monaco-editor";

interface MonacoDiffViewerProps {
  originalContent: string;
  modifiedContent: string;
  originalLabel?: string;
  modifiedLabel?: string;
  filePath?: string;
  language?: string;
  onApply?: () => void;
  onReject?: () => void;
  onClose?: () => void;
  readOnly?: boolean;
  height?: number | string;
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
  const parts = path?.split(".") || [];
  return parts.length > 1 ? parts[parts.length - 1].toLowerCase() : "";
}

function langOf(path: string): string {
  const base = path?.split("/").pop()?.toLowerCase() ?? "";
  if (base === "dockerfile") return "dockerfile";
  if (base === "makefile") return "makefile";
  return LANG_MAP[extOf(path)] ?? "plaintext";
}

// 动态导入 monaco
let monacoModule: typeof Monaco | null = null;
async function getMonaco(): Promise<typeof Monaco> {
  if (monacoModule) return monacoModule;
  monacoModule = await import("monaco-editor");
  return monacoModule;
}

export default function MonacoDiffViewer({
  originalContent,
  modifiedContent,
  originalLabel = "修改前 (Original)",
  modifiedLabel = "修改后 (Modified)",
  filePath,
  language,
  onApply,
  onReject,
  onClose,
  readOnly = true,
  height = 300,
}: MonacoDiffViewerProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneDiffEditor | null>(null);
  const originalModelRef = useRef<Monaco.editor.ITextModel | null>(null);
  const modifiedModelRef = useRef<Monaco.editor.ITextModel | null>(null);
  const initializedRef = useRef(false);

  const fileLanguage = language || langOf(filePath || "");

  // 计算行数差异
  const lineDiff = modifiedContent.split("\n").length - originalContent.split("\n").length;
  const diffLabel = lineDiff > 0 ? `+${lineDiff}` : lineDiff.toString();

  // 初始化 DiffEditor
  const initEditor = useCallback(async () => {
    if (!containerRef.current || initializedRef.current) return;

    try {
      const monaco = await getMonaco();

      // 定义主题（如果还没有）
      const currentTheme = (monaco.editor as any).getTheme?.() || "";
      if (currentTheme !== "tiancan-dark") {
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
            { token: "variable", foreground: "9CDCFE" },
          ],
          colors: {
            "editor.background": "#1e1e1e",
            "editor.foreground": "#d4d4d4",
            "editor.lineHighlightBackground": "#2d2d2d",
            "editorCursor.foreground": "#aeafad",
            "editor.selectionBackground": "#264f78",
            "editorLineNumber.foreground": "#858585",
            "diffEditor.insertedTextBackground": "#2ea04326",
            "diffEditor.removedTextBackground": "#f8514926",
            "diffEditor.insertedLineBackground": "#2ea0431a",
            "diffEditor.removedLineBackground": "#f851491a",
          },
        });
        monaco.editor.setTheme("tiancan-dark");
      }

      // 创建 Models
      originalModelRef.current = monaco.editor.createModel(
        originalContent || "",
        fileLanguage
      );
      modifiedModelRef.current = monaco.editor.createModel(
        modifiedContent || "",
        fileLanguage
      );

      // 创建 DiffEditor
      const diffEditor = monaco.editor.createDiffEditor(containerRef.current, {
        readOnly: true,
        renderSideBySide: true,
        automaticLayout: true,
        scrollBeyondLastLine: false,
        minimap: { enabled: false },
        lineNumbers: "on",
        glyphMargin: false,
        folding: true,
        renderWhitespace: "selection",
        wordWrap: "on",
        fontSize: 12,
        fontFamily: "'Fira Code', 'Cascadia Code', Consolas, monospace",
        scrollbar: {
          verticalScrollbarSize: 8,
          horizontalScrollbarSize: 8,
        },
        diffWordWrap: "on",
        ignoreTrimWhitespace: false,
        renderOverviewRuler: true,
        originalEditable: false,
      });

      diffEditor.setModel({
        original: originalModelRef.current,
        modified: modifiedModelRef.current,
      });

      editorRef.current = diffEditor;
      initializedRef.current = true;
    } catch (error) {
      console.error("Failed to initialize Monaco DiffEditor:", error);
    }
  }, [originalContent, modifiedContent, fileLanguage]);

  // 初始化
  useEffect(() => {
    initEditor();
    return () => {
      // 清理
      if (originalModelRef.current) {
        originalModelRef.current.dispose();
      }
      if (modifiedModelRef.current) {
        modifiedModelRef.current.dispose();
      }
      if (editorRef.current) {
        editorRef.current.dispose();
      }
      initializedRef.current = false;
    };
  }, [initEditor]);

  // 更新内容
  useEffect(() => {
    if (!editorRef.current) return;

    const updateContent = async () => {
      const monaco = await getMonaco();
      const editor = editorRef.current;
      if (!editor) return;

      // 重新创建 Models
      if (originalModelRef.current) {
        originalModelRef.current.dispose();
      }
      if (modifiedModelRef.current) {
        modifiedModelRef.current.dispose();
      }

      originalModelRef.current = monaco.editor.createModel(
        originalContent || "",
        fileLanguage
      );
      modifiedModelRef.current = monaco.editor.createModel(
        modifiedContent || "",
        fileLanguage
      );

      editor.setModel({
        original: originalModelRef.current,
        modified: modifiedModelRef.current,
      });
    };

    updateContent();
  }, [originalContent, modifiedContent, fileLanguage]);

  const containerHeight = typeof height === "number" ? `${height}px` : height;

  return (
    <div className="diff-viewer" style={styles.container}>
      {/* Header */}
      <div style={styles.header}>
        <div style={styles.headerLeft}>
          <span style={styles.title}>
            📄 {filePath?.split("/").pop() || "文件对比"}
          </span>
          {filePath && (
            <span style={styles.pathLabel}>{filePath}</span>
          )}
          <span style={{
            ...styles.diffBadge,
            backgroundColor: lineDiff > 0 ? "var(--status-success, #22c55e)" : "var(--status-error, #ef4444)",
          }}>
            {diffLabel}
          </span>
        </div>
        <div style={styles.headerRight}>
          {!readOnly && (
            <>
              {onApply && (
                <button
                  style={{ ...styles.actionButton, ...styles.applyButton }}
                  onClick={onApply}
                  title="应用修改"
                >
                  <Check size={14} />
                  <span>应用</span>
                </button>
              )}
              {onReject && (
                <button
                  style={{ ...styles.actionButton, ...styles.rejectButton }}
                  onClick={onReject}
                  title="拒绝修改"
                >
                  <XCircle size={14} />
                  <span>拒绝</span>
                </button>
              )}
            </>
          )}
          {onClose && (
            <button
              style={{ ...styles.iconButton, ...styles.closeButton }}
              onClick={onClose}
              title="关闭"
            >
              <X size={14} />
            </button>
          )}
        </div>
      </div>

      {/* Legend */}
      <div style={styles.legend}>
        <span style={styles.legendItem}>
          <span style={styles.legendColorOriginal}></span>
          {originalLabel}
        </span>
        <span style={styles.legendItem}>
          <span style={styles.legendColorModified}></span>
          {modifiedLabel}
        </span>
      </div>

      {/* Diff Editor Container */}
      <div
        ref={containerRef}
        style={{
          height: `calc(${containerHeight} - 70px)`, // 40px header + 28px legend + 2px border
          minHeight: 150,
          overflow: "hidden",
        }}
      />

      {/* Fallback for when Monaco hasn't loaded */}
      {!initializedRef.current && (
        <div style={styles.loading}>
          <span style={styles.loadingText}>加载 Diff 编辑器...</span>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: "flex",
    flexDirection: "column",
    backgroundColor: "var(--bg-primary, #1e1e1e)",
    borderRadius: 6,
    overflow: "hidden",
    border: "1px solid var(--border-color, #333)",
    height: "100%",
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "8px 12px",
    backgroundColor: "var(--bg-secondary, #252526)",
    borderBottom: "1px solid var(--border-color, #333)",
    height: 40,
    boxSizing: "border-box",
  },
  headerLeft: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    flex: 1,
    minWidth: 0,
    overflow: "hidden",
  },
  headerRight: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    flexShrink: 0,
  },
  iconButton: {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: 24,
    height: 24,
    background: "none",
    border: "none",
    borderRadius: 4,
    color: "var(--text-secondary, #9ca3af)",
    cursor: "pointer",
    padding: 0,
  },
  closeButton: {
    marginLeft: 4,
  },
  title: {
    fontSize: 13,
    fontWeight: 500,
    color: "var(--text-primary, #ccc)",
    whiteSpace: "nowrap",
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  pathLabel: {
    fontSize: 11,
    color: "var(--text-muted, #6b7280)",
    marginLeft: 8,
    whiteSpace: "nowrap",
    overflow: "hidden",
    textOverflow: "ellipsis",
    maxWidth: 200,
  },
  diffBadge: {
    fontSize: 10,
    padding: "2px 6px",
    borderRadius: 8,
    color: "#fff",
    marginLeft: 8,
    fontWeight: 600,
  },
  actionButton: {
    display: "flex",
    alignItems: "center",
    gap: 4,
    padding: "4px 8px",
    border: "none",
    borderRadius: 4,
    cursor: "pointer",
    fontSize: 12,
    fontWeight: 500,
  },
  applyButton: {
    backgroundColor: "var(--status-success, #22c55e)",
    color: "#fff",
  },
  rejectButton: {
    backgroundColor: "var(--status-error, #ef4444)",
    color: "#fff",
  },
  legend: {
    display: "flex",
    alignItems: "center",
    gap: 16,
    padding: "6px 12px",
    backgroundColor: "var(--bg-tertiary, #2d2d2d)",
    fontSize: 11,
    color: "var(--text-secondary, #9ca3af)",
    height: 28,
    boxSizing: "border-box",
  },
  legendItem: {
    display: "flex",
    alignItems: "center",
    gap: 6,
  },
  legendColorOriginal: {
    width: 12,
    height: 12,
    borderRadius: 2,
    backgroundColor: "rgba(248, 81, 73, 0.3)",
    border: "1px solid rgba(248, 81, 73, 0.5)",
  },
  legendColorModified: {
    width: 12,
    height: 12,
    borderRadius: 2,
    backgroundColor: "rgba(46, 160, 67, 0.3)",
    border: "1px solid rgba(46, 160, 67, 0.5)",
  },
  loading: {
    position: "absolute",
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    backgroundColor: "var(--bg-primary, #1e1e1e)",
  },
  loadingText: {
    color: "var(--text-muted, #6b7280)",
    fontSize: 12,
  },
};
