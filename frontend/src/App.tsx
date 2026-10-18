import React, { useCallback, useRef, useEffect } from "react";
import { useTranslation } from "./i18n";
import WindowRouter from "./components/WindowRouter";
import {
  Code2, Files, FolderPlus, GitBranch, Globe,
  LocateFixed, RefreshCw, Save, Settings, Wrench, Terminal, Bug,
  Plus, X, Puzzle, Search, Download, Trash2, Store, PackageCheck,
  ListTree, Database, FlaskConical, Rocket, ScrollText, Server,
  Network, History, ArrowLeftRight,
} from "lucide-react";

import FileTree from "./components/FileTree";
import SplitEditorArea from "./components/SplitEditorArea";
import AIPanel, { Message, type ActivityItem } from "./components/AIPanel";
import { appendActivity, describeTool, finishToolActivity, withConversationContext } from "./aiActivity";
import StatusBar from "./components/StatusBar";
import GitPanel from "./components/GitPanel";
import { AIChangesPanel, AgentTask, TaskFile } from "./components/AIChangesPanel";
import { Task } from "./bindings/task";
// OutlinePanel and CallHierarchyPanel moved into AnalysisPanel accordion
import RemotePanel from "./components/RemotePanel";

import { ReadFile, WriteFile, GetDirectoryTree, SelectDirectory, FileNode } from "./bindings/filesystem";
import { GetStatus, InitRepo, StageAll, Commit, Push } from "./bindings/git";
import {
  SearchExtensions, GetInstalledExtensions, InstallExtension, UninstallExtension,
  InstallLocalVSIX, GetExtensionIcon, GetExtensionContributes,
  Extension as ExtType, ExtensionContributes,
} from "./bindings/extension";
import { StartAndGetPort } from "./bindings/terminal";
import { StartAndGetPort as StartLspPort } from "./bindings/lsp";
import { AgentRun, AgentRunAPI, AgentRunWeb, StartAgentStream, StopAgentRequest, RespondPermission, SmartAgentRun, SmartAgentRunV2 } from "./bindings/ai";
import { RemoteReadFile, DockerReadFile } from "./bindings/remote";
import { Events } from "@wailsio/runtime";
import type { ModelSource } from "./components/AIPanel";
import PermissionConfirmModal, { type PermissionRequest } from "./components/PermissionConfirmModal";
import { applyExtensionContributes } from "./extensions/contributes";
import { RunSetup } from "./bindings/project";
import { SessionAppend, SessionMessages, type MsgView } from "./bindings/ctxsvc";
import { Project, ProjectType } from "./types";
import { RevealSignal } from "./components/FileTree";
import TerminalPanel from "./components/TerminalPanel";
import ProjectWizard, { type WizardResult } from "./components/ProjectWizard";
import GlobalProgress, { type InstallTask } from "./components/GlobalProgress";
import ExtensionDrawer from "./components/ExtensionDrawer";
import AnalysisPanel from "./components/AnalysisPanel";
import SearchPanel from "./components/SearchPanel";
import DatabasePanel from "./components/DatabasePanel";
import TestingPanel from "./components/TestingPanel";
import ApiTestPanel from "./components/ApiTestPanel";
import PlaywrightPanel from "./components/PlaywrightPanel";
import DeployWorkspace from "./components/DeployWorkspace";
import DeployPanel from "./components/DeployPanel";
import ModelStatusIndicator from "./components/ModelStatusIndicator";
import GitDiffViewer from "./components/GitDiffViewer";
import GitCommitDetail from "./components/GitCommitDetail";
import RunEnvConfigModal from "./components/RunEnvConfigModal";
import DebugPanel, { type Breakpoint, type DebugPanelHandle } from "./components/DebugPanel";
import CommitFileDiffViewer from "./components/CommitFileDiffViewer";
import OutputPanel from "./components/OutputPanel";
import SettingsModal from "./components/SettingsModal";
import { PLUGIN_RECOMMENDATIONS, type PluginRec } from "./data/pluginRecommendations";

// ── Zustand stores ──────────────────────────────────────────────────────────
import { useEditorStore, Tab } from "./store/editorStore";
import { useProjectStore } from "./store/projectStore";
import { useUIStore, SidebarTab } from "./store/uiStore";

// ── Local state interfaces ──────────────────────────────────────────────────
interface Extension {
  id: string; name: string; technicalName: string; limited: boolean;
  publisher: string; version: string; description: string;
  installed: boolean; installing?: boolean; iconUrl?: string;
}
type ExtensionTab = "marketplace" | "installed";

// ── Helpers ─────────────────────────────────────────────────────────────────
function extOf(path: string) {
  const parts = path.split(".");
  return parts.length > 1 ? parts[parts.length - 1].toLowerCase() : "";
}

const LANG_MAP: Record<string, string> = {
  ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
  go: "go", py: "python", rs: "rust", java: "java", cpp: "cpp", c: "c",
  cs: "csharp", rb: "ruby", php: "php", swift: "swift", kt: "kotlin",
  dart: "dart",
};

function langOf(path: string): string {
  const base = path.split("/").pop()?.toLowerCase() ?? "";
  if (base === "dockerfile") return "dockerfile";
  return LANG_MAP[extOf(path)] ?? "plaintext";
}

const BINARY_EXTS = new Set(["pdf", "doc", "docx", "xls", "xlsx", "csv", "ppt", "pptx"]);
const ANALYSIS_EXTS = new Set([
  "html", "htm", "xml", "css", "scss", "less", "sass",
  "ts", "tsx", "js", "jsx", "go", "py", "java", "kt", "dart", "scala",
  "rs", "c", "cpp", "cc", "h", "hpp", "php", "rb", "vue", "md", "mdx", "json",
]);

let msgUidSeq = 0;

/**
 * 给消息惰性分配一个稳定标识。
 * 直接写在原对象上（不换引用）：React 不会因为多一个字段就重渲染，而之后任何
 * `{...m}` 复制都会把它带走 —— 这正是落盘去重需要的性质。
 */
function ensureUid(m: Message): string {
  if (!m.uid) m.uid = `u${++msgUidSeq}`;
  return m.uid;
}

function normalizeAgentMessages(rawMessages: any[], pushActivity?: (kind: ActivityItem["kind"], text: string, detail?: string, toolName?: string) => void): Message[] {
  const rawMsgs = (rawMessages ?? []).filter((m: any) => m?.role !== "user");
  const agentMsgs: Message[] = [];

  for (let i = 0; i < rawMsgs.length; i++) {
    const m = rawMsgs[i] ?? {};
    const next = rawMsgs[i + 1] ?? {};
    const role = String(m.role || "assistant");
    const content = String(m.content ?? "");
    const name = m.name || m.toolName || "";

    if (!content.trim() && role !== "tool_call") continue;

    if (role === "assistant" && next && (next.role === "tool_call" || next.role === "tool_result" || next.role === "tool")) {
      pushActivity?.("thinking", "思考过程", content.trim() || "模型正在规划下一步");
    } else if (role === "tool_call") {
      const info = describeTool(name || "tool", content);
      pushActivity?.("tool", info.title, content, name || "tool");
    } else if (role === "tool_result" || role === "tool") {
      pushActivity?.("result", `工具完成 ${name || "结果"}`, content, name || "tool");
    } else if (role === "thinking") {
      pushActivity?.("thinking", "思考过程", content);
    } else {
      agentMsgs.push({ role: "assistant", content });
    }
  }

  return agentMsgs;
}

export default function App() {
  // ── Zustand ──────────────────────────────────────────────────────────────
  const editor = useEditorStore();
  const project = useProjectStore();
  const ui = useUIStore();

  // ── Session state from projectStore (per-project isolation) ─────────────
  const messages = project.activeMessages as Message[];
  const setMessages: React.Dispatch<React.SetStateAction<Message[]>> = (action) => {
    const projectState = useProjectStore.getState();
    if (typeof action === 'function') {
      const currentMessages = projectState.activeMessages as Message[];
      const newMessages = (action as (prev: Message[]) => Message[])(currentMessages);
      projectState.replaceMessages(newMessages as any);
    } else {
      projectState.replaceMessages(action as any);
    }
  };
  const agentTasks = project.activeAgentTasks as unknown as AgentTask[];
  const setAgentTasks = (tasks: AgentTask[]) => {
    project.clearAgentTasks();
    tasks.forEach(t => project.addAgentTask(t as any));
  };

  // ── Local state ───────────────────────────────────────────────────────
  const [aiLoading, setAiLoading] = React.useState(false);
  const [aiActivityItems, setAiActivityItems] = React.useState<ActivityItem[]>([]);
  const streamRef = React.useRef<((evt: any) => void) | null>(null);
  const aiRunSourceRef = React.useRef<ModelSource | null>(null);
  const aiStopRequestedRef = React.useRef(false);
  const [permRequest, setPermRequest] = React.useState<PermissionRequest | null>(null);
  const [revealSignal, setRevealSignal] = React.useState<RevealSignal | null>(null);
  const [runEnvTarget, setRunEnvTarget] = React.useState<{ path: string; name: string; projectId?: string } | null>(null);
  const [installTasks, setInstallTasks] = React.useState<InstallTask[]>([]);
  const [extensions, setExtensions] = React.useState<Extension[]>([]);
  const [extensionSearch, setExtensionSearch] = React.useState("");
  const [extensionLoading, setExtensionLoading] = React.useState(false);
  const [extensionTab, setExtensionTab] = React.useState<ExtensionTab>("marketplace");
  const [installedExtensions, setInstalledExtensions] = React.useState<Extension[]>([]);
  const [extDragOver, setExtDragOver] = React.useState(false);
  const [extensionDetailExt, setExtensionDetailExt] = React.useState<Extension | null>(null);
  const [pluginRec, setPluginRec] = React.useState<(PluginRec & { ext: string }) | null>(null);
  const [breakpoints, setBreakpoints] = React.useState<Breakpoint[]>([]);
  const [debugPausedAt, setDebugPausedAt] = React.useState<{ file: string; line: number } | null>(null);
  const [analysisContent, setAnalysisContent] = React.useState<string | null>(null);
  const [showSettings, setShowSettings] = React.useState(false);
  const [aiContextSignal, setAiContextSignal] = React.useState<{ id: number; text: string } | null>(null);

  const debugPanelRef = useRef<DebugPanelHandle>(null);
  const monacoGlobalRef = useRef<any>(null);
  const [monacoInstance, setMonacoInstance] = React.useState<any>(null);
  const editorInstanceRef = useRef<any>(null);
  const openedPathsRef = useRef<Set<string>>(new Set());
  const dismissedRecs = useRef<Set<string>>(new Set());
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const pushAIActivity = useCallback((kind: ActivityItem["kind"], text: string, detail?: string, toolName?: string) => {
    setAiActivityItems((current) => {
      if (kind === "result" && toolName) {
        return finishToolActivity(current, toolName, detail || "", /error|failed|denied|not allowed|失败|错误|拒绝/i.test(detail || ""));
      }
      if (kind === "thinking" && text === "思考过程") {
        const now = Date.now();
        const settled = current.filter((item) => !(item.kind === "thinking" && item.text === "思考过程"));
        return appendActivity(settled, {
          id: now + Math.random(), kind, text, detail,
          status: "running", startedAt: now,
        });
      }
      if (kind === "tool" && toolName && current.some((item) => item.toolName === toolName && item.detail === detail)) {
        return current;
      }
      const now = Date.now();
      const terminal = kind === "success" || kind === "error";
      const settled = current.map((item) => item.status === "running" && (!item.toolName || terminal)
        ? { ...item, status: "success" as const, completedAt: now }
        : item);
      return appendActivity(settled, {
        id: now + Math.random(), kind, text, detail, toolName,
        status: kind === "error" ? "error" : kind === "success" || kind === "result" ? "success" : "running",
        startedAt: now,
        completedAt: terminal ? now : undefined,
      });
    });
  }, []);

  const stopAIRequest = useCallback(async () => {
    aiStopRequestedRef.current = true;
    await StopAgentRequest().catch(() => false);
    if (streamRef.current) {
      Events.Off("agent:stream");
      streamRef.current = null;
    }
    setPermRequest(null);
    setMessages((current) => current
      .filter((message) => !(message.role === "assistant" && !message.content.trim()))
      .map((message) => message.streaming ? { ...message, streaming: false } : message));
    pushAIActivity("success", "已停止生成");
    aiRunSourceRef.current = null;
    setAiLoading(false);
  }, [pushAIActivity]);

  useEffect(() => {
    const off = Events.On("subagent:progress", (evt: any) => {
      if (!aiRunSourceRef.current || aiRunSourceRef.current === "local") return;
      const data = evt?.data ?? evt;
      const toolName = String(data?.toolName || "");
      if (toolName && data?.output) {
        pushAIActivity("result", `工具完成 ${toolName}`, String(data.output), toolName);
      } else if (toolName) {
        const args = JSON.stringify(data?.toolArgs ?? {});
        const info = describeTool(toolName, args);
        pushAIActivity("tool", info.title, args, toolName);
      } else if (data?.error) {
        pushAIActivity("error", "执行失败", String(data.error));
      }
    });
    const offReasoning = Events.On("ai:reasoning", (evt: any) => {
      if (!aiRunSourceRef.current) return;
      const data = evt?.data ?? evt;
      const content = String(data?.content || "").trim();
      if (!content) return;
      pushAIActivity("thinking", "思考过程", content);
      // 追加到末尾的 thinking 消息，供「思考中…」下方的单行实时思考条展示
      setMessages((p) => {
        const updated = [...p];
        const last = updated[updated.length - 1];
        if (last && last.role === "thinking") {
          updated[updated.length - 1] = { ...last, content: last.content + "\n" + content };
          return updated;
        }
        // 流式回答进行中不打断；否则新开一个思考块
        if (last && (last.role === "assistant" || last.streaming)) return p;
        updated.push({ role: "thinking", content });
        return updated;
      });
    });
    return () => { off(); offReasoning(); };
  }, [pushAIActivity]);

  // ── 项目级会话持久化（P0）：消息不再只活在前端内存 ──────────────────────
  // 每条落定的 user/assistant 消息都会追加到后端 journal；重启后从后端恢复；
  // 回退后也以后端为准重新加载。
  //
  // 去重按「消息稳定标识 uid」：既不能按内容（用户重复说"在吗"会被误判成已写过，
  // 后端收不到、本轮锚点永远不出现），也不能按对象身份（消息一被 spread 复制身份
  // 就变，写入后 seq 写不回原对象 → 下一次又写一遍 → 无限循环把会话撑爆）。
  // uid 在首次考虑落盘时才惰性分配，之后跟着消息走，两种坑一起避开。
  const persistedRef = React.useRef<Map<string, "pending" | "done">>(new Map());
  // 后端水位线：后端已有的前 N 条消息不必再写入。
  const baselineRef = React.useRef(0);
  // 恢复闸门：后端历史还没取回来之前不能写入，否则会把前端已有的整段历史
  // 当成"新消息"再写一遍（持久化 effect 是同步的，必然抢在 await 之前跑完）。
  const restoredRef = React.useRef(false);

  const reloadSessionMessages = React.useCallback(async (root: string, force: boolean) => {
    if (!root) return;
    try {
      const rows: MsgView[] = await SessionMessages(root, "main", 0);
      if (!rows?.length) return;
      // seq 必须带上：侧边锚点刻度靠它定位到具体消息。
      const restored: Message[] = rows.map((m) => ({
        role: m.role as Message["role"],
        content: m.content,
        seq: m.seq,
        // 后端消息用 seq 当稳定标识，前端再怎么重建数组都不会认错。
        uid: `s${m.seq}`,
      }));
      // 这些都是后端已经有的（带 seq），登记为已写过，避免再写一遍。
      restored.forEach((m) => persistedRef.current.set(m.uid!, "done"));
      // 回退后后端消息变少，水位线必须跟着降，否则新消息会被误判为"已写入"。
      if (force) baselineRef.current = rows.length;
      else if (rows.length > baselineRef.current) baselineRef.current = rows.length;
      setMessages((prev) => (force || prev.length === 0 ? restored : prev));
    } catch {
      // 会话尚不存在（首次打开项目）：忽略，首次发消息时会创建
    }
  }, []);

  // 切换/打开项目时从后端恢复历史
  React.useEffect(() => {
    const root = project.activeProject?.rootPath ?? "";
    if (!root) return;
    let alive = true;
    restoredRef.current = false;
    // 换项目就是换会话：后端消息的 uid 是 "s"+seq，不同项目之间会撞号，
    // 不清干净的话新项目里的消息可能被上一项目遗留的登记当成"已写过"。
    persistedRef.current.clear();
    baselineRef.current = 0;
    void (async () => {
      try {
        const rows: MsgView[] = await SessionMessages(root, "main", 0);
        if (!alive) return;
        if (!rows?.length) { restoredRef.current = true; return; }
        // seq 必须带上：侧边锚点刻度靠它定位到具体消息。
        const restored: Message[] = rows.map((m) => ({
          role: m.role as Message["role"],
          content: m.content,
          seq: m.seq,
          uid: `s${m.seq}`,
        }));
        // 这些都是后端已经有的（带 seq），登记为已写过，避免再写一遍。
        restored.forEach((m) => persistedRef.current.set(m.uid!, "done"));
        // 后端已有这些消息，抬高水位线，避免重启时被重复写入后端。
        if (rows.length > baselineRef.current) baselineRef.current = rows.length;
        setMessages((prev) => (prev.length ? prev : restored));
      } catch {
        // 会话尚不存在或后端不可用：忽略，首次发消息时会创建
      } finally {
        if (alive) restoredRef.current = true;
      }
    })();
    return () => { alive = false; };
  }, [project.activeProject?.rootPath]);

  // 落定的消息写入后端（覆盖 auto / local / api / web 全部链路）
  React.useEffect(() => {
    const root = project.activeProject?.rootPath ?? "";
    if (!root) return;
    if (!restoredRef.current) return; // 等后端历史取回来，避免整段重复写入
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
      void SessionAppend(root, "main", m.role, m.content)
        .then((seq: number) => {
          if (typeof seq !== "number" || seq <= 0) {
            persistedRef.current.delete(uid); // 后端没给出序号，允许再试一次
            return;
          }
          persistedRef.current.set(uid, "done");
          setMessages((prev) => {
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
  }, [messages, project.activeProject?.rootPath]);

  useEffect(() => {
    const off = Events.On("ai:model-stream", (evt: any) => {
      if (!aiRunSourceRef.current || aiRunSourceRef.current === "local") return;
      const data = evt?.data ?? evt;
      const phase = String(data?.phase || "token");
      const content = String(data?.content || "");
      setMessages((current) => {
        if (phase === "start" || phase === "reset") {
          return current.filter((message) => !message.streaming);
        }
        if (phase !== "token" || !content) return current;
        const next = [...current];
        let index = -1;
        for (let i = next.length - 1; i >= 0; i--) {
          if (next[i].streaming) { index = i; break; }
        }
        if (index >= 0) next[index] = { ...next[index], content: next[index].content + content };
        else next.push({ role: "assistant", content, streaming: true });
        return next;
      });
    });
    return () => { off(); };
  }, []);

  useEffect(() => {
    const off = Events.On("ai:provider", (evt: any) => {
      const data = evt?.data ?? evt;
      const provider = data?.provider || "模型";
      const detail = data?.detail || "";
      const status = data?.status || "attempt";
      if (status === "success") {
        pushAIActivity("thinking", "正在整理回复", "模型已返回内容，正在转换为聊天消息");
      } else if (status === "error") {
        pushAIActivity("thinking", "正在切换可用模型", detail || `当前通道 ${provider} 没有可用回复，继续尝试下一个`);
      } else if (status === "skip") {
        pushAIActivity("thinking", "继续寻找可用模型", `${provider} 暂不可用，跳过并检查后续通道`);
      } else {
        pushAIActivity("thinking", "正在等待模型推理", `已把当前请求和项目上下文交给 ${provider}`);
      }
    });
    return () => { off(); };
  }, [pushAIActivity]);

  const displayPath = useCallback((filePath: string) => {
    const root = project.activeProject?.rootPath;
    if (root && filePath.startsWith(root + "/")) {
      return filePath.slice(root.length + 1);
    }
    return filePath;
  }, [project.activeProject?.rootPath]);

  const quoteContext = (value: string) => JSON.stringify(value);

  const appendAIContext = useCallback((text: string) => {
    if (ui.appMode !== "ai") return;
    setAiContextSignal((prev) => ({ id: (prev?.id ?? 0) + 1, text }));
  }, [ui.appMode]);

  const appendFileContext = useCallback((filePath: string) => {
    appendAIContext(`@file(${quoteContext(displayPath(filePath))})`);
  }, [appendAIContext, displayPath]);

  const appendSymbolContext = useCallback((ctx: { name: string; filePath: string; line: number; detail?: string }) => {
    const detail = ctx.detail ? ` ${ctx.detail}` : "";
    appendAIContext(`@symbol(${quoteContext(displayPath(ctx.filePath))}:${ctx.line} ${quoteContext(ctx.name)}${detail})`);
  }, [appendAIContext, displayPath]);

  // ── Keyboard shortcuts for AI Changes navigation ─────────────────────────
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Only handle when changes panel is open
      if (ui.activeToolTab !== "changes" || !ui.toolPanelOpen) return;

      // Check for Ctrl/Cmd + arrow keys
      if (e.ctrlKey || e.metaKey) {
        if (e.key === "ArrowLeft") {
          e.preventDefault();
          const success = ui.navigateToPrevFileModification();
          if (success) {
            // Refresh the tasks to update the UI
            Task.GetAgentTasks().then((tasks: any[]) => {
              project.clearAgentTasks();
              tasks.forEach((t: any) => project.addAgentTask(t));
            }).catch(console.error);
          }
        } else if (e.key === "ArrowRight") {
          e.preventDefault();
          const success = ui.navigateToNextFileModification();
          if (success) {
            // Refresh the tasks to update the UI
            Task.GetAgentTasks().then((tasks: any[]) => {
              project.clearAgentTasks();
              tasks.forEach((t: any) => project.addAgentTask(t));
            }).catch(console.error);
          }
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [ui, project]);

  // ── Initialization ───────────────────────────────────────────────────────
  useEffect(() => {
    StartAndGetPort().then(ui.setTerminalPort).catch(console.error);
    StartLspPort().then(ui.setLspPort).catch(console.error);
    project.loadPersistedProjects();
  }, []);

  // Listen for filetree:refresh events from backend (emitted after AI write operations)
  useEffect(() => {
    const off = Events.On("filetree:refresh", async (rootPath: any) => {
      const projId = typeof rootPath === "string" ? rootPath : project.activeProject?.id;
      if (projId) project.refreshProject(projId);
      // Reload content of any open editor tabs that match the project root
      const openTabs = editor.tabs;
      for (const tab of openTabs) {
        if (!tab.path.startsWith("remote://")) {
          try {
            const content = await ReadFile(tab.path);
            editor.reloadTabContent(tab.path, content ?? "");
          } catch { /* file may have been deleted */ }
        }
      }
    });
    return () => { Events.Off("filetree:refresh"); };
  }, [project, editor]);

  useEffect(() => {
    loadInstalledExtensions();
  }, []);

  // Sync analysis content with active tab
  useEffect(() => {
    const activeTabData = editor.tabs.find((t) => t.path === editor.activeTab);
    setAnalysisContent(activeTabData?.content ?? null);
  }, [editor.activeTab, editor.tabs]);

  // ── Extension management ─────────────────────────────────────────────────
  const loadInstalledExtensions = useCallback(async () => {
    try {
      const list = await GetInstalledExtensions();
      const withIcons = await Promise.all(
        list.map(async (ext: ExtType) => {
          const iconUrl = await GetExtensionIcon(ext.publisher, ext.technicalName).catch(() => "");
          return { ...ext, iconUrl, installing: false };
        })
      );
      setInstalledExtensions(withIcons);
      if (monacoGlobalRef.current) {
        for (const ext of list) {
          GetExtensionContributes(ext.publisher, ext.technicalName)
            .then((contrib: ExtensionContributes | null) => contrib && applyExtensionContributes(monacoGlobalRef.current, contrib))
            .catch(() => {});
        }
      }
    } catch (err) {
      console.error("load installed extensions:", err);
    }
  }, []);

  const doSearch = useCallback(async (query: string) => {
    setExtensionLoading(true);
    try {
      const results = await SearchExtensions(query);
      setExtensions(results);
    } catch (err) {
      console.error("搜索扩展失败:", err);
    }
    setExtensionLoading(false);
  }, []);

  const handleExtensionSearchChange = useCallback((value: string) => {
    setExtensionSearch(value);
    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(() => doSearch(value.trim()), 400);
  }, [doSearch]);

  const handleExtDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    setExtDragOver(false);
    const files = Array.from(e.dataTransfer.files).filter((f) => f.name.endsWith(".vsix"));
    for (const file of files) {
      const filePath = (file as any).path as string;
      if (!filePath) continue;
      await InstallLocalVSIX(filePath).catch(console.error);
    }
    await loadInstalledExtensions();
  }, [loadInstalledExtensions]);

  const handleUninstallExtension = useCallback(async (ext: Extension) => {
    await UninstallExtension(ext.publisher, ext.technicalName).catch(console.error);
    await loadInstalledExtensions();
  }, [loadInstalledExtensions]);

  // ── Resize handlers ──────────────────────────────────────────────────────
  const handleSidebarResize = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    const start = e.clientX, startW = ui.sidebarWidth;
    const onMove = (ev: MouseEvent) => ui.setSidebarWidth(Math.max(160, Math.min(500, startW + (ev.clientX - start))));
    const onUp = () => { document.removeEventListener("mousemove", onMove); document.removeEventListener("mouseup", onUp); };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [ui.sidebarWidth]);

  const handleToolPanelResize = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    const start = e.clientX, startW = ui.toolPanelWidth;
    const onMove = (ev: MouseEvent) => ui.setToolPanelWidth(Math.max(240, Math.min(600, startW - (ev.clientX - start))));
    const onUp = () => { document.removeEventListener("mousemove", onMove); document.removeEventListener("mouseup", onUp); };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [ui.toolPanelWidth]);

  // ── File operations ──────────────────────────────────────────────────────
  const openFileAtLine = useCallback(async (filePath: string, line: number) => {
    try {
      const existing = editor.tabs.find((t) => t.path === filePath);
      if (!existing) {
        const content = await ReadFile(filePath);
        const name = filePath.split("/").pop() ?? filePath;
        editor.openTab({ path: filePath, name, content, isDirty: false });
      } else {
        editor.setActiveTab(filePath);
      }
      setRevealSignal({ path: filePath, ts: Date.now() });
      setTimeout(() => {
        if (editorInstanceRef.current && line > 0) {
          editorInstanceRef.current.revealLineInCenter(line);
          editorInstanceRef.current.setPosition({ lineNumber: line, column: 1 });
          editorInstanceRef.current.focus();
        }
      }, 80);
    } catch (err) { console.error("openFileAtLine:", err); }
  }, [editor]);

  const openFile = useCallback(async (node: FileNode) => {
    if (node.isDir) return;

    if (ui.appMode === "ai") {
      appendFileContext(node.path);
    }

    if (openedPathsRef.current.has(node.path)) { editor.setActiveTab(node.path); return; }
    const ext = extOf(node.path);
    openedPathsRef.current.add(node.path);

    if (BINARY_EXTS.has(ext)) {
      editor.openTab({ path: node.path, name: node.name, content: "", isDirty: false });
      ui.setActiveToolTab("analysis");
      ui.setToolPanelOpen(true);
      return;
    }
    try {
      const content = await ReadFile(node.path);
      editor.openTab({ path: node.path, name: node.name, content, isDirty: false });
      if (ANALYSIS_EXTS.has(ext)) {
        ui.setActiveToolTab("analysis");
        ui.setToolPanelOpen(true);
      }
      const rec = PLUGIN_RECOMMENDATIONS[ext];
      if (rec && !dismissedRecs.current.has(ext) && !installedExtensions.some((e) => e.id === rec.id)) {
        setPluginRec({ ...rec, ext });
      } else {
        setPluginRec(null);
      }
    } catch (err) {
      openedPathsRef.current.delete(node.path);
      console.error("open:", err);
    }
  }, [editor, ui, installedExtensions, appendFileContext]);

  const handleSave = useCallback(async (path: string) => {
    const tab = editor.tabs.find((t) => t.path === path);
    if (!tab) return;
    await WriteFile(path, tab.content);
    editor.markTabSaved(path);
    if (project.activeProject) {
      const gitStatus = await GetStatus(project.activeProject.rootPath).catch(() => null);
      if (gitStatus) {
        project.refreshProject(project.activeProject.id);
      }
    }
  }, [editor, project]);

  // ── Extension install flow ───────────────────────────────────────────────
  const runExtensionInstalls = useCallback(async (extensionIds: string[], setupType?: string, dirPath?: string) => {
    if (extensionIds.length === 0 && !setupType) return;
    const tasks: InstallTask[] = extensionIds.map((id) => ({
      id, displayName: id.split(".").pop() ?? id, status: "pending",
    }));
    if (setupType && dirPath) tasks.push({ id: "__setup__", displayName: `运行 ${setupType} 依赖安装`, status: "pending" });
    setInstallTasks(tasks);
    ui.setShowProgress(true);

    for (const id of extensionIds) {
      if (installedExtensions.some((e) => e.id === id)) {
        setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "done" } : t));
        continue;
      }
      setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "installing" } : t));
      const parts = id.split(".");
      try {
        await InstallExtension(parts[0], parts.slice(1).join("."));
        setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "done" } : t));
      } catch {
        setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "error" } : t));
      }
    }

    if (setupType && dirPath) {
      setInstallTasks((p) => p.map((t) => t.id === "__setup__" ? { ...t, status: "installing" } : t));
      try {
        await RunSetup(dirPath, setupType);
        setInstallTasks((p) => p.map((t) => t.id === "__setup__" ? { ...t, status: "done" } : t));
      } catch {
        setInstallTasks((p) => p.map((t) => t.id === "__setup__" ? { ...t, status: "error" } : t));
      }
    }
    await loadInstalledExtensions();
  }, [installedExtensions, ui, loadInstalledExtensions]);

  // ── Git ──────────────────────────────────────────────────────────────────
  const handleInitRepo = useCallback(async () => {
    if (!project.activeProject) return;
    await InitRepo(project.activeProject.rootPath);
    await project.refreshProject(project.activeProject.id);
  }, [project]);

  const handleCommit = useCallback(async (message: string) => {
    if (!project.activeProject) return;
    const hash = await Commit(project.activeProject.rootPath, message, "", "");
    await project.refreshProject(project.activeProject.id);
    setMessages((p) => [...p, { role: "system", content: `已提交: ${hash.slice(0, 8)} — ${message}` }]);
  }, [project]);

  // ── Titlebar ─────────────────────────────────────────────────────────────
  const activeTabData = editor.tabs.find((t) => t.path === editor.activeTab);
  const { t, i18n, toggleLang } = useTranslation();

  const titlebarText = editor.activeTab
    ? (() => {
        const proj = project.projects.find((p) => editor.activeTab!.startsWith(p.rootPath + "/"));
        if (proj) {
          const rel = editor.activeTab!.slice(proj.rootPath.length + 1);
          const crumb = [proj.name, ...rel.split("/")].join(" › ");
          return `${crumb}${activeTabData?.isDirty ? " ●" : ""}`;
        }
        return `${activeTabData?.name ?? editor.activeTab}${activeTabData?.isDirty ? " ●" : ""}`;
      })()
    : t("app.title");

  // ── Active file lang for Outline / CallHierarchy ─────────────────────────
  const activeFileLang = editor.activeTab ? langOf(editor.activeTab) : null;

  // ── Render ───────────────────────────────────────────────────────────────
  return (
    <WindowRouter>
    <div className="ide-root">
      {/* Titlebar */}
      <div className="titlebar">
        <div className="titlebar-section" />
        <div className="titlebar-center">
          <span className="titlebar-title">{titlebarText}</span>
        </div>
        <div className="titlebar-section titlebar-section-right">
          {/* Model indicator - shows current AI model/router status */}
          <ModelStatusIndicator />
          <div className="mode-switch" title={ui.appMode === "editor" ? t("mode.switchToAi") : t("mode.switchToEditor")}>
            <button
              className={`switch-track${ui.appMode === "ai" ? " switch-on" : ""}`}
              onClick={() => ui.setAppMode(ui.appMode === "editor" ? "ai" : "editor")}
            >
              <span className="switch-thumb" />
            </button>
          </div>
          <button className="titlebar-icon-btn" onClick={toggleLang} title={i18n.language === "zh" ? t("langSwitch.toEn") : t("langSwitch.toZh")}>
            <Globe size={13} />
          </button>
        </div>
      </div>

      <div className="workspace">
        {/* Activity Bar */}
        <div className="activity-bar">
          <div className="activity-bar-top">
            {(["projects", "search", "git", "database", "testing", "deploy", "extensions", "remote"] as SidebarTab[]).map((tab) => {
              const icons: Record<SidebarTab, React.ReactNode> = {
                projects: <Files size={20} strokeWidth={1.5} />,
                search: <Search size={20} strokeWidth={1.5} />,
                git: <>{<GitBranch size={20} strokeWidth={1.5} />}{project.activeProject?.gitStatus?.isDirty && <span className="activity-badge" />}</>,
                database: <Database size={20} strokeWidth={1.5} />,
                testing: <FlaskConical size={20} strokeWidth={1.5} />,
                deploy: <Rocket size={20} strokeWidth={1.5} />,
                extensions: <Puzzle size={20} strokeWidth={1.5} />,
                outline: <ListTree size={20} strokeWidth={1.5} />,
                callhierarchy: <Network size={20} strokeWidth={1.5} />,
                remote: <Server size={20} strokeWidth={1.5} />,
              };
              const titles: Record<SidebarTab, string> = {
                projects: t("sidebar.projects"), search: t("sidebar.search"), git: t("sidebar.git"),
                database: t("sidebar.database"), testing: t("sidebar.testing"), deploy: t("sidebar.deploy"),
                extensions: t("sidebar.extensions"), outline: t("sidebar.outline"), callhierarchy: t("sidebar.callhierarchy"),
                remote: t("sidebar.remote"),
              };
              return (
                <button
                  key={tab}
                  className={`activity-item${ui.sidebarOpen && ui.sidebarTab === tab ? " active" : ""}`}
                  onClick={() => ui.handleActivityClick(tab)}
                  title={titles[tab]}
                >
                  {icons[tab]}
                </button>
              );
            })}
            <button
              className={`activity-item${ui.toolPanelOpen ? " active" : ""}`}
              onClick={() => ui.setToolPanelOpen(!ui.toolPanelOpen)}
              title={t("tool.panel")}
            >
              <Wrench size={20} strokeWidth={1.5} />
            </button>
          </div>
          <div className="activity-bar-bottom">
            <button className="activity-item" title={t("action.settings")} onClick={() => setShowSettings(true)}>
              <Settings size={20} strokeWidth={1.5} />
            </button>
          </div>
        </div>

        {/* Sidebar */}
        <div className={`sidebar${ui.sidebarOpen ? "" : " sidebar-collapsed"}`} style={{ width: ui.sidebarOpen ? ui.sidebarWidth : 0 }}>

          {/* Projects */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "projects" ? "contents" : "none" }}>
            <div className="sidebar-header">
              <span className="sidebar-header-path" title={project.activeProject?.rootPath}>
                {project.activeProject ? project.activeProject.name : t("sidebar.projects")}
              </span>
              <div style={{ display: "flex", gap: 2 }}>
                {project.activeProject && (
                  <button className="sidebar-header-btn sidebar-header-btn-always"
                    onClick={() => project.refreshProject(project.activeProject!.id)} title={t("action.refresh")}>
                    <RefreshCw size={12} />
                  </button>
                )}
                <button className="sidebar-header-btn sidebar-header-btn-always"
                  onClick={() => editor.activeTab && setRevealSignal({ path: editor.activeTab, ts: Date.now() })}
                  title={t("tool.locateInTree")}
                  style={{ opacity: editor.activeTab ? 1 : 0.35 }}>
                  <LocateFixed size={12} />
                </button>
                {editor.activeTab && activeTabData?.isDirty && (
                  <button className="sidebar-header-btn sidebar-header-btn-always"
                    onClick={() => handleSave(editor.activeTab!)} title={t("action.saveFile")}>
                    <Save size={12} />
                  </button>
                )}
                <button className="sidebar-header-btn sidebar-header-btn-always"
                  onClick={() => ui.setShowWizard(true)} title={t("action.importProject")}>
                  <FolderPlus size={12} />
                </button>
              </div>
            </div>
            <FileTree
              projects={project.projects}
              activeProjectId={project.activeProjectId}
              activeFile={editor.activeTab}
              revealSignal={revealSignal}
              onFileClick={openFile}
              onProjectActivate={project.activateProject}
              onProjectRemove={project.removeProject}
              onProjectTypeChange={project.setProjectType}
              onProjectRename={project.renameProject}
              onFileRenamed={(oldPath, newPath) => {
                const name = newPath.split("/").pop() ?? newPath;
                editor.renameTab(oldPath, newPath, name);
                const proj = project.projects.find((p) => oldPath.startsWith(p.rootPath + "/"));
                if (proj) project.refreshProject(proj.id);
              }}
              onFileDeleted={(path) => {
                editor.deleteTabsByPrefix(path);
                const proj = project.projects.find((p) => path.startsWith(p.rootPath + "/"));
                if (proj) project.refreshProject(proj.id);
              }}
              onFileCreated={(path) => {
                const proj = project.projects.find((p) => path.startsWith(p.rootPath + "/"));
                if (proj) project.refreshProject(proj.id);
              }}
              onConfigureRunEnv={(path, name) => {
                const proj = project.projects.find((p) => path.startsWith(p.rootPath));
                setRunEnvTarget({ path, name, projectId: proj?.id });
              }}
            />
            {project.activeProject && (
              <div className="sidebar-footer">
                <button
                  className="sidebar-footer-btn"
                  onClick={() => project.activeProject && setRunEnvTarget({ path: project.activeProject.rootPath, name: project.activeProject.name, projectId: project.activeProject.id })}
                >
                  <Wrench size={12} />
                  <span>{project.activeProject.type ?? t("tool.configEnv")}</span>
                </button>
                <button
                  className="sidebar-footer-btn"
                  onClick={() => { ui.setToolPanelOpen(true); ui.setActiveToolTab("changes"); }}
                >
                  <History size={12} />
                  <span>AI 修改</span>
                </button>
              </div>
            )}
          </div>

          {/* Search */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "search" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <div className="sidebar-header"><span className="sidebar-header-path">{t("sidebar.search")}</span></div>
            <SearchPanel rootPath={project.activeProject?.rootPath ?? null} projects={project.projects} onOpenFile={openFileAtLine} />
          </div>

          {/* Git */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "git" ? "contents" : "none" }}>
            <GitPanel
              projects={project.projects}
              activeProjectId={project.activeProjectId}
              status={project.activeProject?.gitStatus ?? null}
              workingDir={project.activeProject?.rootPath ?? null}
              onProjectChange={project.activateProject}
              onInitRepo={handleInitRepo}
              onStageAll={async () => { if (project.activeProject) await StageAll(project.activeProject.rootPath); }}
              onCommit={handleCommit}
              onPush={async () => { if (project.activeProject) await Push(project.activeProject.rootPath); }}
              onRefresh={async () => { if (project.activeProject) await project.refreshProject(project.activeProject.id); }}
              onCommitClick={(commit) => {
                if (!project.activeProject) return;
                const tabPath = `__commit_detail__:${project.activeProject.rootPath}:${commit.hash}`;
                if (!editor.tabs.find((t) => t.path === tabPath)) {
                  editor.openTab({ path: tabPath, name: `${commit.hash.slice(0, 7)} ${commit.message.split("\n")[0].slice(0, 30)}`, content: "", isDirty: false });
                }
                editor.setActiveTab(tabPath);
              }}
              onOpenFile={(relPath) => {
                if (!project.activeProject) return;
                if (relPath.startsWith("__commit_diff__:")) {
                  const parts = relPath.split(":");
                  if (parts.length >= 3) {
                    const hash = parts[1], filePath = parts.slice(2).join(":");
                    const fileName = filePath.split("/").pop() ?? filePath;
                    const tabPath = `__commit_diff__:${hash}:${filePath}`;
                    if (!editor.tabs.find((t) => t.path === tabPath))
                      editor.openTab({ path: tabPath, name: `${fileName} (${hash.substring(0, 7)})`, content: "", isDirty: false });
                    editor.setActiveTab(tabPath);
                  }
                  return;
                }
                const diffPath = `__diff__:${project.activeProject.rootPath}:${relPath}`;
                const fileName = relPath.split("/").pop() ?? relPath;
                if (!editor.tabs.find((t) => t.path === diffPath))
                  editor.openTab({ path: diffPath, name: `${fileName} (${t("tool.compare")})`, content: "", isDirty: false });
                editor.setActiveTab(diffPath);
              }}
            />
          </div>

          {/* Remote */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "remote" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <div className="sidebar-header"><span className="sidebar-header-path">{t("remote.title")}</span></div>
            <RemotePanel onOpenRemoteFile={async (connID, path, type) => {
              try {
                const content = type === "docker" ? await DockerReadFile(connID, path) : await RemoteReadFile(connID, path);
                const name = path.split("/").pop() ?? path;
                editor.openTab({ path: `remote://${connID}/${path}`, name, content: content ?? "", isDirty: false });
              } catch (e: any) {
                console.error("open remote file:", e);
              }
            }} />
          </div>

          {/* Database */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "database" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <DatabasePanel />
          </div>

          {/* Deploy */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "deploy" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <DeployPanel onOpenDeploy={() => {
              const path = "__deploy__";
              if (!editor.tabs.find((t) => t.path === path))
                editor.openTab({ path, name: t("deploy.console"), content: "", isDirty: false });
              editor.setActiveTab(path);
            }} />
          </div>

          {/* Testing */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "testing" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <TestingPanel
              onOpenApiTest={() => {
                const path = "__api_test__";
                if (!editor.tabs.find((t) => t.path === path))
                  editor.openTab({ path, name: t("testing.apiName"), content: "", isDirty: false });
                editor.setActiveTab(path);
              }}
              onOpenPlaywright={() => {
                const path = "__playwright__";
                if (!editor.tabs.find((t) => t.path === path))
                  editor.openTab({ path, name: "Playwright", content: "", isDirty: false });
                editor.setActiveTab(path);
              }}
            />
          </div>

          {/* Extensions */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "extensions" ? "contents" : "none" }}>
            <div className="sidebar-header"><span className="sidebar-header-path">{t("extension.title")}</span></div>
            <div className="extension-panel-sidebar">
              <div className="ext-tab-bar">
                <button className={`ext-tab${extensionTab === "marketplace" ? " active" : ""}`}
                  onClick={() => setExtensionTab("marketplace")}>
                  <Store size={12} />{t("extension.marketplace")}
                </button>
                <button className={`ext-tab${extensionTab === "installed" ? " active" : ""}`}
                  onClick={() => { setExtensionTab("installed"); loadInstalledExtensions(); }}>
                  <PackageCheck size={12} />{t("extension.installed")}
                  {installedExtensions.length > 0 && <span className="ext-tab-badge">{installedExtensions.length}</span>}
                </button>
              </div>

              {extensionTab === "marketplace" && (
                <>
                  <div className="extension-search">
                    <Search size={14} />
                    <input type="text" placeholder={t("extension.searchPlaceholder")}
                      value={extensionSearch} onChange={(e) => handleExtensionSearchChange(e.target.value)} />
                  </div>
                  <div className="extension-list">
                    {extensionLoading ? (
                      <div className="extension-loading">{t("extension.searching")}</div>
                    ) : extensions.length === 0 ? (
                      <div className="extension-empty">
                        <Puzzle size={48} strokeWidth={1} />
                        <span>{t("extension.emptyTitle")}</span>
                        <p>{t("extension.emptyDesc")}</p>
                      </div>
                    ) : extensions.filter((ext) =>
                      ext.name.toLowerCase().includes(extensionSearch.toLowerCase()) ||
                      ext.publisher.toLowerCase().includes(extensionSearch.toLowerCase())
                    ).map((ext) => (
                      <div key={ext.id} className="extension-item">
                        <div className="extension-icon">
                          {ext.iconUrl ? <img src={ext.iconUrl} alt={ext.name} className="ext-icon-img" /> : <Puzzle size={24} />}
                        </div>
                        <div className="extension-info">
                          <div className="extension-name">{ext.name}{ext.limited && <span className="ext-limited-badge" title={t("tool.limited")}>⚠ {t("tool.limited")}</span>}</div>
                          <div className="extension-publisher">{ext.publisher} · v{ext.version}</div>
                          <div className="extension-desc">{ext.description}</div>
                        </div>
                        <button className={`extension-btn${ext.installed ? " installed" : ""}`} disabled={ext.installing}
                          onClick={async () => {
                            if (ext.installed) return;
                            setExtensions((p) => p.map((e) => e.id === ext.id ? { ...e, installing: true } : e));
                            let ok = false;
                            try { await InstallExtension(ext.publisher, ext.technicalName); ok = true; }
                            catch (err) { alert(`${t("extension.installFailed")}：${err}`); }
                            finally {
                              setExtensions((p) => p.map((e) => e.id === ext.id ? { ...e, installing: false, installed: ok } : e));
                              if (ok) await loadInstalledExtensions();
                            }
                          }}>
                          {ext.installing ? t("extension.installing") : ext.installed ? t("extension.installedBtn") : <Download size={14} />}
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}

              {extensionTab === "installed" && (
                <>
                  <div className={`ext-drop-zone${extDragOver ? " drag-over" : ""}`}
                    onDragOver={(e) => { e.preventDefault(); setExtDragOver(true); }}
                    onDragLeave={() => setExtDragOver(false)} onDrop={handleExtDrop}>
                    <Download size={16} /><span>{t("extension.dropHint")}</span>
                  </div>
                  <div className="extension-list">
                    {installedExtensions.length === 0 ? (
                      <div className="extension-empty">
                        <PackageCheck size={48} strokeWidth={1} />
                        <span>{t("extension.noInstalled")}</span>
                      </div>
                    ) : installedExtensions.map((ext) => (
                      <div key={ext.id} className="extension-item" onClick={() => setExtensionDetailExt(ext)} style={{ cursor: "pointer" }}>
                        <div className="extension-icon">
                          {ext.iconUrl ? <img src={ext.iconUrl} alt={ext.name} className="ext-icon-img" /> : <Puzzle size={24} />}
                        </div>
                        <div className="extension-info">
                          <div className="extension-name">{ext.name}</div>
                          <div className="extension-publisher">{ext.publisher} · v{ext.version}</div>
                          <div className="extension-desc">{ext.description}</div>
                        </div>
                        <button className="extension-btn extension-btn-uninstall" title={t("extension.uninstall")}
                          onClick={(e) => { e.stopPropagation(); handleUninstallExtension(ext); }}>
                          <Trash2 size={14} />
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
          </div>
        </div>

        {ui.sidebarOpen && <div className="resize-handle" onMouseDown={handleSidebarResize} />}

        {/* Main editor area */}
        {ui.appMode === "editor" ? (
          <SplitEditorArea
            lspPort={ui.lspPort || undefined}
            rootPath={project.activeProject?.rootPath}
            onMonacoMount={(m) => { monacoGlobalRef.current = m; setMonacoInstance(m); }}
            onEditorMount={(e) => { editorInstanceRef.current = e; }}
            breakpoints={breakpoints}
            onBreakpointToggle={(file, line) => {
              setBreakpoints((p) => {
                const exists = p.some((bp) => bp.file === file && bp.line === line);
                return exists
                  ? p.filter((bp) => !(bp.file === file && bp.line === line))
                  : [...p, { file, line, enabled: true }];
              });
            }}
            pausedAt={debugPausedAt}
            onEvaluateExpr={(expr) => debugPanelRef.current?.evaluate(expr) ?? Promise.resolve(null)}
            onCursorChange={(line, col) => editor.setCursorInfo(line, col)}
            onSave={handleSave}
            renderSpecial={(path) => {
              if (path === "__api_test__") return <ApiTestPanel />;
              if (path === "__playwright__") return <PlaywrightPanel />;
              if (path === "__deploy__") return <DeployWorkspace />;
              if (path.startsWith("__commit_diff__:")) {
                const parts = path.split(":");
                if (parts.length >= 3 && project.activeProject) {
                  return <CommitFileDiffViewer repoPath={project.activeProject.rootPath} commitHash={parts[1]} filePath={parts.slice(2).join(":")} />;
                }
              }
              if (path.startsWith("__diff__:")) {
                const [, repoPath, ...rest] = path.split(":");
                return <GitDiffViewer repoPath={repoPath} relPath={rest.join(":")} />;
              }
              if (path.startsWith("__commit_detail__:")) {
                const [, repoPath, hash] = path.split(":");
                const tab = editor.tabs.find((t) => t.path === path);
                const commit = { hash, message: tab?.name?.split(" ").slice(1).join(" ") ?? "", author: "", when: "" };
                return <GitCommitDetail repoPath={repoPath} commit={commit} onRefresh={() => project.activeProject && project.refreshProject(project.activeProject.id)} />;
              }
              return null;
            }}
            pluginRecBanner={pluginRec ? (
              <div className="plugin-rec-banner">
                <span className="plugin-rec-icon">🔌</span>
                <span className="plugin-rec-text">
                  {t("extension.recDetected")} <strong>.{pluginRec.ext}</strong> {t("extension.recInstall")} <strong>{pluginRec.name}</strong>
                </span>
                <button className="plugin-rec-btn plugin-rec-install"
                  onClick={async () => { setPluginRec(null); dismissedRecs.current.add(pluginRec.ext); await runExtensionInstalls([pluginRec.id]); }}>
                  {t("extension.installBtn")}
                </button>
                <button className="plugin-rec-btn plugin-rec-dismiss"
                  onClick={() => { dismissedRecs.current.add(pluginRec.ext); setPluginRec(null); }}>
                  {t("extension.ignore")}
                </button>
              </div>
            ) : null}
          />
        ) : (
          <AIPanel
            messages={messages}
            contextToAppend={aiContextSignal}
            projectRoot={project.activeProject?.rootPath ?? ""}
            onRollback={() => { void reloadSessionMessages(project.activeProject?.rootPath ?? "", true); }}
            onSend={async (text, source: ModelSource) => {
            if (text === "__clear__") {
              // Cleanup any active stream listener
              if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
              setMessages([]);
              setAiActivityItems([]);
              return;
            }
            const requestText = withConversationContext(text, messages);
            aiStopRequestedRef.current = false;
            setMessages((p) => [
              ...p,
              { role: "user", content: text },
            ]);
            setAiLoading(true);
            setAiActivityItems([]);
            aiRunSourceRef.current = source;

            const rootPath = project.activeProject?.rootPath ?? "";
            pushAIActivity("thinking", "准备上下文", source === "auto" ? "Auto 将按可用模型自动尝试，并把项目路径作为上下文" : `使用 ${source} 模式处理当前请求`);

            // Auto mode: use AgentV2 with SmartRouter (enhanced with rollback, parallel execution, MCP)
            if (source === "auto") {
              try {
                pushAIActivity("route", "选择 Auto 路由", rootPath ? "结合当前项目目录和聊天输入执行" : "当前没有项目目录，仅使用聊天输入执行");
                const result = await SmartAgentRunV2(requestText, rootPath, 20);
                if (aiStopRequestedRef.current) return;
                if (result.taskAnalysis) {
                  pushAIActivity("thinking", "正在组织回复", "已完成任务类型判断，正在把模型输出整理为最终回答");
                }
                const agentMsgs = normalizeAgentMessages(result.messages ?? [], pushAIActivity);
                // Add task analysis info if available
                if (result.taskAnalysis) {
                  console.log("🧠 AgentV2 任务分析:", result.taskAnalysis);
                }
                if (agentMsgs.length > 0) {
                  setMessages((p) => [...p.filter((message) => !message.streaming), ...agentMsgs]);
                  pushAIActivity(result.error ? "error" : "success", result.error ? "执行结束但有错误" : "已生成回复");
                } else {
                  setMessages((p) => [...p.filter((message) => !message.streaming), {
                    role: "assistant",
                    content: result.error ? `❌ ${result.error}` : "(no response)",
                  }]);
                  pushAIActivity(result.error ? "error" : "success", result.error ? "执行失败" : "返回空响应");
                }
              } catch (err: any) {
                if (aiStopRequestedRef.current) return;
                pushAIActivity("error", "请求失败", err?.message ?? String(err));
                setMessages((p) => [...p.filter((message) => !message.streaming), { role: "assistant", content: `❌ AgentV2 失败: ${err?.message ?? err}` }]);
              }
              setAiLoading(false);
              return;
            }

            // Use streaming for local mode (LM Studio supports ChatStream)
            if (source === "local") {
              // Remove previous listener if any
              if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
              pushAIActivity("route", "连接本地模型", "等待流式响应");

              const handler = (evt: any) => {
                const data = evt?.data ?? evt;
                const eType = data?.type ?? "";
                const eContent = data?.content ?? "";
                const eName = data?.name ?? "";
                const eThinking = data?.thinking ?? "";
                switch (eType) {
                  case "token":
                    setMessages((p) => {
                      const updated = [...p];
                      const last = updated[updated.length - 1];
                      if (last && last.role === "assistant") {
                        updated[updated.length - 1] = { ...last, content: last.content + eContent };
                      } else if (eContent) {
                        updated.push({ role: "assistant", content: eContent });
                      }
                      return updated;
                    });
                    break;
                  case "tool_call":
                    if (eThinking.trim()) {
                      pushAIActivity("thinking", "思考过程", eThinking);
                    }
                    pushAIActivity("tool", describeTool(eName || "tool", eContent).title, eContent, eName || "tool");
                    setMessages((p) => {
                      const updated = [...p];
                      const last = updated[updated.length - 1];
                      // Convert last assistant message to thinking with backend-extracted content
                      if (last && last.role === "assistant") {
                        const thinkContent = eThinking || last.content;
                        updated[updated.length - 1] = {
                          ...last,
                          role: "thinking",
                          content: thinkContent.trim() ? thinkContent : last.content,
                        };
                      }
                      return updated;
                    });
                    break;
                  case "tool_result":
                    pushAIActivity("result", `工具完成 ${eName || "tool"}`, eContent, eName || "tool");
                    break;
                  case "permission_request":
                    pushAIActivity("tool", `等待授权 ${eName || "tool"}`, data?.reason ?? "", eName || "tool");
                    setPermRequest({
                      id: data?.id ?? "",
                      toolName: eName,
                      args: eContent,
                      reason: data?.reason ?? "",
                    });
                    break;
                  case "done":
                    pushAIActivity("success", "任务完成");
                    setMessages((p) => {
                      const updated = [...p];
                      const last = updated[updated.length - 1];
                      if (last && last.role === "assistant" && !last.content.trim()) {
                        updated.pop();
                      } else if ((!last || last.role !== "assistant") && eContent.trim()) {
                        updated.push({ role: "assistant", content: eContent });
                      }
                      return updated;
                    });
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                  case "stream_end":
                    pushAIActivity("success", "响应结束");
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                  case "error":
                    if (aiStopRequestedRef.current) break;
                    pushAIActivity("error", "执行失败", eContent);
                    setMessages((p) => [...p, { role: "assistant", content: `❌ ${eContent}` }]);
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                  case "cancelled":
                    pushAIActivity("success", "已停止生成");
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                }
              };

              Events.On("agent:stream", handler);
              streamRef.current = handler;

              try {
                await StartAgentStream(requestText, rootPath, 20);
              } catch (err: any) {
                if (aiStopRequestedRef.current) return;
                Events.Off("agent:stream");
                streamRef.current = null;
                pushAIActivity("error", "启动流失败", err?.message ?? String(err));
                setMessages((p) => [...p.filter((message) => !message.streaming), { role: "assistant", content: `❌ AI request failed: ${err?.message ?? err}` }]);
                setAiLoading(false);
              }
            } else {
              // API/Web calls emit unified model-stream events while AgentRun manages tools.
              try {
                let result;
                if (source === "web") {
                  pushAIActivity("route", "使用 Web AI", "等待网页登录态响应");
                  result = await AgentRunWeb(requestText, rootPath, 20);
                } else {
                  pushAIActivity("route", "使用 API", "等待接口响应");
                  result = await AgentRunAPI(requestText, rootPath, 20);
                }
                if (aiStopRequestedRef.current) return;
                const agentMsgs = normalizeAgentMessages(result.messages ?? [], pushAIActivity);
                if (agentMsgs.length > 0) {
                  setMessages((p) => [...p.filter((message) => !message.streaming), ...agentMsgs]);
                  pushAIActivity(result.error ? "error" : "success", result.error ? "执行结束但有错误" : "已生成回复");
                } else {
                  setMessages((p) => [...p.filter((message) => !message.streaming), {
                    role: "assistant",
                    content: result.error ? `❌ ${result.error}` : "(no response)",
                  }]);
                  pushAIActivity(result.error ? "error" : "success", result.error ? "执行失败" : "返回空响应");
                }
              } catch (err: any) {
                if (aiStopRequestedRef.current) return;
                pushAIActivity("error", "请求失败", err?.message ?? String(err));
                setMessages((p) => [...p.filter((message) => !message.streaming), { role: "assistant", content: `❌ AI request failed: ${err?.message ?? err}` }]);
              } finally {
                setAiLoading(false);
              }
            }
          }} onStop={stopAIRequest} isLoading={aiLoading} activityItems={aiActivityItems} fullWidth />
        )}

        {/* Tool Panel */}
        {ui.toolPanelOpen && (
          <>
            <div className="resize-handle" onMouseDown={handleToolPanelResize} />
            <div className="tool-panel" style={{ width: ui.toolPanelWidth }}>
              <div className="tool-panel-header" style={{ display: 'none' }}>
                <div className="tool-panel-tabs">
                  {(["terminal", "analysis", "debug", "modify", "changes"] as const).map((tab) => (
                    <button key={tab} className={`tool-panel-tab${ui.activeToolTab === tab ? " active" : ""}`}
                      onClick={() => ui.setActiveToolTab(tab)}>
                      {tab === "terminal" ? t("terminal.title") :
                       tab === "analysis" ? t("tool.analysis") :
                       tab === "debug" ? t("tool.debug") :
                       tab === "modify" ? t("tool.runOutput") :
                       tab === "changes" ? t("sidebar.changes") :
                       installedExtensions.find((ext) => ext.id === tab)?.name ?? tab}
                    </button>
                  ))}
                </div>
                <button className="tool-panel-close" onClick={() => ui.setToolPanelOpen(false)} title={t("tool.close")}>
                  <X size={14} />
                </button>
              </div>
              <div className="tool-panel-body">
                {ui.activeToolTab === "terminal" && (
                  <div className="tool-tab-content">
                    <div className="terminal-tabs">
                      <div className="terminal-tabs-scroll">
                        {ui.terminals.map((term, idx) => (
                          <div key={term.id} className={`terminal-tab${ui.activeTerminalId === term.id ? " active" : ""}`}
                            onClick={() => ui.setActiveTerminalId(term.id)}>
                            <span>{t("terminal.tabName").replace("{{n}}", String(idx + 1))}</span>
                            {ui.terminals.length > 1 && (
                              <button className="terminal-tab-close"
                                onClick={(e) => { e.stopPropagation(); ui.removeTerminal(term.id); }}>×</button>
                            )}
                          </div>
                        ))}
                      </div>
                      <button className="terminal-add-btn" onClick={() => ui.addTerminal()} title={t("terminal.add")}>
                        <Plus size={12} />
                      </button>
                    </div>
                    <div className="terminal-content">
                      {ui.terminals.map((term) => (
                        <TerminalPanel key={term.id} port={ui.terminalPort}
                          active={ui.activeToolTab === "terminal" && ui.toolPanelOpen && ui.activeTerminalId === term.id}
                          hidden={ui.activeTerminalId !== term.id}
                          initCmd={term.initCmd} />
                      ))}
                    </div>
                  </div>
                )}
                {ui.activeToolTab === "analysis" && (
                  <div className="tool-tab-content" style={{ overflowY: "auto", padding: 0 }}>
                    <AnalysisPanel
                      filePath={editor.activeTab}
                      content={analysisContent}
                      onGoToLine={(line) => editor.activeTab && openFileAtLine(editor.activeTab, line)}
                      rootPath={project.activeProject?.rootPath ?? null}
                      langId={activeFileLang}
                      cursorLine={editor.cursorInfo.line}
                      cursorCol={editor.cursorInfo.col}
                      onOpenFile={(path, line) => openFileAtLine(path, line)}
                      onAddContext={appendSymbolContext}
                      lspPort={ui.lspPort || undefined}
                      monaco={monacoInstance}
                    />
                  </div>
                )}
                <div className="tool-tab-content" style={{ padding: 0, overflow: "hidden", display: ui.activeToolTab === "debug" ? "flex" : "none", flexDirection: "column" }}>
                  <DebugPanel ref={debugPanelRef} rootPath={project.activeProject?.rootPath ?? null}
                    activeFile={editor.activeTab} breakpoints={breakpoints}
                    projectType={project.activeProject?.type ?? null}
                    onPauseAt={(file, line) => { setDebugPausedAt({ file, line }); openFileAtLine(file, line); }}
                    onNavigate={openFileAtLine}
                    onDeleteBreakpoint={(file, line) => setBreakpoints((p) => p.filter((bp) => !(bp.file === file && bp.line === line)))}
                    onClearBreakpoints={() => setBreakpoints([])}
                    onToggleBreakpoint={(file, line, enabled) => setBreakpoints((p) => p.map((bp) => bp.file === file && bp.line === line ? { ...bp, enabled } : bp))}
                    onUpdateBreakpoint={(file, line, patch) => setBreakpoints((p) => p.map((bp) => bp.file === file && bp.line === line ? { ...bp, ...patch } : bp))}
                    onDebugStateChange={(s) => { if (s !== "paused") setDebugPausedAt(null); }} />
                </div>
                {ui.activeToolTab === "modify" && (
                  <div className="tool-tab-content" style={{ overflowY: "auto", padding: 0 }}>
                    <OutputPanel projectRootPath={project.activeProject?.rootPath ?? null} />
                  </div>
                )}
                {ui.activeToolTab === "changes" && (
                  <div className="tool-tab-content" style={{ overflowY: "auto", padding: 0 }}>
                    <AIChangesPanel
                      tasks={agentTasks}
                      projectRoot={project.activeProject?.rootPath ?? null}
                      sessionID="main"
                      onApplyTask={(taskId) => {
                        console.log("Apply task:", taskId);
                      }}
                      onRejectTask={(taskId) => {
                        console.log("Reject task:", taskId);
                      }}
                      onApplyFile={(taskId, filePath) => {
                        console.log("Apply file:", taskId, filePath);
                      }}
                      onRejectFile={(taskId, filePath) => {
                        console.log("Reject file:", taskId, filePath);
                      }}
                      onRefresh={() => {
                        console.log("Refresh tasks");
                      }}
                    />
                  </div>
                )}
              </div>
              <div className="tool-panel-footer">
                <button className={`tool-footer-tab${ui.activeToolTab === "analysis" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("analysis")} title={t("tool.analysis")}>
                  <ListTree size={16} />
                </button>
                <button className={`tool-footer-tab${ui.activeToolTab === "debug" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("debug")} title={t("tool.debug")}>
                  <Bug size={16} />
                </button>
                <button className={`tool-footer-tab${ui.activeToolTab === "terminal" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("terminal")} title={t("terminal.title")}>
                  <Terminal size={16} />
                </button>
                <button className={`tool-footer-tab${ui.activeToolTab === "changes" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("changes")} title={t("sidebar.changes")}>
                  <History size={16} />
                </button>
              </div>
            </div>
          </>
        )}
      </div>

      <StatusBar
        activeFile={editor.activeTab}
        isDirty={activeTabData?.isDirty ?? false}
        gitBranch={project.activeProject?.gitStatus?.branch ?? null}
        gitDirty={project.activeProject?.gitStatus?.isDirty ?? false}
        workingDir={project.activeProject?.rootPath ?? null}
        language={editor.activeTab ? extOf(editor.activeTab) : ""}
        cursorInfo={editor.cursorInfo}
        activeProject={project.activeProject}
        onStartProject={() => { ui.setToolPanelOpen(true); ui.setActiveToolTab("modify"); }}
        onStartDebug={() => { ui.setToolPanelOpen(true); ui.setActiveToolTab("debug"); debugPanelRef.current?.startDebug(); }}
      />

      {runEnvTarget && (
        <RunEnvConfigModal
          projectName={runEnvTarget.name}
          rootPath={runEnvTarget.path}
          projectType={runEnvTarget.projectId ? project.projects.find((p) => p.id === runEnvTarget.projectId)?.type : undefined}
          onSaveType={(type) => runEnvTarget.projectId && project.setProjectType(runEnvTarget.projectId, type)}
          onClose={() => setRunEnvTarget(null)}
        />
      )}

      {ui.showWizard && (
        <ProjectWizard
          onCancel={() => ui.setShowWizard(false)}
          onConfirm={(result: WizardResult) => {
            ui.setShowWizard(false);
            project.importProject(result.dirPath).catch(console.error);
            runExtensionInstalls(result.extensionsToInstall, result.setupType, result.dirPath);
          }}
        />
      )}

      <GlobalProgress tasks={installTasks} visible={ui.showProgress} onDismiss={() => ui.setShowProgress(false)} />

      {showSettings && <SettingsModal onClose={() => setShowSettings(false)} />}

      <PermissionConfirmModal
        request={permRequest}
        onRespond={async (requestID, approved) => {
          setPermRequest(null);
          try { await RespondPermission(requestID, approved); } catch { /* ignore */ }
        }}
      />

      <ExtensionDrawer
        ext={extensionDetailExt}
        lspPort={ui.lspPort}
        rootPath={project.activeProject?.rootPath}
        monaco={monacoInstance}
        onClose={() => setExtensionDetailExt(null)}
        onUninstall={(e) => { handleUninstallExtension(e); setExtensionDetailExt(null); }}
      />
    </div>
    </WindowRouter>
  );
}
