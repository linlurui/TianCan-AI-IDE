import React, { useEffect, useState } from "react";
import { useTranslation } from "../i18n";
import { X, RotateCcw, Save, ExternalLink, FolderOpen } from "lucide-react";
import { GetSettings, SaveSettings, ResetSettings, GetSettingsPath, GetCurrentVersion, CheckForUpdate } from "../bindings/config";

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

type Tab = "editor" | "ai" | "lsp" | "about";

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
            {(["editor", "ai", "lsp", "about"] as Tab[]).map((tabKey) => (
              <button key={tabKey} className={`settings-nav-item${tab === tabKey ? " active" : ""}`} onClick={() => setTab(tabKey)}>
                {{ editor: t("settings.editor"), ai: t("settings.ai"), lsp: t("settings.lsp"), about: t("settings.about") }[tabKey]}
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

            {tab === "ai" && (
              <>
                <div className="settings-section-title">🤖 AI Provider</div>

                <ProviderSelector />

                <div className="settings-section-title" style={{ marginTop: 24 }}>⚙️ 自定义 API</div>
                <CustomAPIForm />
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

// ─── Provider Selector Component ────────────────────────────────────

interface Provider {
  id: string;
  name: string;
  type: "cli" | "desktop" | "api" | "local";
  status: "available" | "unavailable" | "needs_login" | "error";
  icon?: string;
  color?: string;
  hasQuota?: boolean;
}

function ProviderSelector() {
  const { t } = useTranslation();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [activeProvider, setActiveProvider] = useState<string>("");
  const [discovering, setDiscovering] = useState(false);

  useEffect(() => {
    loadProviders();
  }, []);

  const loadProviders = async () => {
    try {
      // 调用后端发现 Provider
      const list = await (window as any).go.ai.Service.ListProviders();
      setProviders(list || []);

      // 获取当前活跃的 Provider
      const active = await (window as any).go.ai.Service.GetActiveProvider();
      if (active) {
        setActiveProvider(active.id);
      }
    } catch (e) {
      console.error("Failed to load providers:", e);
    }
  };

  const handleDiscover = async () => {
    setDiscovering(true);
    try {
      await (window as any).go.ai.Service.DiscoverProviders();
      await loadProviders();
    } catch (e) {
      console.error("Discovery failed:", e);
    } finally {
      setDiscovering(false);
    }
  };

  const handleSelect = async (id: string) => {
    try {
      await (window as any).go.ai.Service.SetActiveProvider(id);
      setActiveProvider(id);
    } catch (e) {
      console.error("Failed to set provider:", e);
    }
  };

  const getStatusIcon = (status: string) => {
    switch (status) {
      case "available": return "✓";
      case "unavailable": return "✗";
      case "needs_login": return "?";
      default: return "○";
    }
  };

  const getStatusColor = (status: string) => {
    switch (status) {
      case "available": return "#10b981";
      case "unavailable": return "#ef4444";
      case "needs_login": return "#f59e0b";
      default: return "#6b7280";
    }
  };

  return (
    <div className="provider-selector">
      <div className="provider-header">
        <button className="settings-btn" onClick={handleDiscover} disabled={discovering}>
          {discovering ? "🔍 " + t("settings.discovering") : "🔍 " + t("settings.discoverProviders")}
        </button>
      </div>

      <div className="provider-list">
        {providers.length === 0 ? (
          <div className="provider-empty">
            {t("settings.noProvidersFound")}
          </div>
        ) : (
          providers.map((p) => (
            <div
              key={p.id}
              className={`provider-item ${activeProvider === p.id ? "active" : ""}`}
              onClick={() => p.status === "available" && handleSelect(p.id)}
              style={{ opacity: p.status === "available" ? 1 : 0.5 }}
            >
              <div className="provider-icon" style={{ background: p.color || "#6366f1" }}>
                {p.icon || "🤖"}
              </div>
              <div className="provider-info">
                <div className="provider-name">{p.name}</div>
                <div className="provider-type">{p.type}</div>
              </div>
              <div className="provider-status" style={{ color: getStatusColor(p.status) }}>
                {getStatusIcon(p.status)} {p.status}
              </div>
            </div>
          ))
        )}
      </div>

      {activeProvider && (
        <div className="provider-active-hint">
          {t("settings.currentProvider")}: {providers.find(p => p.id === activeProvider)?.name}
        </div>
      )}
    </div>
  );
}

// ─── Custom API Form Component ─────────────────────────────────────

function CustomAPIForm() {
  const { t } = useTranslation();
  const [baseURL, setBaseURL] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [model, setModel] = useState("");
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ success: boolean; message: string } | null>(null);

  const handleTest = async () => {
    if (!baseURL) return;
    setTesting(true);
    setTestResult(null);
    try {
      await (window as any).go.ai.Service.AddCustomProvider(
        "custom-" + Date.now(),
        "Custom API",
        baseURL,
        apiKey,
        model
      );
      setTestResult({ success: true, message: t("settings.connectionSuccess") });
    } catch (e: any) {
      setTestResult({ success: false, message: e.message || t("settings.connectionFailed") });
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="custom-api-form">
      <div className="custom-api-row">
        <label>Base URL</label>
        <input
          type="text"
          placeholder="https://api.openai.com/v1"
          value={baseURL}
          onChange={(e) => setBaseURL(e.target.value)}
          className="settings-input"
        />
      </div>
      <div className="custom-api-row">
        <label>API Key</label>
        <input
          type="password"
          placeholder="sk-..."
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          className="settings-input"
        />
      </div>
      <div className="custom-api-row">
        <label>Model</label>
        <input
          type="text"
          placeholder="gpt-4o"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          className="settings-input"
        />
      </div>
      <div className="custom-api-actions">
        <button
          className="settings-btn"
          onClick={handleTest}
          disabled={testing || !baseURL}
        >
          {testing ? t("settings.testing") : t("settings.testConnection")}
        </button>
        {testResult && (
          <span className={`test-result ${testResult.success ? "success" : "error"}`}>
            {testResult.message}
          </span>
        )}
      </div>
    </div>
  );
}

