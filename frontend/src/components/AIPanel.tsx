import React, { useRef, useEffect, useState, useCallback } from "react";
import { useTranslation } from "../i18n";
import { Events } from "@wailsio/runtime";
import { Trash2, Paperclip, SendHorizontal, Cpu, Cloud, Download, Loader2, Key, Globe } from "lucide-react";
import {
  ListModels, IsLMStudioInstalled, InstallLMStudio, LaunchLMStudio,
  SetModel, ListAPIProviders, SetAPIProvider, GetAPIProvider,
  ListWebProviders, LoginWeb, SetWebAuth, GetWebAuth, IsWebProviderLoggedIn,
} from "../bindings/ai";
import type { APIProviderConfig, WebProviderInfo } from "../bindings/ai";

// ─── <think> block component ────────────────────────────────
function ThinkBlock({ content, streaming }: { content: string; streaming?: boolean }) {
  const [open, setOpen] = useState(streaming ?? false);
  return (
    <div className={`ai-think-block${streaming ? " ai-think-streaming" : ""}`}>
      <button className="ai-think-toggle" onClick={() => setOpen(o => !o)}>
        <span className="ai-think-chevron">{open ? "▾" : "▸"}</span>
        <span>{streaming ? "思考中…" : "思考过程"}</span>
      </button>
      {open && <div className="ai-think-content">{content.trim()}</div>}
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
    return <ThinkBlock content={msg.content} />;
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
}

export type ModelSource = "local" | "api" | "web";

type LMModel = { id: string; label: string };

const LOCAL_MODELS_FALLBACK: LMModel[] = [
  { id: "default", label: "LM Studio Default" },
];

interface AIPanelProps {
  messages: Message[];
  onSend: (text: string, source: ModelSource) => void;
  isLoading: boolean;
  fullWidth?: boolean;
}

export default function AIPanel({ messages, onSend, isLoading, fullWidth }: AIPanelProps) {
  const [input, setInput] = React.useState("");
  const [source, setSource] = useState<ModelSource>(() => (localStorage.getItem("ai_source") as ModelSource) || "local");
  const [localModels, setLocalModels] = useState<LMModel[]>(LOCAL_MODELS_FALLBACK);
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

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

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

  const refreshLocalModels = useCallback(async () => {
    try {
      const models = await ListModels();
      if (models && models.length > 0) {
        const lmModels = models.map((m: any) => ({ id: m.id, label: m.id }));
        setLocalModels([{ id: "default", label: "LM Studio Default" }, ...lmModels]);
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
          const preferred = list.some(p => p.id === storedID) ? storedID : saved;
          setWebProviderID(preferred);
          setWebLoggedIn(true);
        } else if (storedID && list.some(p => p.id === storedID)) {
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
      setActiveProvider(p);
      setModel(p.model);
      setCustomBaseUrl(p.baseUrl);
      setCustomModel(p.model);
      setShowApiKeyInput(false);
      setApiKeyInput("");
      // Save to backend
      await SetAPIProvider(p.name, p.baseUrl, "", p.model);
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

  const handleSend = () => {
    const text = input.trim();
    if (!text || isLoading) return;
    onSend(text, source);
    setInput("");
    textareaRef.current?.focus();
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

        {source === "local" && (
          <select
            className="ai-model-select"
            value={model}
            onChange={(e) => { setModel(e.target.value); SetModel(e.target.value).catch(() => {}); }}
          >
            {localModels.map((m) => (
              <option key={m.id} value={m.id}>{m.label}</option>
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
          onClick={() => onSend("__clear__", source)}
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
          <div style={{ display: "flex", gap: 6, justifyContent: "flex-end" }}>
            <button className="btn-ghost" onClick={() => setShowApiKeyInput(false)}>{t("ai.cancel")}</button>
            <button className="btn-primary" onClick={handleSaveApiKey}>{t("ai.save")}</button>
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

      <div className="ai-messages">
        {installLogs.length > 0 && (
          <div className="ai-install-logs">
            {installLogs.map((l, i) => <div key={i}>{l}</div>)}
          </div>
        )}
        {messages.length === 0 && (
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

        {messages.map((msg, idx) => (
          <div key={idx} className={`ai-message ${msg.role}`}>
            <MessageBody msg={msg} t={t} />
          </div>
        ))}

        {isLoading && (
          <div className="ai-message assistant">
            <div className="ai-message-role">AI</div>
            <div className="ai-message-body" style={{ color: "var(--text-muted)" }}>
              {t("ai.thinking")}
            </div>
          </div>
        )}

        <div ref={bottomRef} />
      </div>

      <div className="ai-input-area">
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
          <button
            className="btn-primary"
            onClick={handleSend}
            disabled={isLoading || !input.trim()}
            style={{ display: "flex", alignItems: "center", gap: 5 }}
          >
            <SendHorizontal size={13} />
            {t("ai.send")}
          </button>
        </div>
      </div>
    </div>
  );
}
