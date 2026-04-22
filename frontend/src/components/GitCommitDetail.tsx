import React, { useEffect, useState, useCallback } from "react";
import { useTranslation } from "../i18n";
import { GitCommit, RotateCcw, ChevronRight, ChevronDown, Loader2, X } from "lucide-react";
import { CommitInfo, CommitFileInfo, GetCommitFiles, GetCommitFileDiff, RestoreFileFromCommit } from "../bindings/git";

interface Props {
  repoPath: string;
  commit: CommitInfo;
  onRefresh?: () => void;
}

function statusColor(s: string): string {
  return s === "M" ? "var(--warning)" : s === "A" ? "var(--success)" : s === "D" ? "var(--error)" : "var(--text-muted)";
}

function DiffViewer({ diff }: { diff: string }) {
  return (
    <pre className="git-diff-viewer">
      {diff.split("\n").map((line, i) => {
        let cls = "diff-line";
        if (line.startsWith("+++") || line.startsWith("---")) cls += " diff-line-header";
        else if (line.startsWith("@@")) cls += " diff-line-hunk";
        else if (line.startsWith("+")) cls += " diff-line-add";
        else if (line.startsWith("-")) cls += " diff-line-del";
        return <div key={i} className={cls}>{line || " "}</div>;
      })}
    </pre>
  );
}

function CommitFileRow({ repoPath, hash, file, onRefresh }: {
  repoPath: string; hash: string; file: CommitFileInfo; onRefresh?: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [diff, setDiff] = useState<string | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [restoring, setRestoring] = useState(false);

  const loadDiff = useCallback(async () => {
    if (diff !== null) return;
    setDiffLoading(true);
    try {
      const d = await GetCommitFileDiff(repoPath, hash, file.path);
      setDiff(d ?? "");
    } catch { setDiff(""); }
    finally { setDiffLoading(false); }
  }, [repoPath, hash, file.path, diff]);

  const toggle = () => {
    if (!open) loadDiff();
    setOpen((v) => !v);
  };

  const handleRestore = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!window.confirm(t("git.confirmRestoreFile", { file: file.path, hash: hash.slice(0, 7) }))) return;
    setRestoring(true);
    try {
      await RestoreFileFromCommit(repoPath, hash, file.path);
      onRefresh?.();
    } catch (err) { alert(String(err)); }
    finally { setRestoring(false); }
  };

  return (
    <div className="git-commit-file-wrap">
      <div className={`git-commit-file-item${open ? " active" : ""}`} onClick={toggle}>
        <span className="ana-tree-arrow">
          {open ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        </span>
        <span className="git-commit-file-status" style={{ color: statusColor(file.status[0]) }}>
          {file.status[0]}
        </span>
        <span className="git-commit-file-name">{file.path}</span>
        <button
          className="git-log-revert-btn"
          disabled={restoring}
          title={t("git.restoreFileVersionTitle", { hash: hash.slice(0, 7), path: file.path })}
          onClick={handleRestore}
        >
          <RotateCcw size={10} />
          {restoring ? "…" : t("git.restore")}
        </button>
      </div>
      {open && (
        <div className="git-diff-container">
          {diffLoading
            ? <div className="git-diff-loading"><Loader2 size={12} className="spin" /> {t("git.loadingDiff")}</div>
            : diff ? <DiffViewer diff={diff} />
            : <div className="git-diff-loading">{t("git.noDiffContent")}</div>}
        </div>
      )}
    </div>
  );
}

export default function GitCommitDetail({ repoPath, commit, onRefresh }: Props) {
  const { t } = useTranslation();
  const [files, setFiles] = useState<CommitFileInfo[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    setFiles(null);
    setError(null);
    GetCommitFiles(repoPath, commit.hash)
      .then((list) => setFiles(list ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [repoPath, commit.hash]);

  const when = (() => {
    try { return new Date(commit.when).toLocaleString("zh-CN"); } catch { return commit.when; }
  })();

  return (
    <div className="git-commit-detail">
      <div className="git-commit-detail-header">
        <GitCommit size={14} className="git-commit-detail-icon" />
        <div className="git-commit-detail-meta">
          <span className="git-commit-detail-hash">{commit.hash}</span>
          <span className="git-commit-detail-author">{commit.author}</span>
          <span className="git-commit-detail-when">{when}</span>
        </div>
      </div>
      <div className="git-commit-detail-msg">{commit.message}</div>

      <div className="git-commit-detail-files">
        <div className="git-commit-detail-files-header">
          {t("git.changedFiles")}
          {files && <span className="ana-count">{files.length}</span>}
        </div>
        {loading && (
          <div className="git-diff-full-loading">
            <Loader2 size={16} className="spin" /><span>{t("git.loadingFileList")}</span>
          </div>
        )}
        {error && <div className="git-diff-full-error"><X size={14} />{error}</div>}
        {files && files.map((f) => (
          <CommitFileRow
            key={f.path}
            repoPath={repoPath}
            hash={commit.hash}
            file={f}
            onRefresh={onRefresh}
          />
        ))}
        {files && files.length === 0 && (
          <div className="git-diff-loading">{t("git.noFileChanges")}</div>
        )}
      </div>
    </div>
  );
}
