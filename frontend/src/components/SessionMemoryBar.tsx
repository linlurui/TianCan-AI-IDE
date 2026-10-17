import React, { useCallback, useEffect, useState } from "react";
import { Anchor, History, RotateCcw, ShieldAlert, Search, Eye, ChevronDown, ChevronRight, Loader2, CircleDot } from "lucide-react";
import {
  SessionOpen, SessionStats, SessionAnchors, SessionDropAnchor, SessionRollback,
  SessionAudit, SessionRedlines, SessionAddRedline, SessionRecall, PromptPreview,
  EmbeddingConfig,
  type AnchorInfo, type AuditEntry, type Redline, type RecallHit, type Packed, type SessionInfo,
} from "../bindings/ctxsvc";

// 聊天框里的「项目记忆」工具条：锚点 / 审计 / 回退 / 红线 / 语义召回 / 提示词预览。
// 设计原则：所有操作都能在这里"看见"——会话不再是黑盒，锚点可查、流水可审、状态可退。

type Panel = "none" | "anchors" | "audit" | "redlines" | "recall" | "prompt";

interface Props {
  projectRoot: string;
  sessionID?: string;
  /** 当前输入框内容，用于语义召回与提示词预览 */
  input?: string;
  /** 回退成功后通知上层刷新消息列表 */
  onRollback?: (seq: number) => void;
  compact?: boolean;
}

function fmtTime(ms: number): string {
  if (!ms) return "-";
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

const KIND_LABEL: Record<string, string> = {
  msg: "消息",
  "seg.open": "开段",
  "seg.close": "收段",
  "req.add": "需求",
  "req.done": "需求完成",
  "red.add": "红线",
  rollback: "回退",
  recall: "召回",
  title: "标题",
};

export default function SessionMemoryBar({ projectRoot, sessionID = "main", input = "", onRollback, compact }: Props) {
  const [panel, setPanel] = useState<Panel>("none");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>("");

  const [info, setInfo] = useState<SessionInfo | null>(null);
  const [anchors, setAnchors] = useState<AnchorInfo[]>([]);
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [redlines, setRedlines] = useState<Redline[]>([]);
  const [hits, setHits] = useState<RecallHit[]>([]);
  const [packed, setPacked] = useState<Packed | null>(null);
  const [embed, setEmbed] = useState<{ remote: boolean; model: string; degraded: boolean } | null>(null);
  const [newRed, setNewRed] = useState("");

  const guard = useCallback(async (fn: () => Promise<any>) => {
    setBusy(true);
    setErr("");
    try {
      await fn();
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  }, []);

  // 打开即拉取会话概览与 embedding 状态
  useEffect(() => {
    if (!projectRoot) return;
    let alive = true;
    (async () => {
      try {
        const s = await SessionOpen(projectRoot, sessionID);
        if (alive) setInfo(s);
      } catch { /* 会话尚未创建，静默 */ }
      try {
        const c = await EmbeddingConfig();
        if (alive) setEmbed({ remote: !!c.remote, model: c.model, degraded: !!c.degraded });
      } catch { /* ignore */ }
    })();
    return () => { alive = false; };
  }, [projectRoot, sessionID]);

  const refresh = useCallback(async () => {
    try {
      const s = await SessionOpen(projectRoot, sessionID);
      setInfo(s);
    } catch { /* ignore */ }
  }, [projectRoot, sessionID]);

  const toggle = (p: Panel) => {
    if (panel === p) { setPanel("none"); return; }
    setPanel(p);
    if (p === "anchors") void guard(async () => setAnchors((await SessionAnchors(projectRoot, sessionID)) ?? []));
    if (p === "audit") void guard(async () => setAudit((await SessionAudit(projectRoot, sessionID, 0, 200)) ?? []));
    if (p === "redlines") void guard(async () => setRedlines((await SessionRedlines(projectRoot, sessionID)) ?? []));
  };

  const doRecall = () => {
    if (!input.trim()) return;
    void guard(async () => setHits((await SessionRecall(projectRoot, sessionID, input.trim(), 5)) ?? []));
  };

  const doPreview = () => {
    if (!input.trim()) return;
    void guard(async () => {
      const p = await PromptPreview(projectRoot, sessionID, input.trim());
      setPacked(p ?? null);
    });
  };

  const doAnchor = () => {
    void guard(async () => {
      await SessionDropAnchor(projectRoot, sessionID);
      setAnchors((await SessionAnchors(projectRoot, sessionID)) ?? []);
      await refresh();
    });
  };

  const doRollback = (seq: number, note: string) => {
    if (!window.confirm(`确认回退到 seq=${seq}？\n\n回退后该点之后的消息将从上下文中移除，但审计流水会完整保留这一操作记录。`)) return;
    void guard(async () => {
      await SessionRollback(projectRoot, sessionID, seq, note);
      onRollback?.(seq);
      setAnchors((await SessionAnchors(projectRoot, sessionID)) ?? []);
      setAudit((await SessionAudit(projectRoot, sessionID, 0, 200)) ?? []);
      await refresh();
    });
  };

  const doAddRed = () => {
    const t = newRed.trim();
    if (!t) return;
    void guard(async () => {
      await SessionAddRedline(projectRoot, sessionID, t);
      setNewRed("");
      setRedlines((await SessionRedlines(projectRoot, sessionID)) ?? []);
      await refresh();
    });
  };

  if (!projectRoot) return null;

  const chevron = (p: Panel) => (panel === p ? <ChevronDown size={12} /> : <ChevronRight size={12} />);

  return (
    <div className={`sess-bar${compact ? " sess-bar-compact" : ""}`}>
      <div className="sess-bar-row">
        <button className={`sess-btn${panel === "anchors" ? " active" : ""}`} onClick={() => toggle("anchors")} title="锚点：查看 / 打点 / 回退">
          <Anchor size={13} /> 锚点{info?.anchors ? ` ${info.anchors}` : ""} {chevron("anchors")}
        </button>
        <button className={`sess-btn${panel === "audit" ? " active" : ""}`} onClick={() => toggle("audit")} title="审计：完整事件流水">
          <History size={13} /> 审计 {chevron("audit")}
        </button>
        <button className={`sess-btn${panel === "redlines" ? " active" : ""}`} onClick={() => toggle("redlines")} title="红线：必须遵守的约束">
          <ShieldAlert size={13} /> 红线{info?.redlines ? ` ${info.redlines}` : ""} {chevron("redlines")}
        </button>
        <button className={`sess-btn${panel === "recall" ? " active" : ""}`} onClick={() => toggle("recall")} title="语义召回：从历史中检索">
          <Search size={13} /> 召回 {chevron("recall")}
        </button>
        <button className={`sess-btn${panel === "prompt" ? " active" : ""}`} onClick={() => toggle("prompt")} title="提示词预览：看看到底喂了什么">
          <Eye size={13} /> 提示词 {chevron("prompt")}
        </button>
        {info && (
          <span className="sess-meta" title="当前会话的消息数与估算 token">
            {info.messages} 条 · ~{info.tokens} tok
            {embed?.remote ? " · 神经向量" : " · 本地向量"}
            {embed?.degraded ? "（已降级）" : ""}
          </span>
        )}
        {busy && <Loader2 size={13} className="sess-spin" />}
      </div>

      {err && <div className="sess-err">{err}</div>}

      {panel === "anchors" && (
        <div className="sess-panel">
          <div className="sess-panel-head">
            <span>锚点快照（状态检查点，可用于回退）</span>
            <button className="sess-mini" onClick={doAnchor} disabled={busy}>+ 打锚点</button>
          </div>
          {anchors.length === 0 && <div className="sess-empty">还没有锚点。累积 {64} 条事件会自动打点，也可以手动打。</div>}
          {anchors.map((a) => (
            <div key={a.seq} className="sess-item">
              <CircleDot size={12} className="sess-dot" />
              <span className="sess-seq">#{a.seq}</span>
              <span className="sess-title">{a.title || "（无标题）"}</span>
              <span className="sess-dim">{a.messages} 条 · ~{a.tokens} tok · {fmtTime(a.time)}</span>
              <button className="sess-mini danger" onClick={() => doRollback(a.seq, `回退到锚点 #${a.seq}`)} disabled={busy}>
                <RotateCcw size={11} /> 回退到这里
              </button>
            </div>
          ))}
        </div>
      )}

      {panel === "audit" && (
        <div className="sess-panel">
          <div className="sess-panel-head"><span>审计流水（只追加，不可篡改）</span></div>
          {audit.length === 0 && <div className="sess-empty">暂无事件。</div>}
          <div className="sess-audit">
            {audit.map((a) => (
              <div key={a.seq} className={`sess-item sess-audit-row kind-${a.kind.replace(".", "-")}`}>
                <span className="sess-seq">#{a.seq}</span>
                <span className="sess-kind">{KIND_LABEL[a.kind] ?? a.kind}</span>
                {a.role && <span className="sess-role">{a.role}</span>}
                <span className="sess-preview">{a.preview}</span>
                <span className="sess-dim">{fmtTime(a.time)}</span>
                <button className="sess-mini danger" onClick={() => doRollback(a.seq, `回退到 #${a.seq} 之前`)} disabled={busy}>
                  <RotateCcw size={11} />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {panel === "redlines" && (
        <div className="sess-panel">
          <div className="sess-panel-head">
            <span>项目红线（永久注入提示词，永不压缩）</span>
          </div>
          <div className="sess-add">
            <input
              value={newRed}
              onChange={(e) => setNewRed(e.target.value)}
              placeholder="例如：不要推送代码到远端"
              onKeyDown={(e) => { if (e.key === "Enter") doAddRed(); }}
            />
            <button className="sess-mini" onClick={doAddRed} disabled={busy || !newRed.trim()}>添加</button>
          </div>
          {redlines.length === 0 && <div className="sess-empty">暂无红线。说出约束（"不要…"/"必须…"/"注意…"）会自动入册。</div>}
          {redlines.map((r) => (
            <div key={r.id} className={`sess-item sess-red sev-${r.severity ?? "hard"}`}>
              <ShieldAlert size={12} />
              <span className="sess-preview">{r.text}</span>
              <span className="sess-dim">{(r.severity ?? "hard") === "hard" ? "红线" : "注意"} · {r.source === "extracted" ? "自动抽取" : "手工"} · #{r.seq}</span>
            </div>
          ))}
        </div>
      )}

      {panel === "recall" && (
        <div className="sess-panel">
          <div className="sess-panel-head">
            <span>语义召回（用输入框内容检索历史）</span>
            <button className="sess-mini" onClick={doRecall} disabled={busy || !input.trim()}>检索</button>
          </div>
          {!input.trim() && <div className="sess-empty">在输入框里写点什么，再点检索。</div>}
          {hits.length === 0 && input.trim() && <div className="sess-empty">没有命中（可先点检索）。</div>}
          {hits.map((h) => (
            <div key={h.seq} className="sess-item sess-hit">
              <span className="sess-seq">#{h.seq}</span>
              <span className="sess-score" title={`向量 ${h.vecScore.toFixed(3)} · 关键词 ${h.kwScore.toFixed(3)}`}>
                {(h.score * 100).toFixed(0)}%
              </span>
              <span className="sess-preview">{h.text}</span>
            </div>
          ))}
        </div>
      )}

      {panel === "prompt" && (
        <div className="sess-panel">
          <div className="sess-panel-head">
            <span>提示词装配预览</span>
            <button className="sess-mini" onClick={doPreview} disabled={busy || !input.trim()}>装配</button>
          </div>
          {!packed && <div className="sess-empty">点击"装配"查看将注入的系统上下文。</div>}
          {packed && (
            <>
              <div className="sess-dim">共 ~{packed.tokens} tok · 分块：{packed.sections.map((s) => `${s.name}(${s.tokens})`).join(" · ")}</div>
              {packed.dropped && packed.dropped.length > 0 && (
                <div className="sess-warn">预算裁剪：{packed.dropped.join("、")}</div>
              )}
              <pre className="sess-pre">{packed.system}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}
