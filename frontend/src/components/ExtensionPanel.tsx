import React, { useEffect, useState } from "react";
import { useTranslation } from "../i18n";
import { CheckCircle, XCircle, Loader, Palette, Code2, FileCode, Puzzle, Zap, Terminal, Trash2 } from "lucide-react";
import { GetExtensionContributes, ExtensionContributes, Extension } from "../bindings/extension";
import { getLspClient } from "../lsp/client";
import type * as Monaco from "monaco-editor";

interface ExtensionPanelProps {
  ext: Extension;
  lspPort: number;
  rootPath?: string;
  monaco?: typeof Monaco;
  onUninstall?: (ext: Extension) => void;
}

type LspStatus = "idle" | "connecting" | "connected" | "error" | "unavailable";

export default function ExtensionPanel({ ext, lspPort, rootPath, monaco, onUninstall }: ExtensionPanelProps) {
  const { t } = useTranslation();
  const [contrib, setContrib] = useState<ExtensionContributes | null>(null);
  const [loading, setLoading] = useState(true);
  const [lspStatus, setLspStatus] = useState<LspStatus>("idle");

  useEffect(() => {
    setLoading(true);
    setContrib(null);
    setLspStatus("idle");
    GetExtensionContributes(ext.publisher, ext.technicalName)
      .then((c) => {
        if (!c) return;
        setContrib(c);
        setLspStatus(c.lspCmd && c.lspCmd.length > 0 ? "idle" : "unavailable");
      })
      .catch(() => setContrib(null))
      .finally(() => setLoading(false));
  }, [ext.id]);

  const handleConnectLSP = () => {
    if (!contrib?.lspCmd?.length || !lspPort || !rootPath || !monaco) return;
    const langId = contrib.languages?.[0]?.id ?? ext.technicalName;
    setLspStatus("connecting");
    try {
      getLspClient(lspPort, langId, rootPath, monaco);
      setTimeout(() => setLspStatus("connected"), 1200);
    } catch {
      setLspStatus("error");
    }
  };

  if (loading) {
    return (
      <div className="ext-panel">
        <div className="ext-panel-loading">
          <Loader size={20} className="spin" />
          <span>{t("extensions.loading")}</span>
        </div>
      </div>
    );
  }

  const themes = contrib?.themes?.length ?? 0;
  const langs = contrib?.languages?.length ?? 0;
  const grammars = contrib?.grammars?.length ?? 0;
  const snippets = contrib?.snippets?.length ?? 0;
  const hasLSP = (contrib?.lspCmd?.length ?? 0) > 0;
  const hasAnyStatic = themes + langs + grammars + snippets > 0;

  return (
    <div className="ext-panel">
      <div className="ext-panel-header">
        <Puzzle size={16} />
        <span className="ext-panel-name">{ext.name}</span>
        <span className="ext-panel-version">v{ext.version}</span>
      </div>

      {ext.description && (
        <p className="ext-panel-desc">{ext.description}</p>
      )}

      <div className="ext-panel-section-title">{t("extensions.activeFeatures")}</div>

      <div className="ext-panel-rows">
        <StatusRow
          icon={<Palette size={14} />}
          label={t("extensions.colorThemes")}
          count={themes}
          active={themes > 0}
          hint={themes > 0 ? contrib!.themes.map((th) => th.label).join(", ") : t("extensions.none")}
        />
        <StatusRow
          icon={<FileCode size={14} />}
          label={t("extensions.langConfig")}
          count={langs}
          active={langs > 0}
          hint={langs > 0 ? contrib!.languages.map((l) => l.id).join(", ") : t("extensions.none")}
        />
        <StatusRow
          icon={<Code2 size={14} />}
          label={t("extensions.syntaxHighlight")}
          count={grammars}
          active={grammars > 0}
          hint={grammars > 0 ? t("extensions.grammarsRegistered", { count: grammars }) : t("extensions.none")}
        />
        <StatusRow
          icon={<Terminal size={14} />}
          label={t("extensions.codeSnippets")}
          count={snippets}
          active={snippets > 0}
          hint={snippets > 0 ? t("extensions.snippetsRegistered", { count: snippets }) : t("extensions.none")}
        />
      </div>

      <div className="ext-panel-section-title" style={{ marginTop: 12 }}>Language Server (LSP)</div>

      {hasLSP ? (
        <div className="ext-panel-lsp">
          <code className="ext-lsp-cmd">{contrib!.lspCmd.join(" ")}</code>
          {lspStatus === "idle" && (
            <button className="ext-lsp-btn" onClick={handleConnectLSP}
              disabled={!lspPort || !rootPath}>
              <Zap size={12} /> {t("extensions.connectLSP")}
            </button>
          )}
          {lspStatus === "connecting" && (
            <span className="ext-lsp-status connecting"><Loader size={12} className="spin" /> {t("extensions.connecting")}</span>
          )}
          {lspStatus === "connected" && (
            <span className="ext-lsp-status ok"><CheckCircle size={12} /> {t("extensions.connected")}</span>
          )}
          {lspStatus === "error" && (
            <span className="ext-lsp-status err"><XCircle size={12} /> {t("extensions.startFail")}</span>
          )}
        </div>
      ) : (
        <p className="ext-panel-no-lsp">
          {t("extensions.noLspConfigured")}
          {!hasAnyStatic && (
            <span> {t("extensions.webviewDependent")}</span>
          )}
        </p>
      )}

      {!hasAnyStatic && !hasLSP && (
        <div className="ext-panel-unsupported">
          <XCircle size={14} />
          <span>
            {t("extensions.unsupportedCore")}
          </span>
        </div>
      )}

      {!hasAnyStatic && !hasLSP && onUninstall && (
        <button
          className="ext-panel-uninstall-btn"
          onClick={() => onUninstall(ext)}
        >
          <Trash2 size={12} />
          {t("extensions.uninstall")}
        </button>
      )}
    </div>
  );
}

function StatusRow({
  icon, label, count, active, hint,
}: {
  icon: React.ReactNode;
  label: string;
  count: number;
  active: boolean;
  hint: string;
}) {
  return (
    <div className="ext-status-row" title={hint}>
      <span className="ext-status-icon">{icon}</span>
      <span className="ext-status-label">{label}</span>
      <span className={`ext-status-badge ${active ? "ok" : "off"}`}>
        {active ? `✓ ${count}` : "—"}
      </span>
    </div>
  );
}
