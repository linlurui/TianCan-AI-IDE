import React, { useState, useCallback, useRef, useEffect } from "react";
import { useTranslation } from "../i18n";
import { ChevronDown, ChevronRight, GitBranch, RefreshCw, Check, X, FilePlus, RotateCcw, MinusCircle, FolderOpen, History, Undo2, Trash2, FileCode } from "lucide-react";
import { RepoStatus, CommitInfo, CommitFileInfo, GetFileDiff, StageFile, UnstageFile, DiscardFile, GetLog, RevertCommit, GetCommitFiles, GetCommitFileDiff, RestoreFileFromCommit } from "../bindings/git";
import { DeleteFile } from "../bindings/filesystem";

interface GitPanelProps {
  status: RepoStatus | null;
  workingDir: string | null;
  onInitRepo: () => void;
  onStageAll: () => void;
  onCommit: (message: string) => void;
  onRefresh: () => void;
  onOpenFile?: (path: string) => void;
  onCommitClick?: (commit: CommitInfo) => void;
}

interface FileCtxMenu { x: number; y: number; filePath: string; staging: string; worktree: string; }

const STATUS_KEYS: Record<string, string> = {
  M: "git.modified", A: "git.added", D: "git.deleted", "?": "git.untracked", R: "git.renamed", C: "git.copied",
};

function statusColor(letter: string): string {
  const map: Record<string, string> = {
    M: "var(--warning)", A: "var(--success)", D: "var(--error)",
    "?": "var(--text-muted)",
  };
  return map[letter] ?? "var(--text-secondary)";
}

function DiffViewer({ diff }: { diff: string }) {
  const lines = diff.split("\n");
  return (
    <pre className="git-diff-viewer">
      {lines.map((line, i) => {
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

export default function GitPanel({
  status, workingDir, onInitRepo, onStageAll, onCommit, onRefresh, onOpenFile, onCommitClick,
}: GitPanelProps) {
  const [commitMsg, setCommitMsg] = useState("");
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [diffContent, setDiffContent] = useState<string | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [untrackedOpen, setUntrackedOpen] = useState(false);
  const [fileCtxMenu, setFileCtxMenu] = useState<FileCtxMenu | null>(null);
  const [logOpen, setLogOpen] = useState(false);
  const [commits, setCommits] = useState<CommitInfo[]>([]);
  const [logLoading, setLogLoading] = useState(false);
  const [revertingHash, setRevertingHash] = useState<string | null>(null);
  const ctxRef = useRef<HTMLDivElement>(null);
  const { t } = useTranslation();

  useEffect(() => {
    const close = (e: MouseEvent) => {
      if (ctxRef.current && !ctxRef.current.contains(e.target as Node)) setFileCtxMenu(null);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, []);

  const loadLog = useCallback(async () => {
    if (!workingDir) return;
    setLogLoading(true);
    try {
      const log = await GetLog(workingDir, 30);
      setCommits(log ?? []);
    } catch { setCommits([]); }
    finally { setLogLoading(false); }
  }, [workingDir]);

  useEffect(() => {
    if (logOpen && workingDir) loadLog();
  }, [logOpen, workingDir, loadLog]);

  const handleRevert = useCallback(async (hash: string, msg: string) => {
    if (!workingDir) return;
    if (!window.confirm(t("git.confirmRevert", { msg: msg.split("\n")[0] }))) return;
    setRevertingHash(hash);
    try {
      await RevertCommit(workingDir, hash);
      onRefresh();
      loadLog();
    } catch (err) { alert(String(err)); }
    finally { setRevertingHash(null); }
  }, [workingDir, onRefresh, loadLog]);

  const handleCtxMenu = useCallback((e: React.MouseEvent, fp: string, staging: string, worktree: string) => {
    e.preventDefault();
    e.stopPropagation();
    setFileCtxMenu({ x: e.clientX, y: e.clientY, filePath: fp, staging, worktree });
  }, []);

  const ctxAction = useCallback(async (action: "stage" | "unstage" | "discard" | "delete") => {
    if (!workingDir || !fileCtxMenu) return;
    const fp = fileCtxMenu.filePath;
    setFileCtxMenu(null);
    try {
      if (action === "stage")   await StageFile(workingDir, fp);
      if (action === "unstage") await UnstageFile(workingDir, fp);
      if (action === "discard") {
        if (!window.confirm(t("git.confirmDiscard", { file: fp }))) return;
        await DiscardFile(workingDir, fp);
      }
      if (action === "delete") {
        if (!window.confirm(t("git.confirmDelete", { file: fp }))) return;
        await DeleteFile(workingDir + "/" + fp);
      }
      onRefresh();
    } catch (err) { alert(String(err)); }
  }, [workingDir, fileCtxMenu, onRefresh]);

  const handleFileClick = useCallback(async (filePath: string) => {
    if (!workingDir) return;
    if (selectedFile === filePath) {
      setSelectedFile(null);
      setDiffContent(null);
      return;
    }
    setSelectedFile(filePath);
    setDiffLoading(true);
    setDiffContent(null);
    try {
      const diff = await GetFileDiff(workingDir, filePath);
      setDiffContent(diff);
    } catch (err) {
      setDiffContent(`// ${t("git.diffFail")}: ${err}`);
    } finally {
      setDiffLoading(false);
    }
  }, [workingDir, selectedFile]);

  if (!workingDir) {
    return (
      <div className="git-panel" style={{ color: "var(--text-muted)", fontSize: "12px", padding: "16px" }}>
        {t("git.noProject")}
      </div>
    );
  }

  if (!status?.isRepo) {
    return (
      <div className="git-panel">
        <div style={{ color: "var(--text-muted)", fontSize: "12px", marginBottom: "12px" }}>
          {t("git.notRepo")}
        </div>
        <button className="btn-primary" style={{ width: "100%" }} onClick={onInitRepo}>
          git init
        </button>
      </div>
    );
  }

  const changedFiles = status.files?.filter(
    (f) => !(f.worktree === "?" && f.staging === "?")
  ) ?? [];
  const untrackedFiles = status.files?.filter(
    (f) => f.worktree === "?" && f.staging === "?"
  ) ?? [];

  const handleCommit = () => {
    if (!commitMsg.trim()) return;
    onStageAll();
    onCommit(commitMsg.trim());
    setCommitMsg("");
  };

  return (
    <div className="git-panel">
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "8px" }}>
        <div style={{ fontSize: "12px", color: "var(--accent)", fontWeight: 600, display: "flex", alignItems: "center", gap: 4 }}>
          <GitBranch size={13} />
          {status.branch}
        </div>
        <button className="sidebar-header-btn sidebar-header-btn-always" onClick={onRefresh} title={t("git.refreshStatus")}>
          <RefreshCw size={12} />
        </button>
      </div>

      {changedFiles.length === 0 && untrackedFiles.length === 0 ? (
        <div style={{ fontSize: "12px", color: "var(--success)", padding: "4px 0", display: "flex", alignItems: "center", gap: 4 }}>
          <Check size={13} />
          {t("git.clean")}
        </div>
      ) : (
        <>
          {/* ── Changes ── */}
          {changedFiles.length > 0 && (
            <div style={{ marginBottom: "6px" }}>
              <div style={{ fontSize: "11px", color: "var(--text-muted)", marginBottom: "4px", textTransform: "uppercase", letterSpacing: "0.05em" }}>
                {t("git.changes")} ({changedFiles.length})
              </div>
              {changedFiles.map((f) => {
                const letter = f.worktree !== " " ? f.worktree : f.staging;
                const isSelected = selectedFile === f.path;
                return (
                  <React.Fragment key={f.path}>
                    <div
                      className={`git-status-item git-status-item-clickable${isSelected ? " git-status-item-active" : ""}`}
                      onClick={() => handleFileClick(f.path)}
                      onContextMenu={(e) => handleCtxMenu(e, f.path, f.staging, f.worktree)}
                      title={`${t(STATUS_KEYS[letter] ?? "git.untracked")}: ${f.path}`}
                    >
                      {isSelected ? <ChevronDown size={10} style={{ flexShrink: 0, color: "var(--text-muted)" }} /> : <ChevronRight size={10} style={{ flexShrink: 0, color: "var(--text-muted)" }} />}
                      <span style={{ color: statusColor(letter), fontWeight: 700, fontSize: "11px", minWidth: 12 }}>
                        {letter}
                      </span>
                      <span style={{ fontSize: "11px", fontFamily: "var(--font-mono)", flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        {f.path.split("/").pop()}
                      </span>
                    </div>
                    {isSelected && (
                      <div className="git-diff-container">
                        {diffLoading
                          ? <div className="git-diff-loading">{t("git.loadingDiff")}</div>
                          : diffContent
                            ? <DiffViewer diff={diffContent} />
                            : null}
                      </div>
                    )}
                  </React.Fragment>
                );
              })}
            </div>
          )}

          {/* ── Untracked (collapsible) ── */}
          {untrackedFiles.length > 0 && (
            <div style={{ marginBottom: "6px" }}>
              <div
                className="git-log-header"
                onClick={() => setUntrackedOpen((v) => !v)}
              >
                {untrackedOpen ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
                {t("git.untracked")} ({untrackedFiles.length})
              </div>
              {untrackedOpen && untrackedFiles.map((f) => {
                const isSelected = selectedFile === f.path;
                return (
                  <React.Fragment key={f.path}>
                    <div
                      className={`git-status-item git-status-item-clickable${isSelected ? " git-status-item-active" : ""}`}
                      onClick={() => handleFileClick(f.path)}
                      onContextMenu={(e) => handleCtxMenu(e, f.path, "?", "?")}
                      title={`${t("git.untracked")}: ${f.path}`}
                    >
                      {isSelected ? <ChevronDown size={10} style={{ flexShrink: 0, color: "var(--text-muted)" }} /> : <ChevronRight size={10} style={{ flexShrink: 0, color: "var(--text-muted)" }} />}
                      <span style={{ color: "var(--text-muted)", fontWeight: 700, fontSize: "11px", minWidth: 12 }}>U</span>
                      <span style={{ fontSize: "11px", fontFamily: "var(--font-mono)", flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        {f.path.split("/").pop()}
                      </span>
                    </div>
                    {isSelected && (
                      <div className="git-diff-container">
                        {diffLoading
                          ? <div className="git-diff-loading">{t("git.loadingDiff")}</div>
                          : diffContent
                            ? <DiffViewer diff={diffContent} />
                            : null}
                      </div>
                    )}
                  </React.Fragment>
                );
              })}
            </div>
          )}
        </>
      )}

      {changedFiles.length > 0 && (
        <div className="commit-area">
          <textarea
            className="commit-input"
            placeholder={t("git.commitPlaceholder")}
            value={commitMsg}
            onChange={(e) => setCommitMsg(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                handleCommit();
              }
            }}
            rows={2}
          />
          <button
            className="btn-primary"
            style={{ width: "100%" }}
            disabled={!commitMsg.trim()}
            onClick={handleCommit}
          >
            {t("git.stageAndCommit")}
          </button>
        </div>
      )}
      {fileCtxMenu && (
        <div
          ref={ctxRef}
          className="context-menu"
          style={{ top: fileCtxMenu.y, left: fileCtxMenu.x, position: "fixed", zIndex: 9999 }}
          onClick={(e) => e.stopPropagation()}
        >
          <div className="context-menu-label" style={{ maxWidth: 200, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {fileCtxMenu.filePath.split("/").pop()}
          </div>
          {onOpenFile && (
            <div className="context-menu-item" onClick={() => { onOpenFile(fileCtxMenu.filePath); setFileCtxMenu(null); }}>
              <FolderOpen size={11} style={{ marginRight: 6, opacity: 0.7 }} />
              {t("git.compareDiff")}
            </div>
          )}
          <div className="context-menu-divider" />
          <div className="context-menu-item" onClick={() => ctxAction("stage")} title={t("git.stageTitle")}>
            <FilePlus size={11} style={{ marginRight: 6, opacity: 0.7 }} />
            {t("git.stage")} <span style={{ opacity: 0.5, marginLeft: 4, fontSize: 10 }}>git add</span>
          </div>
          {fileCtxMenu.staging !== "?" && (
            <div className="context-menu-item" onClick={() => ctxAction("unstage")} title={t("git.unstageTitle")}>
              <MinusCircle size={11} style={{ marginRight: 6, opacity: 0.7 }} />
              {t("git.unstage")} <span style={{ opacity: 0.5, marginLeft: 4, fontSize: 10 }}>git restore --staged</span>
            </div>
          )}
          {fileCtxMenu.worktree !== "?" && (
            <>
              <div className="context-menu-divider" />
              <div className="context-menu-item context-menu-item-danger" onClick={() => ctxAction("discard")} title={t("git.discardTitle")}>
                <RotateCcw size={11} style={{ marginRight: 6, opacity: 0.7 }} />
                {t("git.discardFile")} <span style={{ opacity: 0.5, marginLeft: 4, fontSize: 10 }}>git checkout --</span>
              </div>
            </>
          )}
          {fileCtxMenu.staging === "?" && fileCtxMenu.worktree === "?" && (
            <>
              <div className="context-menu-divider" />
              <div className="context-menu-item context-menu-item-danger" onClick={() => ctxAction("delete")}>
                <X size={11} style={{ marginRight: 6, opacity: 0.7 }} />
                {t("git.deleteFile")}
              </div>
            </>
          )}
        </div>
      )}
      {/* ── Commit History ── */}
      {status?.isRepo && (
        <div className="git-log-section">
          <div
            className="git-log-header"
            onClick={() => setLogOpen((v) => !v)}
          >
            {logOpen ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
            <History size={11} />
            <span>{t("git.commitHistory")}</span>
            {commits.length > 0 && <span className="ana-count">{commits.length}</span>}
          </div>
          {logOpen && (
            <div className="git-log-list">
              {logLoading ? (
                <div className="git-log-empty">{t("git.loading")}</div>
              ) : commits.length === 0 ? (
                <div className="git-log-empty">{t("git.noCommits")}</div>
              ) : commits.map((c) => (
                <div
                  key={c.hash}
                  className="git-log-item"
                  onClick={() => onCommitClick?.(c)}
                  style={{ cursor: onCommitClick ? "pointer" : "default" }}
                >
                  <div className="git-log-item-top">
                    <span className="git-log-hash">{c.hash}</span>
                    <span className="git-log-author">{c.author}</span>
                    <button
                      className="git-log-revert-btn"
                      disabled={revertingHash === c.hash}
                      title={t("git.revertTitle", { hash: c.hash })}
                      onClick={(e) => { e.stopPropagation(); handleRevert(c.hash, c.message); }}
                    >
                      <Undo2 size={11} />
                      {revertingHash === c.hash ? "…" : "revert"}
                    </button>
                  </div>
                  <div className="git-log-msg">{c.message.split("\n")[0]}</div>
                  <div className="git-log-when">{new Date(c.when).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" })}</div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
