import React, { useEffect, useState } from "react";
import { useTranslation } from "../i18n";
import { X, RotateCcw, Save, ExternalLink, FolderOpen, Plug, Plus, Trash2, RefreshCw, Power, ChevronRight } from "lucide-react";
import { GetSettings, SaveSettings, ResetSettings, GetSettingsPath, GetCurrentVersion, CheckForUpdate } from "../bindings/config";
import { GetServers, ConnectServer, DisconnectServer, ToggleServer, SaveConfig, DeleteConfig, ListTools, GetConfigs, MCPServer, MCPConfig, MCPToolInfo } from "../bindings/mcp";

interface Settings {
  fontSize: number;
  tabSize: number;
  wordWrap: boolean;
  minimap: boolean;
  fontFamily: string;
  lineHeight: number;
  renderWhitespace: string;
  theme: string;
  lmStudioUrl: string;
  defaultModel: string;
  inlineCompletion: boolean;
  autoCheckUpdate: boolean;
  checkedVersion: string;
  // LSP
  lspBinDir: string;
  lspPaths: Record<string, string>;
}

interface Props {
  onClose: () => void;
}

type Tab = "editor" | "lsp" | "mcp" | "about";

// Use types from bindings/mcp
type MCPServerState = MCPServer;

const LSP_LANGS = [
  { id: "go",         label: "Go",         binary: "gopls" },
  { id: "typescript", label: "TypeScript",  binary: "typescript-language-server" },
  { id: "javascript", label: "JavaScript",  binary: "typescript-language-server" },
  { id: "python",     label: "Python",      binary: "pylsp" },
  { id: "rust",       label: "Rust",        binary: "rust-analyzer" },
  { id: "c",          label: "C",           binary: "clangd" },
  { id: "cpp",        label: "C++",         binary: "clangd" },
  { id: "html",       label: "HTML",        binary: "vscode-html-language-server" },
  { id: "css",        label: "CSS",         binary: "vscode-css-language-server" },
  { id: "json",       label: "JSON",        binary: "vscode-json-language-server" },
  { id: "yaml",       label: "YAML",        binary: "yaml-language-server" },
  { id: "lua",        label: "Lua",         binary: "lua-language-server" },
  { id: "bash",       label: "Bash/Shell",  binary: "bash-language-server" },
];

export default function SettingsModal({ onClose }: Props) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>("editor");
  const [settings, setSettings] = useState<Settings | null>(null);
  const [settingsPath, setSettingsPath] = useState("");
  const [version, setVersion] = useState("");
  const [updateInfo, setUpdateInfo] = useState<{ version: string; url: string; notes: string } | null>(null);
  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [saved, setSaved] = useState(false);
  // MCP tab state
  const [mcpServers, setMCPServers] = useState<MCPServerState[]>([]);
  const [mcpConfigs, setMCPConfigs] = useState<Record<string, MCPConfig>>({});
  const [mcpLoading, setMCPLoading] = useState(false);
  const [mcpAdding, setMCPAdding] = useState(false);
  const [mcpExpanded, setMCPExpanded] = useState<string | null>(null);
  // New server form state
  const [newServerName, setNewServerName] = useState("");
  const [newServerType, setNewServerType] = useState<"stdio" | "http" | "sse">("stdio");
  const [newServerCommand, setNewServerCommand] = useState("");
  const [newServerArgs, setNewServerArgs] = useState("");
  const [newServerURL, setNewServerURL] = useState("");

  useEffect(() => {
    GetSettings().then((s: any) => setSettings({
      ...s,
      lspBinDir: s.lspBinDir ?? "",
      lspPaths: s.lspPaths ?? {},
    }));
    GetSettingsPath().then(setSettingsPath);
    GetCurrentVersion().then(setVersion);
  }, []);

  const set = (key: keyof Settings, value: any) =>
    setSettings((s) => s ? { ...s, [key]: value } : s);

  const setLspPath = (lang: string, value: string) =>
    setSettings((s) => s ? { ...s, lspPaths: { ...s.lspPaths, [lang]: value } } : s);

  const handleSave = async () => {
    if (!settings) return;
    await SaveSettings(settings as any);
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
  };

  const handleReset = async () => {
    await ResetSettings();
    const s = await GetSettings() as any;
    setSettings({ ...s, lspBinDir: s.lspBinDir ?? "", lspPaths: s.lspPaths ?? {} });
  };

  const handleCheckUpdate = async () => {
    setCheckingUpdate(true);
    setUpdateInfo(null);
    try {
      const info = await CheckForUpdate() as any;
      setUpdateInfo(info ?? null);
    } finally {
      setCheckingUpdate(false);
    }
  };

  // MCP handlers
  const loadMCPServers = async () => {
    setMCPLoading(true);
    try {
      const servers = await GetServers();
      setMCPServers(servers ?? []);
      const configs = await GetConfigs();
      setMCPConfigs(configs ?? {});
    } finally {
      setMCPLoading(false);
    }
  };

  const handleToggleServer = async (name: string, enabled: boolean) => {
    await ToggleServer(name, enabled);
    await loadMCPServers();
  };

  const handleDeleteServer = async (name: string) => {
    if (!confirm(`Delete MCP server "${name}"?`)) return;
    await DeleteConfig(name);
    await loadMCPServers();
  };

  const handleAddServer = async () => {
    if (!newServerName.trim()) return;
    const cfg: MCPConfig = {
      Type: newServerType,
      Command: newServerType === "stdio" ? newServerCommand : undefined,
      Args: newServerType === "stdio" ? newServerArgs.split(/\s+/).filter(Boolean) : undefined,
      Url: newServerType !== "stdio" ? newServerURL : undefined,
    };
    await SaveConfig(newServerName.trim(), cfg);
    setNewServerName("");
    setNewServerCommand("");
    setNewServerArgs("");
    setNewServerURL("");
    setMCPAdding(false);
    await loadMCPServers();
    // Auto-connect
    await ConnectServer(newServerName.trim(), cfg);
    await loadMCPServers();
  };

  const handleConnectServer = async (name: string, cfg: MCPConfig) => {
    await ConnectServer(name, cfg);
    await loadMCPServers();
  };

  // Load MCP servers when switching to mcp tab
  useEffect(() => {
    if (tab === "mcp" && mcpServers.length === 0) {
      loadMCPServers();
    }
  }, [tab]);

  if (!settings) return null;

  const effectiveBinDir = settings.lspBinDir || t("settings.defaultBinDir");

  return (
    <div className="settings-overlay" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="settings-modal">
        <div className="settings-header">
          <span className="settings-title">{t("settings.title")}</span>
          <button className="settings-close" onClick={onClose}><X size={16} /></button>
        </div>

        <div className="settings-layout">
          <div className="settings-sidebar">
            {(["editor", "lsp", "mcp", "about"] as Tab[]).map((tabKey) => (
              <button key={tabKey} className={`settings-nav-item${tab === tabKey ? " active" : ""}`} onClick={() => setTab(tabKey)}>
                {{ editor: t("settings.editor"), lsp: t("settings.lsp"), mcp: t("settings.mcp"), about: t("settings.about") }[tabKey]}
              </button>
            ))}
          </div>

          <div className="settings-content">
            {tab === "editor" && (
              <>
                <div className="settings-section-title">{t("settings.fontSection")}</div>
                <SettingRow label={t("settings.fontFamily")}>
                  <input className="settings-input" value={settings.fontFamily}
                    onChange={(e) => set("fontFamily", e.target.value)} />
                </SettingRow>
                <SettingRow label={t("settings.fontSize")}>
                  <input className="settings-input narrow" type="number" min={10} max={32}
                    value={settings.fontSize} onChange={(e) => set("fontSize", +e.target.value)} />
                </SettingRow>
                <SettingRow label={t("settings.lineHeight")}>
                  <input className="settings-input narrow" type="number" min={14} max={40}
                    value={settings.lineHeight} onChange={(e) => set("lineHeight", +e.target.value)} />
                </SettingRow>

                <div className="settings-section-title">{t("settings.indentSection")}</div>
                <SettingRow label={t("settings.tabWidth")}>
                  <select className="settings-select" value={settings.tabSize}
                    onChange={(e) => set("tabSize", +e.target.value)}>
                    {[2, 4, 8].map((n) => <option key={n} value={n}>{n}</option>)}
                  </select>
                </SettingRow>

                <div className="settings-section-title">{t("settings.displaySection")}</div>
                <SettingRow label={t("settings.wordWrap")}>
                  <Toggle value={settings.wordWrap} onChange={(v) => set("wordWrap", v)} />
                </SettingRow>
                <SettingRow label={t("settings.minimap")}>
                  <Toggle value={settings.minimap} onChange={(v) => set("minimap", v)} />
                </SettingRow>
                <SettingRow label={t("settings.whitespace")}>
                  <select className="settings-select" value={settings.renderWhitespace}
                    onChange={(e) => set("renderWhitespace", e.target.value)}>
                    {["none", "boundary", "selection", "trailing", "all"].map((v) => (
                      <option key={v} value={v}>{v}</option>
                    ))}
                  </select>
                </SettingRow>
              </>
            )}

            {tab === "lsp" && (
              <>
                <div className="settings-section-title">{t("settings.installDirSection")}</div>
                <div className="settings-hint">
                  {t("settings.installDirHint")}
                </div>
                <SettingRow label={t("settings.lspInstallDir")}>
                  <input
                    className="settings-input"
                    value={settings.lspBinDir}
                    placeholder={t("settings.lspInstallPlaceholder")}
                    onChange={(e) => set("lspBinDir", e.target.value)}
                  />
                </SettingRow>
                <div className="settings-hint lsp-bindir-hint">
                  <FolderOpen size={11} /> {t("settings.effectivePath")}:<code>{effectiveBinDir}</code>
                </div>

                <div className="settings-section-title" style={{ marginTop: 20 }}>{t("settings.langPathSection")}</div>
                <div className="settings-hint">
                  {t("settings.langPathHint")}
                </div>
                {LSP_LANGS.map(({ id, label, binary }) => (
                  <SettingRow key={id} label={label}>
                    <input
                      className="settings-input"
                      value={settings.lspPaths[id] ?? ""}
                      placeholder={t("settings.autoFind", { binary })}
                      onChange={(e) => setLspPath(id, e.target.value)}
                    />
                  </SettingRow>
                ))}
              </>
            )}

            {tab === "mcp" && (
              <>
                <div className="settings-section-title">{t("settings.mcp.title")}</div>
                <div className="settings-hint" style={{ marginBottom: 16 }}>
                  {t("settings.mcp.hint")}
                </div>

                {mcpLoading ? (
                  <div className="settings-mcp-loading">{t("settings.mcp.loading")}</div>
                ) : (
                  <div className="settings-mcp-list">
                    {mcpServers.length === 0 && !mcpAdding && (
                      <div className="settings-mcp-empty">{t("settings.mcp.empty")}</div>
                    )}

                    {mcpServers.map((server) => {
                      const isExpanded = mcpExpanded === server.Name;
                      return (
                        <div key={server.Name} className="settings-mcp-server">
                          <div className="settings-mcp-server-header">
                            <button
                              className="settings-mcp-toggle"
                              onClick={() => setMCPExpanded(isExpanded ? null : server.Name)}
                            >
                              <ChevronRight size={14} className={`settings-mcp-chevron${isExpanded ? " open" : ""}`} />
                            </button>
                            <span className="settings-mcp-server-name">
                              <Plug size={13} />
                              {server.Name}
                            </span>
                            <span className={`settings-mcp-status ${server.Type}`}>
                              {server.Type === "connected" ? "🟢" : server.Type === "pending" ? "🟡" : "⚪"}
                            </span>
                            <button
                              className={`settings-mcp-power ${server.Type === "connected" ? "on" : ""}`}
                              onClick={() => handleToggleServer(server.Name, server.Type !== "connected")}
                              title={server.Type === "connected" ? t("settings.mcp.disable") : t("settings.mcp.enable")}
                            >
                              <Power size={14} />
                            </button>
                            <button
                              className="settings-mcp-delete"
                              onClick={() => handleDeleteServer(server.Name)}
                              title={t("settings.mcp.delete")}
                            >
                              <Trash2 size={14} />
                            </button>
                          </div>

                          {isExpanded && (
                            <div className="settings-mcp-server-details">
                              <div className="settings-mcp-info-row">
                                <span className="settings-mcp-info-label">{t("settings.mcp.transport")}:</span>
                                <span>{server.Config?.Type ?? "stdio"}</span>
                              </div>
                              {server.Config?.Command && (
                                <div className="settings-mcp-info-row">
                                  <span className="settings-mcp-info-label">{t("settings.mcp.command")}:</span>
                                  <code>{server.Config.Command}</code>
                                </div>
                              )}
                              {server.Config?.Url && (
                                <div className="settings-mcp-info-row">
                                  <span className="settings-mcp-info-label">{t("settings.mcp.url")}:</span>
                                  <code>{server.Config.Url}</code>
                                </div>
                              )}
                              <div className="settings-mcp-info-row">
                                <span className="settings-mcp-info-label">{t("settings.mcp.tools")}:</span>
                                <span>{server.Tools?.length ?? 0}</span>
                              </div>
                              {server.Tools && server.Tools.length > 0 && (
                                <div className="settings-mcp-tools">
                                  {server.Tools.map((tool) => (
                                    <div key={tool.Name} className="settings-mcp-tool">
                                      <span className="settings-mcp-tool-name">{tool.Name}</span>
                                      <span className="settings-mcp-tool-desc">{tool.Description}</span>
                                    </div>
                                  ))}
                                </div>
                              )}
                              {server.Error && (
                                <div className="settings-mcp-error">
                                  {t("settings.mcp.error")}: {server.Error}
                                </div>
                              )}
                              {server.Type !== "connected" && server.Type !== "pending" && (
                                <button
                                  className="settings-btn"
                                  style={{ marginTop: 8 }}
                                  onClick={() => handleConnectServer(server.Name, server.Config as MCPConfig)}
                                >
                                  {t("settings.mcp.connect")}
                                </button>
                              )}
                            </div>
                          )}
                        </div>
                      );
                    })}
                  </div>
                )}

                {mcpAdding ? (
                  <div className="settings-mcp-add-form">
                    <div className="settings-section-title" style={{ marginTop: 16 }}>{t("settings.mcp.addServer")}</div>
                    <SettingRow label={t("settings.mcp.serverName")}>
                      <input
                        className="settings-input"
                        value={newServerName}
                        placeholder={t("settings.mcp.serverNamePlaceholder")}
                        onChange={(e) => setNewServerName(e.target.value)}
                      />
                    </SettingRow>
                    <SettingRow label={t("settings.mcp.transportType")}>
                      <select
                        className="settings-select"
                        value={newServerType}
                        onChange={(e) => setNewServerType(e.target.value as "stdio" | "http" | "sse")}
                      >
                        <option value="stdio">stdio</option>
                        <option value="http">HTTP</option>
                        <option value="sse">SSE</option>
                      </select>
                    </SettingRow>
                    {newServerType === "stdio" ? (
                      <>
                        <SettingRow label={t("settings.mcp.command")}>
                          <input
                            className="settings-input"
                            value={newServerCommand}
                            placeholder="npx"
                            onChange={(e) => setNewServerCommand(e.target.value)}
                          />
                        </SettingRow>
                        <SettingRow label={t("settings.mcp.args")}>
                          <input
                            className="settings-input"
                            value={newServerArgs}
                            placeholder={t("settings.mcp.argsPlaceholder")}
                            onChange={(e) => setNewServerArgs(e.target.value)}
                          />
                        </SettingRow>
                      </>
                    ) : (
                      <SettingRow label={t("settings.mcp.url")}>
                        <input
                          className="settings-input"
                          value={newServerURL}
                          placeholder="http://localhost:8080"
                          onChange={(e) => setNewServerURL(e.target.value)}
                        />
                      </SettingRow>
                    )}
                    <div className="settings-mcp-form-actions">
                      <button className="settings-btn ghost" onClick={() => setMCPAdding(false)}>
                        {t("settings.mcp.cancel")}
                      </button>
                      <button className="settings-btn primary" onClick={handleAddServer}>
                        {t("settings.mcp.add")}
                      </button>
                    </div>
                  </div>
                ) : (
                  <button
                    className="settings-btn"
                    style={{ marginTop: 16 }}
                    onClick={() => setMCPAdding(true)}
                  >
                    <Plus size={14} /> {t("settings.mcp.addServer")}
                  </button>
                )}

                <div className="settings-section-title" style={{ marginTop: 24 }}>{t("settings.mcp.quickActions")}</div>
                <div className="settings-mcp-quick-actions">
                  <button className="settings-btn" onClick={loadMCPServers}>
                    <RefreshCw size={13} /> {t("settings.mcp.refresh")}
                  </button>
                </div>
              </>
            )}

            {tab === "about" && (
              <>
                <div className="settings-about">
                  <div className="settings-about-name">{t("settings.appName")}</div>
                  <div className="settings-about-version">{t("settings.version")} {version}</div>
                  <div className="settings-about-path" title={settingsPath}>
                    {t("settings.configFile")}: {settingsPath}
                  </div>
                  <button className="settings-btn" onClick={handleCheckUpdate} disabled={checkingUpdate}>
                    {checkingUpdate ? t("settings.checking") : t("settings.checkUpdate")}
                  </button>
                  {updateInfo && (
                    <div className="settings-update-info">
                      <span>{t("settings.newVersion")} {updateInfo.version}</span>
                      <a href={updateInfo.url} target="_blank" rel="noopener" className="settings-update-link">
                        {t("settings.releaseNotes")} <ExternalLink size={11} />
                      </a>
                    </div>
                  )}
                  {updateInfo === null && !checkingUpdate && version && (
                    <div className="settings-up-to-date">{t("settings.upToDate")}</div>
                  )}
                </div>
              </>
            )}
          </div>
        </div>

        <div className="settings-footer">
          <button className="settings-btn ghost" onClick={handleReset} title={t("settings.resetTitle")}>
            <RotateCcw size={13} /> {t("settings.reset")}
          </button>
          <div style={{ flex: 1 }} />
          {saved && <span className="settings-saved-hint">{t("settings.saved")}</span>}
          <button className="settings-btn primary" onClick={handleSave}>
            <Save size={13} /> {t("settings.save")}
          </button>
        </div>
      </div>
    </div>
  );
}

function SettingRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="settings-row">
      <span className="settings-label">{label}</span>
      <div className="settings-control">{children}</div>
    </div>
  );
}

function Toggle({ value, onChange }: { value: boolean; onChange: (v: boolean) => void }) {
  return (
    <button className={`settings-toggle${value ? " on" : ""}`} onClick={() => onChange(!value)}>
      <span className="settings-toggle-thumb" />
    </button>
  );
}

