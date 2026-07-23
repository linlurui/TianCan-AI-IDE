import React, { useEffect, useState, useRef, useCallback } from "react";
import { StartAndGetPort } from "../bindings/terminal";
import { AgentRun, AgentRunAPI, AgentRunWeb, StartAgentStream, SmartAgentRun } from "../bindings/ai";
import { ReadFile, WriteFile, SelectDirectory } from "../bindings/filesystem";
import { Events } from "@wailsio/runtime";
import AIPanel, { type Message, type ModelSource } from "./AIPanel";
import DebugPanel from "./DebugPanel";
import SplitEditorArea from "./SplitEditorArea";
import { useEditorStore } from "../store/editorStore";
import "../App.css";

// Detect which window mode we're in from the URL
function getWindowMode(): string {
  const params = new URLSearchParams(window.location.search);
  return params.get("window") || "main";
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
  const streamRef = useRef<any>(null);

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
      return;
    }
    setMessages((p) => [...p, { role: "user", content: text }]);
    setLoading(true);

    const rootPath = ""; // AI window doesn't have a project context; tools will use default

    try {
      if (source === "auto") {
        const result = await SmartAgentRun(text, rootPath, 20);
        const rawMsgs = (result.messages ?? []).filter((m: any) => m.role !== "user");
        const agentMsgs: Message[] = [];
        for (let i = 0; i < rawMsgs.length; i++) {
          const m = rawMsgs[i];
          const next = rawMsgs[i + 1];
          if (m.role === "assistant" && next && (next.role === "tool_call" || next.role === "tool_result")) {
            agentMsgs.push({ role: "thinking", content: m.content });
          } else {
            agentMsgs.push({ role: m.role as Message["role"], content: m.content, name: m.name });
          }
        }
        setMessages((p) => [...p, ...agentMsgs]);
      } else if (source === "local") {
        // Streaming for local
        if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
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
              setMessages((p) => {
                const updated = [...p];
                const last = updated[updated.length - 1];
                if (last && last.role === "assistant") {
                  updated[updated.length - 1] = { ...last, role: "thinking", content: last.content };
                }
                return [...updated, { role: "tool_call", content: eContent, name: data?.name }];
              });
              break;
            case "tool_result":
              setMessages((p) => [...p, { role: "tool_result", content: eContent, name: data?.name }]);
              setMessages((p) => [...p, { role: "assistant", content: "" }]);
              break;
            case "done":
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
              setLoading(false);
              Events.Off("agent:stream");
              streamRef.current = null;
              break;
            case "error":
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
          result = await AgentRunWeb(text, rootPath, 20);
        } else {
          result = await AgentRunAPI(text, rootPath, 20);
        }
        const rawMsgs = (result.messages ?? []).filter((m: any) => m.role !== "user");
        const agentMsgs: Message[] = [];
        for (let i = 0; i < rawMsgs.length; i++) {
          const m = rawMsgs[i];
          const next = rawMsgs[i + 1];
          if (m.role === "assistant" && next && (next.role === "tool_call" || next.role === "tool_result")) {
            agentMsgs.push({ role: "thinking", content: m.content });
          } else {
            agentMsgs.push({ role: m.role as Message["role"], content: m.content, name: m.name });
          }
        }
        setMessages((p) => [...p, ...agentMsgs]);
      }
    } catch (err: any) {
      setMessages((p) => [...p, { role: "assistant", content: `❌ ${err?.message ?? err}` }]);
    }
    setLoading(false);
  }, []);

  return (
    <div className="ide-root" style={{ height: "100vh" }}>
      <AIPanel messages={messages} onSend={handleSend} isLoading={loading} fullWidth />
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
