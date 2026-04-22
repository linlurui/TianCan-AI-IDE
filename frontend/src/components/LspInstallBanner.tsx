import React, { useRef } from "react";
import { useTranslation } from "../i18n";
import { AlertCircle, Terminal, RefreshCw, Download, CheckCircle, XCircle, Loader } from "lucide-react";
import { useUIStore, LspInstallStatus } from "../store/uiStore";
import { CheckLspInstalled, InstallLspStream } from "../bindings/lsp";

const LSP_NAMES: Record<string, string> = {
  go:         "gopls (Go)",
  typescript: "typescript-language-server",
  javascript: "typescript-language-server",
  python:     "pylsp (Python)",
  html:       "vscode-langservers-extracted",
  css:        "vscode-langservers-extracted",
  json:       "vscode-langservers-extracted",
  yaml:       "yaml-language-server",
  bash:       "bash-language-server",
  shell:      "bash-language-server",
  lua:        "lua-language-server",
  rust:       "rust-analyzer",
  c:          "clangd",
  cpp:        "clangd",
};

const INSTALL_CMDS: Record<string, string> = {
  go:         "go install golang.org/x/tools/gopls@latest",
  typescript: "npm install -g typescript-language-server typescript",
  javascript: "npm install -g typescript-language-server typescript",
  html:       "npm install -g vscode-langservers-extracted",
  css:        "npm install -g vscode-langservers-extracted",
  json:       "npm install -g vscode-langservers-extracted",
  yaml:       "npm install -g yaml-language-server",
  python:     "pip install python-lsp-server",
  bash:       "npm install -g bash-language-server",
  shell:      "npm install -g bash-language-server",
  lua:        "brew install lua-language-server",
  rust:       "rustup component add rust-analyzer",
  c:          "brew install llvm",
  cpp:        "brew install llvm",
};

const AUTO_INSTALLABLE = new Set(
  ["go","typescript","javascript","python","html","css","json","yaml","bash","shell","lua"]
);

interface Props {
  lang: string;
  onInstalled: () => void;
}

export default function LspInstallBanner({ lang, onInstalled }: Props) {
  const { t } = useTranslation();
  const { lspStatus, setLspStatus, lspInstallLog, appendInstallLog, clearInstallLog,
          setToolPanelOpen, setActiveToolTab, addTerminal } = useUIStore();
  const installing = useRef(false);

  const status: LspInstallStatus = lspStatus[lang] ?? "checking";
  const lspName = LSP_NAMES[lang];
  const installCmd = INSTALL_CMDS[lang];
  const canAutoInstall = AUTO_INSTALLABLE.has(lang);

  if (status === "ready" || status === "checking") return null;
  if (!lspName && !installCmd) return null;

  const handleAutoInstall = async () => {
    if (installing.current) return;
    installing.current = true;
    setLspStatus(lang, "installing");
    clearInstallLog();

    try {
      // InstallLspStream blocks until done and returns all lines
      const lines: string[] = await InstallLspStream(lang) as any;
      for (const line of lines) appendInstallLog(line);

      const last = lines[lines.length - 1] ?? "";
      if (last === "OK") {
        setLspStatus(lang, "ready");
        onInstalled();
      } else {
        // Failed — stay on missing so user can try terminal
        setLspStatus(lang, "missing");
      }
    } catch (e: any) {
      appendInstallLog("ERROR: " + (e?.message ?? String(e)));
      setLspStatus(lang, "missing");
    } finally {
      installing.current = false;
    }
  };

  const handleOpenTerminal = () => {
    addTerminal(installCmd);
    setToolPanelOpen(true);
    setActiveToolTab("terminal");
  };

  const handleRecheck = () => {
    setLspStatus(lang, "checking");
    clearInstallLog();
    CheckLspInstalled(lang)
      .then((installed: any) => {
        setLspStatus(lang, installed ? "ready" : (AUTO_INSTALLABLE.has(lang) ? "missing" : "unsupported"));
        if (installed) onInstalled();
      })
      .catch(() => setLspStatus(lang, "unsupported"));
  };

  const isInstalling = status === "installing";
  const lastLog = lspInstallLog[lspInstallLog.length - 1] ?? "";
  const hasError = lspInstallLog.some(l => l.startsWith("ERROR:"));

  return (
    <div className={`lsp-banner${isInstalling ? " installing" : ""}`}>
      <div className="lsp-banner-row">
        {isInstalling
          ? <Loader size={13} className="lsp-banner-icon installing spin" />
          : hasError
            ? <XCircle size={13} className="lsp-banner-icon error" />
            : <AlertCircle size={13} className="lsp-banner-icon missing" />
        }

        <div className="lsp-banner-body">
          <span className="lsp-banner-text">
            {isInstalling
              ? t("lsp.installing", { name: lspName })
              : lspName
                ? t("lsp.notInstalled", { name: lspName })
                : t("lsp.manualInstall", { lang })}
          </span>

          {/* Inline log during / after install */}
          {lspInstallLog.length > 0 && (
            <div className="lsp-banner-log">
              {hasError
                ? <span className="lsp-log-error">{lspInstallLog.find(l => l.startsWith("ERROR:"))}</span>
                : isInstalling
                  ? <span className="lsp-log-line">{lastLog}</span>
                  : null
              }
              {!isInstalling && hasError && (
                <span className="lsp-log-hint">
                  {t("lsp.autoInstallFailed")}
                </span>
              )}
            </div>
          )}
        </div>

        <div className="lsp-banner-actions">
          {canAutoInstall && !isInstalling && (
            <button className="lsp-banner-btn primary" onClick={handleAutoInstall}>
              <Download size={12} /> {t("lsp.oneClickInstall")}
            </button>
          )}
          {installCmd && !isInstalling && (
            <button className="lsp-banner-btn" onClick={handleOpenTerminal} title={t("lsp.manualInstallTerminal")}>
              <Terminal size={12} /> {t("lsp.terminalInstall")}
            </button>
          )}
          {!isInstalling && (
            <button className="lsp-banner-btn" onClick={handleRecheck} title={t("lsp.recheckTitle")}>
              <RefreshCw size={12} /> {t("lsp.installedRefresh")}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// Hook: check LSP status when lang/lspPort changes
export function useLspCheck(lang: string | null, lspPort: number) {
  const { lspStatus, setLspStatus } = useUIStore();

  React.useEffect(() => {
    if (!lang || lang === "plaintext" || !lspPort) return;
    // Only re-check if not already checked (or in "checking" state)
    if (lspStatus[lang] && lspStatus[lang] !== "checking") return;

    setLspStatus(lang, "checking");
    CheckLspInstalled(lang)
      .then((installed: any) => {
        setLspStatus(lang, installed ? "ready" : (AUTO_INSTALLABLE.has(lang) ? "missing" : "unsupported"));
      })
      .catch(() => setLspStatus(lang, "unsupported"));
  }, [lang, lspPort]);

  return lang ? (lspStatus[lang] ?? "checking") : "checking";
}
