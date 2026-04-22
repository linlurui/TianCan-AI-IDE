import React, { useEffect, useState } from "react";
import { DiffEditor } from "@monaco-editor/react";
import { useTranslation } from "../i18n";
import { Loader2, GitCompare } from "lucide-react";
import { GetHeadFileContent } from "../bindings/git";
import { ReadFile } from "../bindings/filesystem";

interface GitDiffViewerProps {
  repoPath: string;
  relPath: string;
}

export default function GitDiffViewer({ repoPath, relPath }: GitDiffViewerProps) {
  const { t } = useTranslation();
  const [original, setOriginal] = useState<string | null>(null);
  const [modified, setModified] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const ext = relPath.split(".").pop()?.toLowerCase() ?? "";
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
    const absPath = repoPath + "/" + relPath;

    Promise.all([
      GetHeadFileContent(repoPath, relPath).catch(() => ""),
      ReadFile(absPath).catch(() => ""),
    ]).then(([head, working]) => {
      if (cancelled) return;
      setOriginal(head ?? "");
      setModified(working ?? "");
    }).catch((e) => {
      if (!cancelled) setError(String(e));
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });

    return () => { cancelled = true; };
  }, [repoPath, relPath]);

  if (loading) {
    return (
      <div className="git-diff-full-loading">
        <Loader2 size={20} className="spin" />
        <span>{t("git.loadingDiffCompare")}</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className="git-diff-full-error">
        <GitCompare size={20} />
        <span>{t("git.cannotLoadDiff")}: {error}</span>
      </div>
    );
  }

  return (
    <div className="git-diff-full-wrap">
      <div className="git-diff-full-header">
        <GitCompare size={13} className="git-diff-full-icon" />
        <span className="git-diff-full-path">{relPath}</span>
        <span className="git-diff-full-labels">
          <span className="git-diff-label git-diff-label-orig">HEAD</span>
          <span className="git-diff-sep">←→</span>
          <span className="git-diff-label git-diff-label-mod">{t("git.workingDir")}</span>
        </span>
      </div>
      <div className="git-diff-full-editor">
        <DiffEditor
          original={original ?? ""}
          modified={modified ?? ""}
          language={lang}
          theme="vs-dark"
          options={{
            readOnly: true,
            renderSideBySide: true,
            minimap: { enabled: false },
            fontSize: 13,
            lineNumbers: "on",
            scrollBeyondLastLine: false,
            diffWordWrap: "off",
          }}
        />
      </div>
    </div>
  );
}
