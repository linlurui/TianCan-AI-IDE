import React, { useState, useEffect, useCallback } from "react";
import { Box, Download, Key, Server, Play, Square, RefreshCw, Settings } from "lucide-react";
import {
  SandboxProviderStatus, SandboxList, SandboxAutoInstall,
  SandboxSetE2BAPIKey, SandboxSetProvider, SandboxStop, SandboxCreate,
} from "../../bindings/github.com/rocky233/tiancan-ai-ide/backend/ai/service";

interface ProviderStatus {
  name: string;
  installed: boolean;
  running: boolean;
  configured: boolean;
  version?: string;
  installPath?: string;
}

interface SandboxInfo {
  id: string;
  name: string;
  state: string;
  image: string;
}

export default function SandboxPanel() {
  const [providers, setProviders] = useState<ProviderStatus[]>([]);
  const [sandboxes, setSandboxes] = useState<SandboxInfo[]>([]);
  const [e2bKey, setE2bKey] = useState("");
  const [e2bKeySaved, setE2bKeySaved] = useState(false);
  const [installLogs, setInstallLogs] = useState<string[]>([]);
  const [installing, setInstalling] = useState("");
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const ps = await SandboxProviderStatus();
      setProviders(ps || []);
    } catch { setProviders([]); }
    try {
      const list = await SandboxList();
      setSandboxes((list?.filter(Boolean) as SandboxInfo[]) || []);
    } catch { setSandboxes([]); }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  const handleInstall = async (name: string) => {
    setInstalling(name);
    setInstallLogs([]);
    setError("");
    try {
      const logs = await SandboxAutoInstall(name);
      setInstallLogs(logs || []);
      await refresh();
    } catch (e: any) {
      setError(e?.message || String(e));
    }
    setInstalling("");
  };

  const handleSetE2BKey = async () => {
    if (!e2bKey.trim()) return;
    try {
      await SandboxSetE2BAPIKey(e2bKey.trim());
      setE2bKeySaved(true);
      await refresh();
      setTimeout(() => setE2bKeySaved(false), 2000);
    } catch (e: any) {
      setError(e?.message || String(e));
    }
  };

  const handleSetProvider = async (name: string) => {
    try {
      await SandboxSetProvider(name);
      await refresh();
    } catch (e: any) {
      setError(e?.message || String(e));
    }
  };

  const handleStop = async (id: string) => {
    try {
      await SandboxStop(id);
      await refresh();
    } catch (e: any) {
      setError(e?.message || String(e));
    }
  };

  const providerIcon = (name: string) => {
    switch (name) {
      case "docker": return <Box size={14} />;
      case "daytona": return <Server size={14} />;
      case "e2b": return <Key size={14} />;
      default: return <Settings size={14} />;
    }
  };

  const statusColor = (p: ProviderStatus) => {
    if (p.running && p.configured) return "#4caf50";
    if (p.installed && !p.running) return "#ff9800";
    return "#e55";
  };

  return (
    <div style={{ padding: 12, fontSize: 13, height: "100%", overflow: "auto" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
        <Box size={16} />
        <strong>沙箱管理</strong>
        <div style={{ flex: 1 }} />
        <button className="ai-install-btn" onClick={refresh} style={{ padding: "2px 8px" }}>
          <RefreshCw size={12} />
        </button>
      </div>

      {error && <div style={{ color: "#e55", fontSize: 11, marginBottom: 8 }}>❌ {error}</div>}

      {/* Provider statuses */}
      <div style={{ marginBottom: 12 }}>
        <h4 style={{ margin: "0 0 6px 0", fontSize: 12 }}>沙箱提供者</h4>
        {providers.map((p) => (
          <div
            key={p.name}
            style={{
              padding: "8px 10px", marginBottom: 4,
              border: "1px solid #333", borderRadius: 6,
              background: "#1a1a2e",
            }}
          >
            <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
              {providerIcon(p.name)}
              <strong style={{ flex: 1 }}>{p.name}</strong>
              <span style={{ fontSize: 10, color: statusColor(p) }}>
                {p.running && p.configured ? "✓ 可用" : p.installed ? "⚠ 未运行" : "✗ 未安装"}
              </span>
            </div>
            {p.version && <div style={{ fontSize: 10, color: "#888", marginTop: 2 }}>版本: {p.version}</div>}
            {p.installPath && <div style={{ fontSize: 10, color: "#666" }}>路径: {p.installPath}</div>}

            <div style={{ marginTop: 4, display: "flex", gap: 6 }}>
              {!p.installed && p.name !== "e2b" && (
                <button
                  className="ai-install-btn"
                  onClick={() => handleInstall(p.name)}
                  disabled={!!installing}
                  style={{ padding: "2px 8px", fontSize: 11 }}
                >
                  <Download size={10} /> {installing === p.name ? "安装中..." : "自动安装"}
                </button>
              )}
              {p.installed && p.configured && (
                <button
                  className="ai-install-btn"
                  onClick={() => handleSetProvider(p.name)}
                  style={{ padding: "2px 8px", fontSize: 11 }}
                >
                  <Play size={10} /> 选择此提供者
                </button>
              )}
            </div>
          </div>
        ))}
      </div>

      {/* E2B API Key setting */}
      <div style={{ marginBottom: 12, border: "1px solid #333", borderRadius: 6, padding: 10 }}>
        <h4 style={{ margin: "0 0 6px 0", fontSize: 12 }}>
          <Key size={12} /> E2B API Key 设置
        </h4>
        <div style={{ display: "flex", gap: 6 }}>
          <input
            type="password"
            value={e2bKey}
            onChange={(e) => setE2bKey(e.target.value)}
            placeholder="输入 E2B API Key"
            style={{
              flex: 1, fontSize: 12, padding: "4px 8px",
              background: "#1a1a2e", color: "#e0e0e0",
              border: "1px solid #333", borderRadius: 4,
            }}
          />
          <button className="ai-install-btn" onClick={handleSetE2BKey} style={{ padding: "4px 10px", fontSize: 11 }}>
            {e2bKeySaved ? "✓ 已保存" : "保存"}
          </button>
        </div>
        <div style={{ fontSize: 10, color: "#888", marginTop: 4 }}>
          获取 API Key: <a href="https://e2b.dev" target="_blank" rel="noreferrer" style={{ color: "#4a9eff" }}>e2b.dev</a>
        </div>
      </div>

      {/* Install logs */}
      {installLogs.length > 0 && (
        <div style={{ marginBottom: 12, border: "1px solid #333", borderRadius: 6, padding: 10 }}>
          <h4 style={{ margin: "0 0 4px 0", fontSize: 12 }}>安装日志</h4>
          {installLogs.map((log, i) => (
            <div key={i} style={{ fontSize: 11, color: "#aaa", fontFamily: "monospace" }}>{log}</div>
          ))}
        </div>
      )}

      {/* Active sandboxes */}
      <div>
        <h4 style={{ margin: "0 0 6px 0", fontSize: 12 }}>活跃沙箱</h4>
        {sandboxes.length === 0 && (
          <div style={{ color: "#888", fontSize: 11 }}>暂无活跃沙箱</div>
        )}
        {sandboxes.map((sb) => (
          <div
            key={sb.id}
            style={{
              padding: "6px 10px", marginBottom: 4,
              border: "1px solid #333", borderRadius: 6,
              background: "#1a1a2e",
              display: "flex", alignItems: "center", gap: 6,
            }}
          >
            <strong style={{ flex: 1, fontSize: 11 }}>{sb.name}</strong>
            <span style={{ fontSize: 10, color: sb.state === "running" ? "#4caf50" : "#ff9800" }}>
              {sb.state}
            </span>
            <button
              onClick={() => handleStop(sb.id)}
              style={{ background: "none", border: "none", cursor: "pointer", padding: 2 }}
            >
              <Square size={10} color="#e55" />
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
