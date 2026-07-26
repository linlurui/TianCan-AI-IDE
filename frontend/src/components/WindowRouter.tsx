import React, { useEffect, useState, useRef, useCallback } from "react";
import { StartAndGetPort } from "@bindings/terminal";
import { AgentRun, AgentRunAPI, AgentRunWeb, StartAgentStream, SmartAgentRun } from "@bindings/ai";
import { ReadFile, WriteFile, SelectDirectory } from "@bindings/filesystem";
import { Events } from "@wailsio/runtime";
import AIPanel, { type Message, type ModelSource, type ActivityItem } from "./AIPanel";
import DebugPanel from "./DebugPanel";
import SplitEditorArea from "./SplitEditorArea";
import { useEditorStore } from "../store/editorStore";
import "../App.css";

// Detect which window mode we're in from the URL
function getWindowMode(): string {
  const params = new URLSearchParams(window.location.search);
  return params.get("window") || "main";
}

function normalizeAgentMessages(rawMessages: any[], pushActivity?: (kind: ActivityItem["kind"], text: string, detail?: string) => void): Message[] {
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
      pushActivity?.("thinking", "模型正在规划下一步", "检测到模型准备使用工具，等待工具执行结果");
    } else if (role === "tool_call") {
      pushActivity?.("tool", `调用工具 ${name || "tool"}`);
    } else if (role === "tool_result" || role === "tool") {
      pushActivity?.("result", `工具返回 ${name || "结果"}`, content.slice(0, 160));
    } else if (role === "thinking") {
      pushActivity?.("thinking", "模型正在分析上下文", "已收到模型中间分析信号，继续等待最终回复");
    } else {
      agentMsgs.push({ role: "assistant", content });
    }
  }

  return agentMsgs;
}

// ─── Terminal Window ──────────────────────────────────────────────────────
function TerminalWindow() {
  const [terminalPort, setTerminalPort] = useState(0);
  const [wsUrl, setWsUrl] = useState("");

  useEffect(() => {
    StartAndGetPort().then((port: number) => {
      setTerminalPort(port);
      setWsUrl(`ws://127.0.0.1:${port}`);
    }).catch(console.error);
  }, []);

  return (
    <div style={{ height: "100vh", display: "flex", flexDirection: "column", background: "#1a1a2e" }}>
      <div style={{ padding: "6px 12px", borderBottom: "1px solid #333", display: "flex", alignItems: "center", gap: 8 }}>
        <span style={{ color: "#aaa", fontSize: 12, fontWeight: 600 }}>终端</span>
      </div>
      <div style={{ flex: 1, position: "relative" }}>
        {wsUrl && (
          <iframe
            src={`http://127.0.0.1:${terminalPort}`}
            style={{ width: "100%", height: "100%", border: "none" }}
            title="Terminal"
          />
        )}
        {!wsUrl && (
          <div style={{ padding: 20, color: "#888", textAlign: "center" }}>正在启动终端...</div>
        )}
      </div>
    </div>
  );
}

// ─── AI Window — full AIPanel with self-managed state ────────────────────
function AIWindow() {
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(false);
  const [activityItems, setActivityItems] = useState<ActivityItem[]>([]);
  const streamRef = useRef<any>(null);

  const pushActivity = useCallback((kind: ActivityItem["kind"], text: string, detail?: string) => {
    setActivityItems([{ id: Date.now() + Math.random(), kind, text, detail }]);
  }, []);

  useEffect(() => {
    const off = Events.On("ai:provider", (evt: any) => {
      const data = evt?.data ?? evt;
      const provider = data?.provider || "模型";
      const detail = data?.detail || "";
      const status = data?.status || "attempt";
      if (status === "success") {
        pushActivity("thinking", "正在整理回复", "模型已返回内容，正在转换为聊天消息");
      } else if (status === "error") {
        pushActivity("thinking", "正在切换可用模型", detail || `当前通道 ${provider} 没有可用回复，继续尝试下一个`);
      } else if (status === "skip") {
        pushActivity("thinking", "继续寻找可用模型", `${provider} 暂不可用，跳过并检查后续通道`);
      } else {
        pushActivity("thinking", "正在等待模型推理", `已把当前请求交给 ${provider}`);
      }
    });
    return () => { off(); };
  }, [pushActivity]);

  // Cleanup streaming listener on unmount
  useEffect(() => {
    return () => {
      if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
    };
  }, []);

  const handleSend = useCallback(async (text: string, source: ModelSource) => {
    if (text === "__clear__") {
      if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
      setMessages([]);
      setActivityItems([]);
      return;
    }
    setMessages((p) => [
      ...p,
      { role: "user", content: text },
    ]);
    setLoading(true);
    setActivityItems([]);

    const rootPath = ""; // AI window doesn't have a project context; tools will use default
    pushActivity("thinking", "准备上下文", source === "auto" ? "Auto 将按可用模型自动尝试" : `使用 ${source} 模式处理当前请求`);

    try {
      if (source === "auto") {
        pushActivity("route", "选择 Auto 路由", "独立 AI 窗口暂无项目目录");
        const result = await SmartAgentRun(text, rootPath, 20);
        const agentMsgs = normalizeAgentMessages(result.messages ?? [], pushActivity);
        setMessages((p) => [...p, ...agentMsgs]);
        pushActivity(result.error ? "error" : "success", result.error ? "执行结束但有错误" : "已生成回复");
      } else if (source === "local") {
        // Streaming for local
        if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
        pushActivity("route", "连接本地模型", "等待流式响应");
        setMessages((p) => [...p, { role: "assistant", content: "" }]);

        const handler = (evt: any) => {
          const data = evt?.data ?? evt;
          const eType = data?.type ?? "";
          const eContent = data?.content ?? "";
          switch (eType) {
            case "token":
              setMessages((p) => {
                const updated = [...p];
                const last = updated[updated.length - 1];
                if (last && last.role === "assistant") {
                  updated[updated.length - 1] = { ...last, content: last.content + eContent };
                }
                return updated;
              });
              break;
            case "tool_call":
              pushActivity("tool", `调用工具 ${data?.name || "tool"}`, eContent);
              setMessages((p) => {
                const updated = [...p];
                const last = updated[updated.length - 1];
                if (last && last.role === "assistant") {
                  updated[updated.length - 1] = { ...last, role: "thinking", content: last.content };
                }
                return updated;
              });
              break;
            case "tool_result":
              pushActivity("result", `工具完成 ${data?.name || "tool"}`, eContent.slice(0, 120));
              setMessages((p) => [...p, { role: "assistant", content: "" }]);
              break;
            case "done":
              pushActivity("success", "任务完成");
              setMessages((p) => {
                const updated = [...p];
                const last = updated[updated.length - 1];
                if (last && last.role === "assistant" && !last.content.trim()) {
                  updated.pop();
                  updated.push({ role: "assistant", content: eContent });
                }
                return updated;
              });
              setLoading(false);
              Events.Off("agent:stream");
              streamRef.current = null;
              break;
            case "stream_end":
              pushActivity("success", "响应结束");
              setLoading(false);
              Events.Off("agent:stream");
              streamRef.current = null;
              break;
            case "error":
              pushActivity("error", "执行失败", eContent);
              setMessages((p) => [...p, { role: "assistant", content: `❌ ${eContent}` }]);
              setLoading(false);
              Events.Off("agent:stream");
              streamRef.current = null;
              break;
          }
        };
        Events.On("agent:stream", handler);
        streamRef.current = handler;
        await StartAgentStream(text, rootPath, 20);
      } else {
        // API / Web blocking
        let result;
        if (source === "web") {
          pushActivity("route", "使用 Web AI", "等待网页登录态响应");
          result = await AgentRunWeb(text, rootPath, 20);
        } else {
          pushActivity("route", "使用 API", "等待接口响应");
          result = await AgentRunAPI(text, rootPath, 20);
        }
        const agentMsgs = normalizeAgentMessages(result.messages ?? [], pushActivity);
        setMessages((p) => [...p, ...agentMsgs]);
        pushActivity(result.error ? "error" : "success", result.error ? "执行结束但有错误" : "已生成回复");
      }
    } catch (err: any) {
      pushActivity("error", "请求失败", err?.message ?? String(err));
      setMessages((p) => [...p, { role: "assistant", content: `❌ ${err?.message ?? err}` }]);
    }
    setLoading(false);
  }, [pushActivity]);

  return (
    <div className="ide-root" style={{ height: "100vh" }}>
      <AIPanel messages={messages} onSend={handleSend} isLoading={loading} activityItems={activityItems} fullWidth />
    </div>
  );
}

// ─── Debug Window — full DebugPanel ──────────────────────────────────────
function DebugWindow() {
  return (
    <div className="ide-root" style={{ height: "100vh" }}>
      <DebugPanel
        rootPath={null}
        activeFile={null}
        breakpoints={[]}
        projectType={null}
      />
    </div>
  );
}

// ─── Editor Window — real Monaco editor via SplitEditorArea ──────────────
function EditorWindow() {
  const [rootPath, setRootPath] = useState<string>("");
  const { openTab, tabs, activeTab } = useEditorStore();

  const handleOpenProject = async () => {
    try {
      const dir = await SelectDirectory();
      if (dir) setRootPath(dir);
    } catch {
      // user cancelled
    }
  };

  const handleOpenFile = async () => {
    // Simple file open: read file and add as tab
    const path = prompt("输入文件路径:");
    if (!path) return;
    try {
      const content = await ReadFile(path);
      const name = path.split("/").pop() || path;
      openTab({ path, name, content, isDirty: false });
    } catch (err: any) {
      alert(`无法打开文件: ${err?.message ?? err}`);
    }
  };

  const handleSave = async (path: string) => {
    const tab = tabs.find((t: any) => t.path === path);
    if (!tab) return;
    try {
      await WriteFile(path, tab.content);
      useEditorStore.getState().markTabSaved(path);
    } catch (err: any) {
      alert(`保存失败: ${err?.message ?? err}`);
    }
  };

  return (
    <div className="ide-root" style={{ height: "100vh", display: "flex", flexDirection: "column" }}>
      <div style={{ padding: "4px 10px", borderBottom: "1px solid var(--border)", display: "flex", alignItems: "center", gap: 8, background: "var(--bg-secondary)", flexShrink: 0 }}>
        <span style={{ color: "var(--text-muted)", fontSize: 12, fontWeight: 600 }}>编辑器</span>
        <button className="toolbar-btn" onClick={handleOpenProject} title="打开项目目录" style={{ fontSize: 11, padding: "2px 8px" }}>
          打开项目
        </button>
        <button className="toolbar-btn" onClick={handleOpenFile} title="打开文件" style={{ fontSize: 11, padding: "2px 8px" }}>
          打开文件
        </button>
        {rootPath && <span style={{ color: "var(--text-secondary)", fontSize: 11 }}>{rootPath}</span>}
      </div>
      <div style={{ flex: 1, minHeight: 0 }}>
        <SplitEditorArea
          rootPath={rootPath}
          onSave={handleSave}
        />
      </div>
    </div>
  );
}

// ─── Main Router ──────────────────────────────────────────────────────────
export default function WindowRouter({ children }: { children: React.ReactNode }) {
  const mode = getWindowMode();

  // In main/unified mode, render the full App as-is
  if (mode === "main") {
    return <>{children}</>;
  }

  // In multi-window mode, render only the relevant panel
  switch (mode) {
    case "terminal":
      return <TerminalWindow />;
    case "ai":
      return <AIWindow />;
    case "debug":
      return <DebugWindow />;
    case "editor":
      return <EditorWindow />;
    default:
      return <>{children}</>;
  }
}
