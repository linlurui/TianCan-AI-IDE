import React from "react";
import { SessionAppend, SessionMessages, type MsgView } from "../bindings/ctxsvc";

// 项目级会话持久化（P0）：消息不再只活在前端内存。
//
// 这个 hook 是「聊天 ↔ 后端 journal」的唯一通道。以前这段逻辑散在 App.tsx 里，
// 结果主窗口有、独立 AI 窗口没有 —— 在 AI 窗口发的消息一次都没落库，锚点、回退、
// 记忆提炼全都跟着空转。现在两边都从这里取。
//
// 去重按「消息稳定标识 uid」：既不能按内容（用户重复说"在吗"会被误判成已写过，
// 后端收不到、本轮锚点永远不出现），也不能按对象身份（消息一被 spread 复制身份
// 就变，写入后 seq 写不回原对象 → 下一次又写一遍 → 无限循环把会话撑爆）。
// uid 在首次考虑落盘时才惰性分配，之后跟着消息走，两种坑一起避开。

/** 参与持久化的消息只需要这几个字段，调用方的 Message 类型比它更宽。 */
export interface PersistableMessage {
  role: string;
  content: string;
  seq?: number;
  uid?: string;
  streaming?: boolean;
}

let msgUidSeq = 0;

/** 给消息分配稳定标识（原地写入，保证同一条消息前后两次拿到的是同一个 uid）。 */
export function ensureUid(m: PersistableMessage): string {
  if (!m.uid) m.uid = `u${++msgUidSeq}`;
  return m.uid;
}

export interface SessionPersistence {
  /** 重新从后端拉取历史（回退、外部改动后调用）。后端为准替换当前消息。 */
  reload: () => void;
}

export function useSessionPersistence<M extends PersistableMessage>(
  root: string,
  sessionID: string,
  messages: M[],
  setMessages: React.Dispatch<React.SetStateAction<M[]>>,
): SessionPersistence {
  const persistedRef = React.useRef<Map<string, "pending" | "done">>(new Map());
  // 后端水位线：后端已有的前 N 条消息不必再写入。
  const baselineRef = React.useRef(0);
  // 恢复闸门：后端历史还没取回来之前不能写入，否则会把前端已有的整段历史
  // 当成"新消息"再写一遍（持久化 effect 是同步的，必然抢在 await 之前跑完）。
  const restoredRef = React.useRef(false);
  const rootRef = React.useRef("");

  // setMessages 在调用方每次渲染都是新函数，直接进依赖数组会让「拉历史」的
  // effect 每渲染跑一次，反过来把用户还没落库的消息冲掉。
  const setMessagesRef = React.useRef(setMessages);
  setMessagesRef.current = setMessages;

  // 换项目就是换会话：后端消息的 uid 是 "s"+seq，不同项目之间会撞号，
  // 不清干净的话新项目里的消息可能被上一项目遗留的登记当成"已写过"。
  if (rootRef.current !== root) {
    rootRef.current = root;
    persistedRef.current.clear();
    baselineRef.current = 0;
    restoredRef.current = false;
  }

  const load = React.useCallback(async () => {
    if (!root) return;
    let rows: MsgView[];
    try {
      rows = (await SessionMessages(root, sessionID, 0)) ?? [];
    } catch {
      // 会话尚不存在（项目第一次打开）：忽略，用户第一条消息会把它创建出来。
      restoredRef.current = true;
      return;
    }
    if (rows.length === 0) {
      // 后端还没有这段历史。这里不动前端现有消息：它们还没落库，写回去才不会丢。
      restoredRef.current = true;
      return;
    }
    // seq 必须带上：侧边锚点刻度靠它定位到具体消息。
    const restored = rows.map(
      (m) =>
        ({
          role: m.role,
          content: m.content,
          seq: m.seq,
          // 后端消息用 seq 当稳定标识，前端再怎么重建数组都不会认错。
          uid: `s${m.seq}`,
        } as unknown as M),
    );
    restored.forEach((m) => persistedRef.current.set(m.uid!, "done"));
    // 后端是持久化的真相：直接整套替换前端，两边长度从此对齐。
    // 之前是「前端非空就不动」，于是出现前端比后端短的情况 —— 那时下面的
    // 水位线会把用户 NEW 发的消息也算成"已入库"，一条都不写，
    // 表现就是"发了消息锚点不涨"。
    setMessagesRef.current(restored);
    baselineRef.current = rows.length;
    restoredRef.current = true;
  }, [root, sessionID]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 落定的消息写入后端（覆盖 auto / local / api / web 全部链路）
  React.useEffect(() => {
    if (!root) return;
    if (!restoredRef.current) return; // 等后端历史取回来，避免整段重复写入
    // 前端数组比水位线还短 —— 清屏、回退、别的窗口改过会话之后，"前 N 条已经在
    // 后端"这个前提就不成立了。不跟着降，新发的消息会被永久误判成已入库。
    if (baselineRef.current > messages.length) baselineRef.current = messages.length;
    for (let i = 0; i < messages.length; i++) {
      const m = messages[i];
      if (m.streaming) continue;
      if (m.role !== "user" && m.role !== "assistant") continue;
      if (!m.content.trim()) continue;
      // 带 seq = 后端来的（历史恢复 / 已写回），不需要再写。
      if (typeof m.seq === "number") continue;
      const uid = ensureUid(m);
      if (i < baselineRef.current) {
        // 后端已经存在这一段（上次运行留下的），只登记不重写。
        persistedRef.current.set(uid, "done");
        continue;
      }
      if (persistedRef.current.has(uid)) continue;
      persistedRef.current.set(uid, "pending");
      // 后端返回的顺序号必须写回这条消息：侧边锚点刻度靠它把刻度摆在对应消息
      // 旁边，也靠 DOM 上的 data-seq 定位过去。丢掉它，当前会话新发的消息
      // 就永远定位不到（点击刻度只会跳到更早的一条）。
      //
      // 写回时按 uid 匹配：按对象身份会在消息被复制后找不到目标（写不回 seq →
      // 下一轮又写一遍 → 无限循环），按内容又会把重复的两句话认成同一条。
      void SessionAppend(root, sessionID, m.role, m.content)
        .then((seq: number) => {
          if (typeof seq !== "number" || seq <= 0) {
            persistedRef.current.delete(uid); // 后端没给出序号，允许再试一次
            return;
          }
          persistedRef.current.set(uid, "done");
          setMessagesRef.current((prev) => {
            // 只找还没拿到 seq 的那一条：万一数组里短暂存在同城 uid 的副本
            // （流式替换的瞬间），也要把序号写到缺的那个身上，否则它会一直重发。
            const j = prev.findIndex((x) => x.uid === uid && typeof x.seq !== "number");
            if (j < 0) return prev;
            const next = prev.slice();
            next[j] = { ...prev[j], seq };
            return next;
          });
        })
        .catch(() => {
          persistedRef.current.delete(uid); // 失败则允许下次重试
        });
    }
  }, [messages, root, sessionID]);

  return React.useMemo(() => ({ reload: () => void load() }), [load]);
}
