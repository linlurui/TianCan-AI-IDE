import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Anchor, Archive, ArchiveRestore, FileSearch, RotateCcw } from "lucide-react";
import {
  SessionAnchors,
  SessionArchiveAnchor,
  SessionRollback,
  type AnchorInfo,
} from "../bindings/ctxsvc";

// 聊天区右侧的竖直「锚点刻度」条。
//
// 一个刻度 = 一轮工作（用户的一次发言及其后续）：摘要做 tooltip，
// 左键定位到该轮消息，右键审查/归档/回退。
// 「审查」打开右侧 AI Changes 面板，列出这一轮 AI 改过的文件与逐段版本。
// 见 docs/anchor-design.md。

interface Props {
  projectRoot: string;
  sessionID?: string;
  /** 当前消息的顺序号列表（已过滤过工具消息），用于把锚点 seq 换算成位置 */
  messageSeqs: number[];
  onRollback?: (seq: number) => void;
  /** 外部需要刷新锚点时递增此值 */
  refreshKey?: number;
  /** 右键「审查此锚点」：跳到 AI Changes 面板看这一轮的文件改动 */
  onReview?: (a: AnchorInfo) => void;
}

type Menu = { x: number; y: number; anchor: AnchorInfo } | null;

// 刻度是 8px 的点，挨得太近就会互相盖住、糊成一条线，一个都点不到。
// 相邻不足这个百分比间距的刻度合并成一簇（一簇仍是一个点，但会说明它代表几轮）。
const MIN_GAP_PCT = 2;

/** 一个刻度：通常是一轮对话，密集时是一簇（count>1）。样子始终一样。 */
interface Mark {
  anchor: AnchorInfo; // 簇里最早的那个（也是点击定位的目标）
  last: AnchorInfo;   // 簇里最晚的那个
  top: number;        // 竖轴百分比位置
  count: number;      // 这一簇代表几轮
  archived: boolean;  // 簇内全部归档才算归档
}

function fmtTime(ms: number): string {
  if (!ms) return "-";
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

export default function AnchorTimeline({ projectRoot, sessionID = "main", messageSeqs, onRollback, refreshKey, onReview }: Props) {
  const [anchors, setAnchors] = useState<AnchorInfo[]>([]);
  const [hover, setHover] = useState<Mark | null>(null);
  const [menu, setMenu] = useState<Menu>(null);
  const [busy, setBusy] = useState(false);
  const railRef = useRef<HTMLDivElement>(null);

  // 最新一条已落库消息的序号。消息条数没变、但序号刚写回来时（新发言落库）
  // 只有它会变，靠它才能触发重新拉取。
  const lastSeq = messageSeqs.length ? messageSeqs[messageSeqs.length - 1] : 0;

  const load = useCallback(async () => {
    if (!projectRoot) return;
    try {
      setAnchors((await SessionAnchors(projectRoot, sessionID)) ?? []);
    } catch {
      /* 会话尚未创建：静默 */
    }
  }, [projectRoot, sessionID]);

  useEffect(() => {
    void load();
  }, [load, refreshKey, messageSeqs.length, lastSeq]);

  // 兜底轮询：依赖数组只覆盖"消息条数/最新序号变化"，但只要有任何一条链路
  // 没触发（比如后端在别处补了锚点、序号写回失败），刻度尺就会一直停在旧数据，
  // 看着像"发了消息不加锚点"。本地 SQLite 查几十行，5 秒一次可以忽略。
  useEffect(() => {
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  // 点击空白处关闭菜单
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    window.addEventListener("click", close);
    window.addEventListener("scroll", close, true);
    return () => {
      window.removeEventListener("click", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [menu]);

  // 把锚点 seq 换算成竖轴上的百分比位置。
  // 取「seq 不超过该锚点的最后一条消息」作为它所代表的位置 —— 锚点是"到这里为止的状态"。
  // 会话越长刻度越密，所以最后再按最小间距合并成簇，保证每个点都看得见、点得到。
  const marks = useMemo<Mark[]>(() => {
    if (!anchors.length) return [];
    const seqs = messageSeqs;
    const total = seqs.length;
    // 只摆「对话轮」。系统自己记的压缩/存储点是内部账，用户不关心也不该看到 ——
    // 混进来只会让刻度尺上出现一堆意义不明的小灰点。
    const raw = anchors
      .filter((a) => a.kind === "turn")
      .sort((a, b) => a.seq - b.seq)
      .map((a) => {
        let idx = -1;
        for (let i = 0; i < total; i++) {
          if (seqs[i] <= a.seq) idx = i;
          else break;
        }
        // 锚点早于所有已知消息时贴顶；晚于所有消息时贴底。
        const top = idx < 0 ? 0 : ((idx + 1) / Math.max(total, 1)) * 100;
        return { anchor: a, top: Math.min(Math.max(top, 1), 99) };
      });
    const out: Mark[] = [];
    for (const item of raw) {
      const a = item.anchor;
      const prev = out[out.length - 1];
      if (prev && item.top - prev.top < MIN_GAP_PCT) {
        prev.count += 1;
        prev.last = a;
        prev.archived = prev.archived && a.status === "archived";
        continue;
      }
      out.push({
        anchor: a,
        last: a,
        top: item.top,
        count: 1,
        archived: a.status === "archived",
      });
    }
    return out;
  }, [anchors, messageSeqs]);

  const locate = (seq: number) => {
    // 在 wrap（而非 document）内查找：刻度条是 wrap 的直接子元素，
    // closest(".ai-messages") 在这里取不到，取 wrap 才是它实际的父容器。
    const root = railRef.current?.closest(".ai-messages-wrap") as HTMLElement | null;
    const scope = root ?? document;
    const nodes = Array.from(scope.querySelectorAll<HTMLElement>("[data-seq]"));
    if (!nodes.length) return;
    let target = nodes[0];
    for (const n of nodes) {
      const s = Number(n.dataset.seq);
      if (!Number.isNaN(s) && s <= seq) target = n;
    }
    target.scrollIntoView({ behavior: "smooth", block: "center" });
    nodes.forEach((n) => n.classList.remove("anchor-flash"));
    target.classList.add("anchor-flash");
    window.setTimeout(() => target.classList.remove("anchor-flash"), 1600);
  };

  const doArchive = async (a: AnchorInfo) => {
    setBusy(true);
    try {
      await SessionArchiveAnchor(projectRoot, sessionID, a.seq, a.status !== "archived");
      await load();
    } finally {
      setBusy(false);
      setMenu(null);
    }
  };

  const doRollback = async (a: AnchorInfo) => {
    if (!window.confirm(`确认回退到锚点 #${a.seq}？\n\n该点之后的消息将从上下文中移除，本次操作会记入会话流水。`)) return;
    setBusy(true);
    try {
      await SessionRollback(projectRoot, sessionID, a.seq, `回退到锚点 #${a.seq}`);
      onRollback?.(a.seq);
      await load();
    } finally {
      setBusy(false);
      setMenu(null);
    }
  };

  const doReview = (a: AnchorInfo) => {
    setMenu(null);
    onReview?.(a);
  };

  // 没有可摆的轮次就不画：只有系统内部存储点时也别留一条空竖线在那儿。
  if (!projectRoot || marks.length === 0) return null;

  return (
    <>
      <div className="anchor-rail" ref={railRef}>
        <div className="anchor-rail-line" />
        {marks.map((mk) => {
          const anchor = mk.anchor;
          return (
          <button
            key={anchor.seq}
            className={[
              "anchor-tick",
              mk.archived ? "archived" : "",
              hover?.anchor.seq === anchor.seq ? "hot" : "",
            ].join(" ")}
            style={{ top: `${mk.top}%` }}
            // 自带悬浮卡片，不能再挂原生 title，否则两个提示叠在一起。
            aria-label={
              mk.count > 1
                ? `#${anchor.seq}–#${mk.last.seq} 共 ${mk.count} 轮`
                : `#${anchor.seq} ${anchor.summary || anchor.title || "锚点"}`
            }
            onClick={(e) => {
              e.stopPropagation();
              locate(anchor.msgSeq || anchor.seq);
            }}
            onContextMenu={(e) => {
              e.preventDefault();
              e.stopPropagation();
              setMenu({ x: e.clientX, y: e.clientY, anchor });
            }}
            onMouseEnter={() => setHover(mk)}
            onMouseLeave={() => setHover((h) => (h?.anchor.seq === anchor.seq ? null : h))}
          />
          );
        })}

        {hover && (
          <div
            className="anchor-tip"
            style={{ top: `${marks.find((m) => m.anchor.seq === hover.anchor.seq)?.top ?? 0}%` }}
          >
            <div className="anchor-tip-head">
              {hover.count > 1
                ? `#${hover.anchor.seq}–#${hover.last.seq} 共 ${hover.count} 轮`
                : hover.anchor.summary || hover.anchor.title || "本轮"}
            </div>
            <div className="anchor-tip-meta">
              {hover.archived ? "已归档 · " : ""}
              {fmtTime(hover.anchor.time)}
            </div>
            <div className="anchor-tip-dim">
              {hover.count > 1 ? "这一带有多轮，点击定位到最早一条" : "左键定位 · 右键审查/归档/回退"}
            </div>
          </div>
        )}
      </div>

      {menu && (
        <div
          className="anchor-menu"
          style={{ left: Math.min(menu.x, window.innerWidth - 190), top: Math.min(menu.y, window.innerHeight - 150) }}
          onClick={(e) => e.stopPropagation()}
        >
          <div className="anchor-menu-head">
            #{menu.anchor.seq} {menu.anchor.summary || menu.anchor.title || "锚点"}
          </div>
          <button className="anchor-menu-item" disabled={busy} onClick={() => { locate(menu.anchor.msgSeq || menu.anchor.seq); setMenu(null); }}>
            <Anchor size={12} /> 定位到此处
          </button>
          <button className="anchor-menu-item" disabled={busy} onClick={() => doReview(menu.anchor)}>
            <FileSearch size={12} /> 审查此锚点（改过的文件）
          </button>
          <button className="anchor-menu-item" disabled={busy} onClick={() => void doArchive(menu.anchor)}>
            {menu.anchor.status === "archived" ? <ArchiveRestore size={12} /> : <Archive size={12} />}
            {menu.anchor.status === "archived" ? "取消归档" : "归档（不再带入上下文）"}
          </button>
          <button className="anchor-menu-item danger" disabled={busy} onClick={() => void doRollback(menu.anchor)}>
            <RotateCcw size={12} /> 回退到这里
          </button>
        </div>
      )}

    </>
  );
}
