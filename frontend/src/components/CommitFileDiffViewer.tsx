import React, { useEffect, useState } from "react";
import { DiffEditor } from "@monaco-editor/react";
import { useTranslation } from "../i18n";
import { Loader2, RotateCcw } from "lucide-react";
import { GetCommitFileDiff, RestoreFileFromCommit } from "../bindings/git";

interface CommitFileDiffViewerProps {
  repoPath: string;
  commitHash: string;
  filePath: string;
}

export default function CommitFileDiffViewer({ repoPath, commitHash, filePath }: CommitFileDiffViewerProps) {
  const { t } = useTranslation();
  const [original, setOriginal] = useState<string>("");
  const [modified, setModified] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [restoring, setRestoring] = useState(false);

  const ext = filePath.split(".").pop()?.toLowerCase() ?? "";
  const LANG_MAP: Record<string, string> = {
    ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
    go: "go", py: "python", rs: "rust", java: "java", cpp: "cpp", c: "c",
    cs: "csharp", rb: "ruby", php: "php", kt: "kotlin",
    json: "json", yaml: "yaml", yml: "yaml", toml: "toml", xml: "xml",
    html: "html", css: "css", scss: "scss", less: "less",
    vue: "html", md: "markdown", sh: "shell", bash: "shell",
    sql: "sql",
  };
  const lang = LANG_MAP[ext] ?? "plaintext";

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);

    GetCommitFileDiff(repoPath, commitHash, filePath)
      .then((diff) => {
        if (cancelled) return;

        // Parse unified diff to extract original and modified content
        const lines = (diff ?? "").split("\n");
        const originalLines: string[] = [];
        const modifiedLines: string[] = [];
        let inHunk = false;

        for (const line of lines) {
          if (line.startsWith("@@")) {
            inHunk = true;
            continue;
          }
          if (line.startsWith("---") || line.startsWith("+++")) {
            continue;
          }
          if (!inHunk) continue;

          if (line.startsWith("-")) {
            originalLines.push(line.substring(1));
          } else if (line.startsWith("+")) {
            modifiedLines.push(line.substring(1));
          } else if (line.startsWith(" ")) {
            const content = line.substring(1);
            originalLines.push(content);
            modifiedLines.push(content);
          }
        }

        setOriginal(originalLines.join("\n"));
        setModified(modifiedLines.join("\n"));
      })
      .catch((e) => {
        if (!cancelled) setError(String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => { cancelled = true; };
  }, [repoPath, commitHash, filePath]);

  const handleRestore = async () => {
    if (!window.confirm(t("git.confirmRestore", { file: filePath, hash: commitHash.substring(0, 7) }))) {
      return;
    }
    setRestoring(true);
    try {
      await RestoreFileFromCommit(repoPath, commitHash, filePath);
      alert(t("git.fileRestored", { file: filePath }));
    } catch (err) {
      alert(t("git.restoreFail", { err: String(err) }));
    } finally {
      setRestoring(false);
    }
  };

  if (loading) {
    return (
      <div style={{ display: "flex", alignItems: "center", justifyContent: "center", height: "100%", gap: 8, color: "var(--text-muted)" }}>
        <Loader2 size={20} className="spin" />
        <span>{t("git.loadingDiffCompare")}</span>
      </div>
    );
  }

  if (error) {
    return (
      <div style={{ padding: 20, color: "var(--error)" }}>
        {t("git.loadFail")}: {error}
      </div>
    );
  }

  return (
    <div style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        padding: "8px 12px",
        background: "var(--bg-secondary)",
        borderBottom: "1px solid var(--border)",
        flexShrink: 0
      }}>
        <div style={{ fontSize: 12, color: "var(--text-secondary)" }}>
          <span style={{ color: "var(--text-primary)", fontWeight: 600 }}>{filePath.split("/").pop()}</span>
          <span style={{ marginLeft: 8 }}>{t("git.commit")}: {commitHash.substring(0, 7)}</span>
        </div>
        <button
          onClick={handleRestore}
          disabled={restoring}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 6,
            padding: "4px 12px",
            background: "var(--accent)",
            color: "white",
            border: "none",
            borderRadius: 4,
            cursor: restoring ? "not-allowed" : "pointer",
            fontSize: 12,
            opacity: restoring ? 0.6 : 1
          }}
          title={t("git.restoreToVersionTitle")}
        >
          <RotateCcw size={14} />
          {restoring ? t("git.restoring") : t("git.restoreToVersion")}
        </button>
      </div>
      <div style={{ flex: 1, minHeight: 0 }}>
        <DiffEditor
          height="100%"
          language={lang}
          original={original}
          modified={modified}
          theme="vs-dark"
          options={{
            readOnly: true,
            renderSideBySide: true,
            minimap: { enabled: false },
            fontSize: 13,
            lineNumbers: "on",
            scrollBeyondLastLine: false,
            wordWrap: "off",
            automaticLayout: true,
          }}
        />
      </div>
    </div>
  );
}
