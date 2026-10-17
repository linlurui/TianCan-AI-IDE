import React, { useRef, useEffect, useState, useCallback } from "react";
import { useTranslation } from "../i18n";
import { Events } from "@wailsio/runtime";
import { Trash2, Paperclip, SendHorizontal, Square, Cpu, Cloud, Download, Loader2, Key, Globe, Activity, Wrench, CheckCircle2, AlertCircle, ChevronDown, ChevronRight, FileCode2, Terminal, Search } from "lucide-react";
import {
  ListModels, IsLMStudioInstalled, InstallLMStudio, LaunchLMStudio,
  SetModel, ListAPIProviders, SetAPIProvider, GetAPIProvider,
  ListWebProviders, LoginWeb, SetWebAuth, GetWebAuth, IsWebProviderLoggedIn,
  SmartRoute, RefreshModelStatuses,
} from "@bindings/ai";
import type { APIProviderConfig, WebProviderInfo } from "@bindings/ai";
import SubAgentProgressPanel from "./SubAgentProgress";
import SessionMemoryBar from "./SessionMemoryBar";
import { describeTool, type ActivityItem, type ActivityKind } from "../aiActivity";
export type { ActivityItem, ActivityKind } from "../aiActivity";

// ─── <think> block component ────────────────────────────────
function ThinkBlock({ content, streaming, title }: { content: string; streaming?: boolean; title?: string }) {
  const [open, setOpen] = useState(streaming ?? false);
  const liveRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const displayContent = content.trim() || (streaming ? "正在准备模型和项目上下文..." : "");
  // 实时思考行：只显示最新一句，单行展示、超出一行隐藏
  const liveLines = content.split("\n").map((l) => l.trim()).filter(Boolean);
  const liveText = liveLines.length ? liveLines[liveLines.length - 1] : "";
  useEffect(() => {
    // 折叠态：内容更新时快速横向滚动到最新（跑马灯效果）
    if (streaming && !open && liveRef.current) {
      liveRef.current.scrollLeft = liveRef.current.scrollWidth;
    }
  }, [liveText, streaming, open]);
  useEffect(() => {
    // 展开态：流式期间自动滚到最新思考内容
    if (streaming && open && contentRef.current) {
      contentRef.current.scrollTop = contentRef.current.scrollHeight;
    }
  }, [content, streaming, open]);
  return (
    <div className={`ai-think-block${streaming ? " ai-think-streaming" : ""}`}>
      <button className="ai-think-toggle" onClick={() => setOpen(o => !o)}>
        <span className="ai-think-chevron">{open ? "▾" : "▸"}</span>
        <span>{title || (streaming ? "思考中…" : "思考过程")}</span>
      </button>
      {streaming && !open && (
        <div className="ai-think-live" ref={liveRef}>
          <span>{liveText || "正在准备模型和项目上下文..."}</span>
        </div>
      )}
      {open && <div className="ai-think-content" ref={contentRef}>{displayContent}</div>}
    </div>
  );
}

// Parse assistant content splitting on <think>...</think> blocks.
function renderMessageContent(content: string): React.ReactNode[] {
  const parts: React.ReactNode[] = [];
  const re = /<think>([\s\S]*?)<\/think>/g;
  let last = 0, key = 0, m: RegExpExecArray | null;

  while ((m = re.exec(content)) !== null) {
    if (m.index > last) parts.push(<span key={key++}>{content.slice(last, m.index)}</span>);
    parts.push(<ThinkBlock key={key++} content={m[1]} />);
    last = m.index + m[0].length;
  }

  const rest = content.slice(last);
  const openIdx = rest.indexOf("<think>");
  if (openIdx !== -1) {
    if (openIdx > 0) parts.push(<span key={key++}>{rest.slice(0, openIdx)}</span>);
    parts.push(<ThinkBlock key={key++} content={rest.slice(openIdx + 7)} streaming />);
  } else if (rest) {
    parts.push(<span key={key++}>{rest}</span>);
  }

  return parts.length ? parts : [<span key={0}>{content}</span>];
}

// ─── Message body renderer ──────────────────────────────
function MessageBody({ msg, t }: { msg: Message; t: (key: string) => string }) {
  if (msg.role === "tool_call") {
    return (
      <div className="ai-tool-card ai-tool-call">
        <div className="ai-tool-card-header">
          <span className="ai-tool-icon">🔧</span>
          <span className="ai-tool-name">{msg.name}</span>
        </div>
        <pre className="ai-tool-args">{(() => { try { return JSON.stringify(JSON.parse(msg.content), null, 2); } catch { return msg.content; } })()}</pre>
      </div>
    );
  }
  if (msg.role === "tool_result") {
    return (
      <div className="ai-tool-card ai-tool-result">
        <div className="ai-tool-card-header">
          <span className="ai-tool-icon">📋</span>
          <span className="ai-tool-name">{msg.name}</span>
        </div>
        <pre className="ai-tool-output">{msg.content.length > 500 ? msg.content.slice(0, 500) + "…" : msg.content}</pre>
      </div>
    );
  }
  if (msg.role === "thinking") {
    return <ThinkBlock content={msg.content} streaming={msg.name === "__activity__"} title={msg.name === "__activity__" ? "执行过程" : undefined} />;
  }
  return (
    <>
      <div className="ai-message-role">
        {msg.role === "user" ? t("ai.roleUser") : msg.role === "assistant" ? t("ai.roleAssistant") : t("ai.roleSystem")}
      </div>
      <div className="ai-message-body">{renderMessageContent(msg.content)}</div>
    </>
  );
}

export interface Message {
  role: "user" | "assistant" | "system" | "tool_call" | "tool_result" | "thinking";
  content: string;
  name?: string; // tool name for tool_call/tool_result
  streaming?: boolean;
}

export type ModelSource = "auto" | "local" | "api" | "web";
type LMModel = { id: string; label: string; available?: boolean; disable_reason?: string };

const LOCAL_MODELS_FALLBACK: LMModel[] = [
  { id: "default", label: "LM Studio Default" },
];

interface AIPanelProps {
  messages: Message[];
  onSend: (text: string, source: ModelSource) => void | Promise<void>;
  onStop?: () => void | Promise<void>;
  isLoading: boolean;
  fullWidth?: boolean;
  contextToAppend?: { id: number; text: string } | null;
  activityItems?: ActivityItem[];
  /** 项目根路径：有值时聊天框显示「项目记忆」工具条（锚点/审计/回退/红线/召回/提示词） */
  projectRoot?: string;
  /** 回退成功后通知上层刷新消息列表 */
  onRollback?: (seq: number) => void;
}

function ActivityIcon({ kind }: { kind: ActivityKind }) {
  switch (kind) {
    case "tool":
      return <Wrench size={12} />;
    case "result":
    case "success":
      return <CheckCircle2 size={12} />;
    case "error":
      return <AlertCircle size={12} />;
    case "route":
      return <Activity size={12} />;
    default:
      return <Loader2 size={12} className="spin" />;
  }
}

function elapsedLabel(item: ActivityItem, now: number) {
  if (!item.startedAt) return "";
  const end = item.completedAt || now;
  const seconds = Math.max(0, Math.round((end - item.startedAt) / 1000));
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

function ActivityRow({ item, now, isLoading }: { item: ActivityItem; now: number; isLoading: boolean }) {
  const [open, setOpen] = useState(false);
  const tool = item.toolName ? describeTool(item.toolName, item.detail) : null;
  const thoughtSummary = item.kind === "thinking" ? item.detail?.replace(/\s+/g, " ").trim() : "";
  const title = thoughtSummary || tool?.title || item.text;
  const summary = tool?.summary;
  const files = item.files?.length ? item.files : tool?.files || [];
  const expandable = item.kind !== "thinking" && Boolean(item.detail || item.output || files.length);
  const icon = item.status === "success" || (!isLoading && item.status === "running")
    ? <CheckCircle2 size={12} />
    : item.status === "error"
      ? <AlertCircle size={12} />
      : item.toolName && ["bash", "shell", "execute_command", "run_command", "test_run"].includes(item.toolName)
    ? <Terminal size={13} />
    : item.toolName && ["grep", "grep_files", "glob", "search_files", "web_search"].includes(item.toolName)
      ? <Search size={13} />
      : <ActivityIcon kind={item.kind} />;

  return (
    <div className={`ai-trace-row ${item.status || item.kind}`}>
      <button className="ai-trace-summary" onClick={() => expandable && setOpen((value) => !value)} aria-expanded={open}>
        <span className="ai-trace-chevron">{expandable ? (open ? <ChevronDown size={13} /> : <ChevronRight size={13} />) : null}</span>
        <span className="ai-trace-icon">{icon}</span>
        <span className="ai-trace-title">{title}</span>
        {summary && <span className="ai-trace-inline">{summary}</span>}
        <span className="ai-trace-time">{elapsedLabel(item, now)}</span>
      </button>
      {open && (
        <div className="ai-trace-detail">
          {files.map((file) => (
            <div className="ai-trace-file" key={file} title={file}>
              <CheckCircle2 size={11} />
              <FileCode2 size={12} />
              <span>{file}</span>
            </div>
          ))}
          {item.detail && <pre>{item.detail.length > 4000 ? `${item.detail.slice(0, 4000)}\n...` : item.detail}</pre>}
          {item.output && <pre className="ai-trace-output">{item.output.length > 4000 ? `${item.output.slice(0, 4000)}\n...` : item.output}</pre>}
        </div>
      )}
    </div>
  );
}

function AIActivityTimeline({ items, isLoading }: { items: ActivityItem[]; isLoading: boolean }) {
  const [open, setOpen] = useState(true);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!isLoading) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [isLoading]);

  const allVisibleItems = items.filter((item) => item.toolName || (item.kind === "thinking" && item.text === "思考过程"));
  const visibleItems = allVisibleItems.slice(-4);
  if (!visibleItems.length) return null;
  const running = isLoading && visibleItems.some((item) => item.status === "running");
  const hasReasoning = visibleItems.some((item) => item.kind === "thinking");
  return (
    <section className="ai-trace" aria-label="模型思考与操作">
      <button className="ai-trace-header" onClick={() => setOpen((value) => !value)}>
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        <span>{hasReasoning ? "模型思考" : "模型操作"}</span>
        <span className="ai-trace-count">最近 {visibleItems.length} 条</span>
        {running && <Loader2 size={13} className="spin ai-trace-running" />}
      </button>
      {open && <div className="ai-trace-list">{visibleItems.map((item) => <ActivityRow key={item.id} item={item} now={now} isLoading={isLoading} />)}</div>}
    </section>
  );
}

export default function AIPanel({ messages, onSend, onStop, isLoading, fullWidth, contextToAppend, activityItems = [], projectRoot, onRollback }: AIPanelProps) {
  const [input, setInput] = React.useState("");
  const [source, setSource] = useState<ModelSource>(() => (localStorage.getItem("ai_source") as ModelSource) || "auto");
  const [routeInfo, setRouteInfo] = useState<{ tier: string; reason: string } | null>(null);
  const [localModels, setLocalModels] = useState<LMModel[]>(LOCAL_MODELS_FALLBACK);
  const [lmStudioKillMsg, setLmStudioKillMsg] = useState<string | null>(null);
  const [model, setModel] = useState("default");
  const [lmInstalled, setLmInstalled] = useState<boolean | null>(null);
  const [installing, setInstalling] = useState(false);
  const [installLogs, setInstallLogs] = useState<string[]>([]);
  // API mode state
  const [providers, setProviders] = useState<APIProviderConfig[]>([]);
  const [activeProvider, setActiveProvider] = useState<APIProviderConfig>({ name: "", baseUrl: "", apiKey: "", model: "" });
  const [showApiKeyInput, setShowApiKeyInput] = useState(false);
  const [apiKeyInput, setApiKeyInput] = useState("");
  const [customBaseUrl, setCustomBaseUrl] = useState("");
  const [customModel, setCustomModel] = useState("");
  // API test state
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ success: boolean; message: string } | null>(null);
  // Web mode state
  const [webProviders, setWebProviders] = useState<WebProviderInfo[]>([]);
  const [webProviderID, setWebProviderIDState] = useState(() => localStorage.getItem("ai_web_provider") || "");
  const setWebProviderID = async (id: string) => {
    setWebProviderIDState(id);
    if (id) localStorage.setItem("ai_web_provider", id);
    // Check login status for the newly selected provider
    try {
      const loggedIn = await IsWebProviderLoggedIn(id);
      setWebLoggedIn(loggedIn);
      if (!loggedIn) {
        setShowWebCookieInput(false);
      }
    } catch {
      setWebLoggedIn(false);
    }
  };
  const [webLoggedIn, setWebLoggedIn] = useState(false);
  const [webLoginWaiting, setWebLoginWaiting] = useState(false);
  const [showWebCookieInput, setShowWebCookieInput] = useState(false);
  const [webCookieInput, setWebCookieInput] = useState("");
  const { t } = useTranslation();
  const bottomRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [inputAreaHeight, setInputAreaHeight] = useState(180); // px
  const [pendingPrompts, setPendingPrompts] = useState<Array<{ id: number; text: string; source: ModelSource }>>([]);
  const promptQueueRef = useRef<Array<{ id: number; text: string; source: ModelSource }>>([]);
  const drainingQueueRef = useRef(false);
  const onSendRef = useRef(onSend);
  const resizingRef = useRef(false);
  const startYRef = useRef(0);
  const startHeightRef = useRef(0);

  useEffect(() => {
    onSendRef.current = onSend;
  }, [onSend]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  useEffect(() => {
    if (!contextToAppend?.text) return;
    setInput((current) => {
      if (current.includes(contextToAppend.text)) return current;
      const prefix = current.trimEnd();
      return prefix ? `${prefix}\n${contextToAppend.text}` : contextToAppend.text;
    });
    requestAnimationFrame(() => textareaRef.current?.focus());
  }, [contextToAppend?.id]);

  // Listen for Playwright-based web login events from the backend
  useEffect(() => {
    const offSuccess = Events.On("webai:login-success", (evt: any) => {
      setWebLoginWaiting(false);
      setWebLoggedIn(true);
      setShowWebCookieInput(false);
      const provider = typeof evt === "object" && evt !== null ? evt.provider : "";
      setInstallLogs((p) => [...p, `✅ ${provider || t("ai.webProvider")} 登录成功`]);
    });
    const offError = Events.On("webai:login-error", (evt: any) => {
      setWebLoginWaiting(false);
      setShowWebCookieInput(true); // fall back to manual cookie input
      const msg = typeof evt === "object" && evt !== null ? evt.error : String(evt);
      setInstallLogs((p) => [...p, `❌ Playwright 登录失败: ${msg}，请手动粘贴 Cookie`]);
    });
    const offFallback = Events.On("webai:login-fallback", () => {
      setWebLoginWaiting(false);
      setShowWebCookieInput(true);
      setInstallLogs((p) => [...p, `ℹ️ 浏览器已打开，登录后请手动粘贴 Cookie`]);
    });
    return () => { offSuccess(); offError(); offFallback(); };
  }, [t]);

  // Listen for router-driven local model availability updates
  useEffect(() => {
    const offModels = Events.On("router:local-models-updated", (evt: any) => {
      const statuses = Array.isArray(evt) ? evt : [];
      const updated = statuses.map((s: any) => ({
        id: s.id,
        label: s.id,
        available: s.available !== false,
        disable_reason: s.disable_reason || "",
      }));
      if (updated.length > 0) {
        setLocalModels([{ id: "default", label: "LM Studio Default", available: true }, ...updated]);
      }
    });
    const offKill = Events.On("router:kill-lm-studio", (evt: any) => {
      const msg = typeof evt === "string" ? evt : "所有本地模型均不可用，建议关闭 LM Studio 释放内存";
      setLmStudioKillMsg(msg);
      setLmInstalled(false);
    });
    return () => { offModels(); offKill(); };
  }, []);

  // Listen for SmartRouter routing decisions
  useEffect(() => {
    const offDecision = Events.On("router:decision", (evt: any) => {
      if (evt && typeof evt === "object") {
        setRouteInfo({ tier: evt.tier || "local", reason: evt.reason || "" });
      }
    });
    const offFallback = Events.On("router:fallback", (evt: any) => {
      if (evt && typeof evt === "object") {
        setRouteInfo({ tier: evt.to || "local", reason: `降级: ${evt.from} → ${evt.to}` });
      }
    });
    return () => { offDecision(); offFallback(); };
  }, []);

  const refreshLocalModels = useCallback(async () => {
    try {
      const models = await ListModels();
      if (models && models.length > 0) {
        const lmModels = models.map((m: any) => ({
          id: m.id,
          label: m.id,
          available: m.available !== false,
          disable_reason: m.disable_reason || "",
        }));
        setLocalModels([{ id: "default", label: "LM Studio Default", available: true }, ...lmModels]);
        return true;
      }
    } catch { /* server not running */ }
    return false;
  }, []);

  const checkLMStudio = useCallback(async () => {
    setLmInstalled(null); // checking
    try {
      const ok = await IsLMStudioInstalled();
      setLmInstalled(ok);
      if (ok) {
        await refreshLocalModels();
      }
    } catch {
      setLmInstalled(false);
    }
  }, [refreshLocalModels]);

  const handleSourceChange = (s: ModelSource) => {
    setSource(s);
    localStorage.setItem("ai_source", s);
    if (s === "local") {
      setModel(localModels.length > 0 ? localModels[0].id : "default");
      checkLMStudio(); // re-check when switching to local
    } else if (s === "api") {
      setModel(activeProvider.model || "gpt-4o");
    } else if (s === "web") {
      setModel(webProviderID || "deepseek");
    }
  };

  // Check LM Studio status on mount
  useEffect(() => {
    checkLMStudio();
  }, []);

  // Load API providers on mount
  useEffect(() => {
    (async () => {
      try {
        const list = await ListAPIProviders();
        setProviders(list);
        const saved = await GetAPIProvider();
        if (saved && saved.name) {
          setActiveProvider(saved);
          setModel(saved.model);
          setApiKeyInput(saved.apiKey);
          setCustomBaseUrl(saved.baseUrl);
          setCustomModel(saved.model);
        } else if (list.length > 0) {
          setActiveProvider(list[0]);
          setModel(list[0].model);
          setCustomBaseUrl(list[0].baseUrl);
          setCustomModel(list[0].model);
        }
      } catch { /* ignore */ }
    })();
  }, []);

  // Load Web providers on mount
  useEffect(() => {
    (async () => {
      try {
        const list = await ListWebProviders();
        setWebProviders(list);
        const saved = await GetWebAuth();
        const storedID = localStorage.getItem("ai_web_provider") || "";
        if (saved) {
          // Backend has auth — prefer stored selection, fall back to auth provider
          const preferred = list.some((p: WebProviderInfo) => p.id === storedID) ? storedID : saved;
          setWebProviderID(preferred);
          setWebLoggedIn(true);
        } else if (storedID && list.some((p: WebProviderInfo) => p.id === storedID)) {
          // No auth yet but user previously selected a provider
          setWebProviderID(storedID);
        } else if (list.length > 0) {
          setWebProviderID(list[0].id);
        }
      } catch { /* ignore */ }
    })();
  }, []);

  const handleInstallLMStudio = useCallback(async () => {
    setInstalling(true);
    setInstallLogs([]);
    try {
      const logs = await InstallLMStudio();
      setInstallLogs(logs);
      setLmInstalled(true);
      await LaunchLMStudio();
      // Poll for models after launch
      for (let attempt = 0; attempt < 6; attempt++) {
        await new Promise(r => setTimeout(r, 2000));
        const gotModels = await refreshLocalModels();
        if (gotModels) break;
      }
    } catch (err: any) {
      setInstallLogs((p) => [...p, `❌ ${t("ai.installFail")}: ${err?.message ?? err}`]);
    } finally {
      setInstalling(false);
    }
  }, []);

  const handleLaunchLMStudio = useCallback(async () => {
    try {
      await LaunchLMStudio();
      setLmInstalled(true);
      // Poll for models after launch (server takes a few seconds to start)
      for (let attempt = 0; attempt < 12; attempt++) {
        await new Promise(r => setTimeout(r, 2000));
        const gotModels = await refreshLocalModels();
        if (gotModels) break;
      }
    } catch (err: any) {
      setInstallLogs((p) => [...p, `❌ ${t("ai.launchFail")}: ${err?.message ?? err}`]);
    }
  }, [refreshLocalModels]);

  const handleProviderChange = useCallback(async (name: string) => {
    const p = providers.find((x) => x.name === name);
    if (p) {
      let next = p;
      try {
        const saved = await GetAPIProvider();
        if (saved?.name === p.name) {
          next = { ...p, ...saved, apiKey: saved.apiKey || p.apiKey || "" };
        }
      } catch { /* keep built-in defaults */ }
      setActiveProvider(next);
      setModel(next.model);
      setCustomBaseUrl(next.baseUrl);
      setCustomModel(next.model);
      setShowApiKeyInput(false);
      setApiKeyInput(next.apiKey || "");
    }
  }, [providers]);

  const handleSaveApiKey = useCallback(async () => {
    const p = activeProvider;
    const baseUrl = p.name === "custom" ? customBaseUrl : p.baseUrl;
    const modelName = p.name === "custom" ? customModel : p.model;
    await SetAPIProvider(p.name, baseUrl, apiKeyInput, modelName);
    setActiveProvider((prev) => ({ ...prev, baseUrl, apiKey: apiKeyInput, model: modelName }));
    setModel(modelName);
    setShowApiKeyInput(false);
  }, [activeProvider, apiKeyInput, customBaseUrl, customModel]);

  const handleWebLogin = useCallback(async () => {
    if (!webProviderID) return;
    try {
      setWebLoginWaiting(true);
      setShowWebCookieInput(false);
      await LoginWeb(webProviderID);
      // If Playwright is available, the browser window is now open.
      // webai:login-success / webai:login-error / webai:login-fallback events
      // will arrive asynchronously and update the state accordingly.
    } catch (err: any) {
      setWebLoginWaiting(false);
      setInstallLogs((p) => [...p, `❌ ${t("ai.openBrowserFail")}: ${err?.message ?? err}`]);
    }
  }, [webProviderID, t]);

  const handleWebSaveCookie = useCallback(async () => {
    if (!webProviderID || !webCookieInput.trim()) return;
    try {
      await SetWebAuth(webProviderID, webCookieInput.trim(), "");
      setWebLoggedIn(true);
      setShowWebCookieInput(false);
      setWebCookieInput("");
    } catch (err: any) {
      setInstallLogs((p) => [...p, `❌ ${t("ai.cookieSaveFail")}: ${err?.message ?? err}`]);
    }
  }, [webProviderID, webCookieInput]);

  const models = source === "local" ? localModels : [{ id: model, label: model }];

  const drainPromptQueue = useCallback(async () => {
    if (drainingQueueRef.current) return;
    drainingQueueRef.current = true;
    try {
      while (promptQueueRef.current.length > 0) {
        const next = promptQueueRef.current.shift();
        if (!next) break;
        setPendingPrompts((current) => current.filter((prompt) => prompt.id !== next.id));
        try {
          await onSendRef.current(next.text, next.source);
        } catch (error) {
          console.error("AI prompt failed:", error);
        }
      }
    } finally {
      drainingQueueRef.current = false;
    }
  }, []);

  const handleSend = () => {
    const text = input.trim();
    if (!text) return;
    const prompt = { id: Date.now() + Math.random(), text, source };
    promptQueueRef.current.push(prompt);
    setPendingPrompts((current) => [...current, prompt]);
    setInput("");
    textareaRef.current?.focus();
    void drainPromptQueue();
  };

  const handleClearChat = () => {
    promptQueueRef.current = [];
    setPendingPrompts([]);
    void onSend("__clear__", source);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey || e.shiftKey)) {
      e.preventDefault();
      handleSend();
    }
  };

  const providerLabel = (name: string) => {
    switch (name) {
      case "openai": return "OpenAI";
      case "deepseek": return "DeepSeek";
      case "qwen": return t("ai.qwen");
      case "siliconflow": return "SiliconFlow";
      case "custom": return t("ai.custom");
      default: return name;
    }
  };

  return (
    <div className={`ai-panel${fullWidth ? " ai-panel-full" : ""}`}>
      <div className="ai-model-header">
        <div className="ai-source-toggle">
          <button
            className={`ai-source-btn${source === "auto" ? " active" : ""}`}
            onClick={() => handleSourceChange("auto")}
            title="Smart Router: 自动选择最佳模型"
          >
            <span style={{ fontSize: 11 }}>⚡</span>
            Auto
          </button>
          <button
            className={`ai-source-btn${source === "local" ? " active" : ""}`}
            onClick={() => handleSourceChange("local")}
            title={t("ai.localModelTitle")}
          >
            <Cpu size={11} />
            {t("ai.local")}
          </button>
          <button
            className={`ai-source-btn${source === "api" ? " active" : ""}`}
            onClick={() => handleSourceChange("api")}
            title={t("ai.apiModelTitle")}
          >
            <Cloud size={11} />
            API
          </button>
          <button
            className={`ai-source-btn${source === "web" ? " active" : ""}`}
            onClick={() => handleSourceChange("web")}
            title={t("ai.webModelTitle")}
          >
            <Globe size={11} />
            Web
          </button>
        </div>
        {routeInfo && (
          <span className="ai-route-badge" title={routeInfo.reason}>
            → {routeInfo.tier}
          </span>
        )}

        {source === "local" && (
          <select
            className="ai-model-select"
            value={model}
            onChange={(e) => { setModel(e.target.value); SetModel(e.target.value).catch(() => {}); }}
          >
            {localModels.map((m) => (
              <option
                key={m.id}
                value={m.id}
                disabled={!m.available}
                style={!m.available ? { color: "#888", fontStyle: "italic" } : undefined}
              >
                {m.label}{!m.available && m.disable_reason ? ` (⚠ ${m.disable_reason})` : ""}
              </option>
            ))}
          </select>
        )}

        {source === "api" && (
          <>
            <select
              className="ai-model-select"
              value={activeProvider.name}
              onChange={(e) => handleProviderChange(e.target.value)}
            >
              {providers.map((p) => (
                <option key={p.name} value={p.name}>{providerLabel(p.name)}</option>
              ))}
            </select>
            {activeProvider.apiKey ? (
              <span className="ai-api-key-badge" title={t("ai.apiKeyConfigured")}>🔑 {t("ai.configured")}</span>
            ) : (
              <button className="ai-install-btn" onClick={() => setShowApiKeyInput(true)} title={t("ai.configApiKey")}>
                <Key size={11} /> {t("ai.configKey")}
              </button>
            )}
          </>
        )}

        {source === "web" && (
          <>
            <select
              className="ai-model-select"
              value={webProviderID}
              onChange={(e) => setWebProviderID(e.target.value)}
            >
              {webProviders.map((p) => (
                <option key={p.id} value={p.id}>{p.displayName}</option>
              ))}
            </select>
            {webLoggedIn ? (
              <span className="ai-api-key-badge" title={t("ai.webLoggedIn")}>🌐 {t("ai.loggedIn")}</span>
            ) : webLoginWaiting ? (
              <span className="ai-install-status" title={t("ai.webLoginWaiting")}>
                <Loader2 size={11} className="spin" /> {t("ai.webLoginWaiting")}
              </span>
            ) : (
              <button className="ai-install-btn" onClick={handleWebLogin} title={t("ai.loginWebTitle")}>
                <Globe size={11} /> {t("ai.login")}
              </button>
            )}
          </>
        )}

        {source === "local" && lmInstalled === null && (
          <span className="ai-install-status"><Loader2 size={11} className="spin" /> {t("ai.detecting")}</span>
        )}
        {source === "local" && lmInstalled === false && !installing && (
          <button className="ai-install-btn" onClick={handleInstallLMStudio} title={t("ai.oneClickInstall")}>
            <Download size={11} /> {t("ai.installLM")}
          </button>
        )}
        {source === "local" && lmStudioKillMsg && (
          <span className="ai-install-status" style={{ color: "#e55", fontSize: 11 }}>⚠ {lmStudioKillMsg}</span>
        )}
        {source === "local" && lmInstalled === false && installing && (
          <span className="ai-install-status"><Loader2 size={11} className="spin" /> {t("ai.installing")}</span>
        )}
        {source === "local" && lmInstalled === true && localModels.length <= 1 && (
          <button className="ai-install-btn" onClick={handleLaunchLMStudio} title={t("ai.launchLMTitle")}>
            <Cpu size={11} /> {t("ai.launchLM")}
          </button>
        )}
        {source === "local" && lmInstalled === true && localModels.length > 1 && (
          <button className="ai-install-btn" onClick={refreshLocalModels} title={t("ai.refreshModels")} style={{ padding: "2px 6px" }}>
            <Loader2 size={11} /> {t("ai.refresh")}
          </button>
        )}
        <button
          className="sidebar-header-btn sidebar-header-btn-always"
          title={t("ai.clearChat")}
          onClick={handleClearChat}
          style={{ marginLeft: "auto", flexShrink: 0 }}
        >
          <Trash2 size={12} />
        </button>
      </div>

      {/* API Key configuration panel */}
      {source === "api" && showApiKeyInput && (
        <div className="ai-api-config">
          <div className="ai-api-config-row">
            <label>API Key</label>
            <input
              className="settings-input"
              type="password"
              placeholder="sk-..."
              value={apiKeyInput}
              onChange={(e) => setApiKeyInput(e.target.value)}
            />
          </div>
          {activeProvider.name === "custom" && (
            <>
              <div className="ai-api-config-row">
                <label>Base URL</label>
                <input
                  className="settings-input"
                  placeholder="https://api.example.com/v1"
                  value={customBaseUrl}
                  onChange={(e) => setCustomBaseUrl(e.target.value)}
                />
              </div>
              <div className="ai-api-config-row">
                <label>{t("ai.model")}</label>
                <input
                  className="settings-input"
                  placeholder="model-name"
                  value={customModel}
                  onChange={(e) => setCustomModel(e.target.value)}
                />
              </div>
            </>
          )}
          <div className="ai-api-config-row">
            <label>{t("ai.model")}</label>
            <input
              className="settings-input"
              placeholder={activeProvider.model || "model-name"}
              value={activeProvider.name === "custom" ? customModel : model}
              onChange={(e) => { setModel(e.target.value); if (activeProvider.name === "custom") setCustomModel(e.target.value); }}
            />
          </div>
          <div style={{ display: "flex", gap: 6, justifyContent: "flex-end", alignItems: "center" }}>
            <button
              className="btn-secondary"
              onClick={async () => {
                if (!apiKeyInput && !customBaseUrl) return;
                setTesting(true);
                setTestResult(null);
                try {
                  const testModel = activeProvider.name === "custom" ? customModel : model;
                  const testBaseUrl = activeProvider.name === "custom" ? customBaseUrl : activeProvider.baseUrl;
                  // Call backend to test connection
                  await (window as any).go.ai.Service.AddCustomProvider(
                    "test-" + Date.now(),
                    "Test",
                    testBaseUrl,
                    apiKeyInput,
                    testModel
                  );
                  setTestResult({ success: true, message: t("settings.connectionSuccess") });
                } catch (e: any) {
                  setTestResult({ success: false, message: e.message || t("settings.connectionFailed") });
                } finally {
                  setTesting(false);
                }
              }}
              disabled={testing || (!apiKeyInput && !customBaseUrl)}
            >
              {testing ? t("settings.testing") : t("settings.testConnection")}
            </button>
            {testResult && (
              <span className={`test-result ${testResult.success ? "success" : "error"}`}>
                {testResult.message}
              </span>
            )}
            <button className="btn-ghost" onClick={() => { setShowApiKeyInput(false); setTestResult(null); }}>{t("ai.cancel")}</button>
            <button className="btn-primary" onClick={() => { handleSaveApiKey(); setShowApiKeyInput(false); }}>{t("ai.save")}</button>
          </div>
        </div>
      )}

      {/* Web Cookie configuration panel */}
      {source === "web" && showWebCookieInput && (
        <div className="ai-api-config">
          <div className="settings-hint" style={{ fontSize: "10px", marginBottom: 4 }}>
            {t("ai.webLoginHint", { provider: webProviders.find(p => p.id === webProviderID)?.displayName ?? webProviderID })}
          </div>
          <div className="ai-api-config-row">
            <label>Cookie</label>
            <input
              className="settings-input"
              type="password"
              placeholder="name1=val1; name2=val2; ..."
              value={webCookieInput}
              onChange={(e) => setWebCookieInput(e.target.value)}
            />
          </div>
          <div style={{ display: "flex", gap: 6, justifyContent: "flex-end" }}>
            <button className="btn-ghost" onClick={() => setShowWebCookieInput(false)}>{t("ai.cancel")}</button>
            <button className="btn-primary" onClick={handleWebSaveCookie}>{t("ai.save")}</button>
          </div>
        </div>
      )}

      {projectRoot ? (
        <SessionMemoryBar
          projectRoot={projectRoot}
          sessionID="main"
          input={input}
          onRollback={(seq) => onRollback?.(seq)}
        />
      ) : null}

      <div className="ai-messages">
        {installLogs.length > 0 && (
          <div className="ai-install-logs">
            {installLogs.map((l, i) => <div key={i}>{l}</div>)}
          </div>
        )}
        {messages.filter((msg) => !["thinking", "tool_call", "tool_result"].includes(msg.role)).length === 0 && (
          <div style={{
            textAlign: "center",
            color: "var(--text-muted)",
            fontSize: "12px",
            marginTop: "24px",
            lineHeight: 1.8,
          }}>
            <div className="ai-welcome-icon">
              <svg
                xmlns="http://www.w3.org/2000/svg"
                width="96"
                height="96"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="1"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <path d="M12 8V4H8"/>
                <ellipse cx="12" cy="15" rx="8" ry="7"/>
                <path d="M2 14h2"/>
                <path d="M20 14h2"/>
                <path d="M9 13v2"/>
                <path d="M15 13v2"/>
              </svg>
            </div>
            <div style={{ fontSize: "18px", fontWeight: 600, color: "var(--text)", marginBottom: "4px" }}>
              {t("ai.appName")}
            </div>
            <div style={{ fontSize: "14px", color: "var(--text-secondary)" }}>
              {t("ai.name")}
            </div>
            <div style={{ marginTop: "12px", color: "var(--text-muted)", fontSize: "11px", lineHeight: 1.6 }}>
              {t("ai.shortcuts")}
            </div>
          </div>
        )}

        {messages.filter((msg) => !["thinking", "tool_call", "tool_result"].includes(msg.role)).map((msg, idx) => (
          <div key={idx} className={`ai-message ${msg.role}`}>
            <MessageBody msg={msg} t={t} />
          </div>
        ))}

        {pendingPrompts.map((prompt) => (
          <div key={prompt.id} className="ai-message user ai-message-queued">
            <div className="ai-message-role">{t("ai.roleUser")} · 排队中</div>
            <div className="ai-message-body">{prompt.text}</div>
          </div>
        ))}

        {isLoading && messages[messages.length - 1]?.role !== "assistant" && !activityItems.some((item) => item.toolName || (item.kind === "thinking" && item.text === "思考过程")) && (
          <div className="ai-message assistant">
            <div className="ai-message-role">AI</div>
            <div className="ai-message-body" style={{ color: "var(--text-muted)" }}>
              {t("ai.thinking")}
            </div>
          </div>
        )}

        <div ref={bottomRef} />
      </div>

      <AIActivityTimeline items={activityItems} isLoading={isLoading} />

      {/* Resize handle between messages and input */}
      <div
        className="ai-resize-handle"
        onMouseDown={(e) => {
          e.preventDefault();
          resizingRef.current = true;
          startYRef.current = e.clientY;
          startHeightRef.current = inputAreaHeight;
          document.body.style.cursor = "row-resize";
          document.body.style.userSelect = "none";
          const onMove = (ev: MouseEvent) => {
            if (!resizingRef.current) return;
            // Dragging UP → input area gets taller (delta negative → height increases)
            const delta = startYRef.current - ev.clientY;
            const newH = Math.max(100, Math.min(500, startHeightRef.current + delta));
            setInputAreaHeight(newH);
          };
          const onUp = () => {
            resizingRef.current = false;
            document.body.style.cursor = "";
            document.body.style.userSelect = "";
            document.removeEventListener("mousemove", onMove);
            document.removeEventListener("mouseup", onUp);
          };
          document.addEventListener("mousemove", onMove);
          document.addEventListener("mouseup", onUp);
        }}
      />

      <SubAgentProgressPanel />

      <div className="ai-input-area" style={{ height: inputAreaHeight, minHeight: 100, maxHeight: 500 }}>
        <textarea
          ref={textareaRef}
          className="ai-input"
          placeholder={t("ai.inputPlaceholder")}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          rows={3}
        />
        <div className="ai-input-actions">
          <button className="btn-ghost" title={t("ai.attachContextTitle")} style={{ display: "flex", alignItems: "center", gap: 5 }}>
            <Paperclip size={13} />
            {t("ai.context")}
          </button>
          {isLoading && onStop && (
            <button className="ai-stop-btn" onClick={() => void onStop()} title="停止生成" aria-label="停止生成">
              <Square size={12} fill="currentColor" />
            </button>
          )}
          <button
            className="btn-primary"
            onClick={handleSend}
            disabled={!input.trim()}
            title={isLoading ? "当前任务完成后继续处理" : t("ai.send")}
            style={{ display: "flex", alignItems: "center", gap: 5 }}
          >
            <SendHorizontal size={13} />
            {isLoading && pendingPrompts.length > 0 ? `排队 ${pendingPrompts.length}` : t("ai.send")}
          </button>
        </div>
      </div>
    </div>
  );
}
