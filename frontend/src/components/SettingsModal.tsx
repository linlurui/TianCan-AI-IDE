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
                <div className="settings-section-title">{t("settings.lmStudioSection")}</div>
                <SettingRow label={t("settings.serviceUrl")}>
                  <input className="settings-input" value={settings.lmStudioUrl}
                    onChange={(e) => set("lmStudioUrl", e.target.value)} />
                </SettingRow>
                <SettingRow label={t("settings.defaultModel")}>
                  <input className="settings-input" value={settings.defaultModel}
                    onChange={(e) => set("defaultModel", e.target.value)} />
                </SettingRow>

                <div className="settings-section-title">{t("settings.apiCloudSection")}</div>
                <div className="settings-hint">
                  {t("settings.apiCloudHint")}
                </div>

                <div className="settings-section-title">{t("settings.completionSection")}</div>
                <SettingRow label={t("settings.inlineCompletion")}>
                  <Toggle value={settings.inlineCompletion} onChange={(v) => set("inlineCompletion", v)} />
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

