import React, { useCallback, useEffect, useState } from "react";
import {
  ArrowLeft,
  FileCode2,
  FilePlus2,
  FileX2,
  PencilLine,
  Check,
  Loader2,
  Undo2,
} from "lucide-react";
import {
  SessionAnchorFiles,
  SessionAnchorFileDetail,
  SessionApplyAnchorHunk,
  type AnchorFileView,
  type AnchorFileDetail,
} from "../bindings/ctxsvc";

// 锚点审查面板：右键锚点 →「审查此锚点」后，AI Changes 面板切换到这里。
// 列出这一轮 AI 改过的文件；点文件看改前/改后的逐段对比；每一段可以单独
// 「应用此段」——把该段还原成改前内容（局部回退，不影响别的段）。
// 被后续轮次改过同一段落的，应用按钮灰掉并加删除线（服务端同样会拒绝）。
// 见 docs/anchor-design.md。

interface Props {
  projectRoot: string;
  sessionID: string;
  seq: number;
  summary: string;
  onClose: () => void;
}

const STATUS_META: Record<string, { label: string; icon: React.ReactNode }> = {
  A: { label: "新增", icon: <FilePlus2 size={12} /> },
  M: { label: "修改", icon: <PencilLine size={12} /> },
  D: { label: "删除", icon: <FileX2 size={12} /> },
};

function fmtCounts(f: { added: number; removed: number }): string {
  if (f.added === 0 && f.removed === 0) return "";
  return `+${f.added} −${f.removed}`;
}

export default function AnchorReview({ projectRoot, sessionID, seq, summary, onClose }: Props) {
  const [files, setFiles] = useState<AnchorFileView[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<AnchorFileDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [applying, setApplying] = useState<number | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const loadFiles = useCallback(async () => {
    if (!projectRoot) return;
    try {
      setFiles((await SessionAnchorFiles(projectRoot, sessionID, seq)) ?? []);
      setError(null);
    } catch (e: any) {
      setError(e?.message ?? String(e));
    }
  }, [projectRoot, sessionID, seq]);

  useEffect(() => {
    void loadFiles();
  }, [loadFiles]);

  const openFile = useCallback(
    async (path: string) => {
      if (!projectRoot) return;
      setSelected(path);
      setDetail(null);
      setNotice(null);
      setDetailLoading(true);
      try {
        setDetail(await SessionAnchorFileDetail(projectRoot, sessionID, seq, path));
      } catch (e: any) {
        setNotice(e?.message ?? String(e));
      } finally {
        setDetailLoading(false);
      }
    },
    [projectRoot, sessionID, seq]
  );

  const applyHunk = useCallback(
    async (path: string, hunk: number) => {
      if (!projectRoot) return;
      setApplying(hunk);
      setNotice(null);
      try {
        await SessionApplyAnchorHunk(projectRoot, sessionID, seq, path, hunk);
        setNotice(`已应用片段 #${hunk}：该段已还原为改前内容。`);
        // 刷新：应用状态 + 行数会变
        await openFile(path);
        await loadFiles();
      } catch (e: any) {
        setNotice(e?.message ?? String(e));
      } finally {
        setApplying(null);
      }
    },
    [projectRoot, sessionID, seq, openFile, loadFiles]
  );

  return (
    <div className="anchor-review">
      <div className="anchor-review-head">
        <button className="anchor-review-back" onClick={onClose} title="返回 AI Changes">
          <ArrowLeft size={14} />
        </button>
        <div className="anchor-review-title">
          <span className="anchor-review-seq">审查 #{seq}</span>
          <span className="anchor-review-summary" title={summary}>
            {summary || "本轮"}
          </span>
        </div>
        <span className="anchor-review-count">
          {files ? `${files.length} 个文件` : "…"}
        </span>
      </div>

      {error && <div className="anchor-review-error">{error}</div>}
      {notice && <div className="anchor-review-notice">{notice}</div>}

      <div className="anchor-review-body">
        <div className="anchor-review-files">
          {files && files.length === 0 && (
            <div className="anchor-review-empty">
              这一轮没有记录到文件改动。
              <br />
              <span>可能是本轮开工时应用尚未运行，或这一轮确实没有改文件。</span>
            </div>
          )}
          {(files ?? []).map((f) => {
            const meta = STATUS_META[f.status] ?? { label: f.status, icon: <FileCode2 size={12} /> };
            return (
              <button
                key={f.path}
                className={`anchor-review-file${selected === f.path ? " active" : ""}`}
                onClick={() => void openFile(f.path)}
              >
                <span className={`anchor-review-status s-${f.status}`}>
                  {meta.icon}
                  {meta.label}
                </span>
                <span className="anchor-review-path" title={f.path}>
                  {f.path}
                </span>
                <span className="anchor-review-diffcount">{fmtCounts(f)}</span>
              </button>
            );
          })}
        </div>

        <div className="anchor-review-detail">
          {!selected && <div className="anchor-review-hint">← 选择文件查看改前/改后对比</div>}
          {detailLoading && (
            <div className="anchor-review-hint">
              <Loader2 size={12} className="animate-spin" /> 加载中…
            </div>
          )}
          {detail && !detailLoading && (
            <div className="anchor-review-file-detail">
              <div className="anchor-review-file-head">
                <FileCode2 size={13} />
                <span className="anchor-review-detail-path">{detail.path}</span>
                <span className="anchor-review-diffcount">{fmtCounts(detail)}</span>
              </div>
              {detail.hunks.length === 0 && <div className="anchor-review-hint">没有可对比的片段。</div>}
              {detail.hunks.map((h) => (
                <div
                  key={h.index}
                  className={`anchor-hunk${h.superseded ? " superseded" : ""}${h.applied ? " applied" : ""}`}
                >
                  <div className="anchor-hunk-head">
                    <span className="anchor-hunk-no">片段 #{h.index}</span>
                    <span className="anchor-hunk-range">
                      改前 {h.beforeStart + 1}–{h.beforeEnd} 行 → 改后 {h.afterStart + 1}–{h.afterEnd} 行
                    </span>
                    {h.applied && (
                      <span className="anchor-hunk-badge applied">
                        <Check size={10} /> 已应用
                      </span>
                    )}
                    {h.superseded ? (
                      <span className="anchor-hunk-badge blocked" title={h.reason}>
                        不可应用 · {h.reason}
                      </span>
                    ) : (
                      <button
                        className="anchor-hunk-apply"
                        disabled={applying !== null}
                        onClick={() => void applyHunk(detail.path, h.index)}
                        title="把这一段还原成改前内容（只影响这一段）"
                      >
                        {applying === h.index ? <Loader2 size={11} className="animate-spin" /> : <Undo2 size={11} />}
                        应用此段
                      </button>
                    )}
                  </div>
                  {h.before.length > 0 && (
                    <pre className="anchor-hunk-lines before">
                      {h.before.map((l, i) => (
                        <div key={i} className="anchor-hunk-line del">
                          <span className="anchor-hunk-ln">{h.beforeStart + i + 1}</span>
                          <span className="anchor-hunk-text">- {l}</span>
                        </div>
                      ))}
                    </pre>
                  )}
                  {h.after.length > 0 && (
                    <pre className="anchor-hunk-lines after">
                      {h.after.map((l, i) => (
                        <div key={i} className="anchor-hunk-line add">
                          <span className="anchor-hunk-ln">{h.afterStart + i + 1}</span>
                          <span className="anchor-hunk-text">+ {l}</span>
                        </div>
                      ))}
                    </pre>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
