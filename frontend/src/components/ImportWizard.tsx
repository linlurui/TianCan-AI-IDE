import { useState, useEffect } from "react";
import { useTranslation } from "../i18n";
import { X, FolderOpen, ChevronRight, CheckCircle2, XCircle, Loader2, Terminal } from "lucide-react";
import { DetectProjectTypes, RunSetup } from "../bindings/project";
import type { DetectResult } from "../bindings/project";

type Step = "select-type" | "running" | "done";

interface Props {
  dirPath: string;
  onConfirm: (dirPath: string) => void;
  onCancel: () => void;
}

export default function ImportWizard({ dirPath, onConfirm, onCancel }: Props) {
  const { t } = useTranslation();
  const [step, setStep] = useState<Step>("select-type");
  const [types, setTypes] = useState<DetectResult[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [runOutput, setRunOutput] = useState("");
  const [runError, setRunError] = useState<string | null>(null);


  useEffect(() => {
    setLoading(true);
    DetectProjectTypes(dirPath)
      .then((results: DetectResult[]) => {
        setTypes(results);
        const first = results.find((r: DetectResult) => r.detected);
        if (first) setSelected(first.type);
      })
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [dirPath]);

  const detected = types.filter((t) => t.detected);
  const others = types.filter((t) => !t.detected);

  const handleStart = async () => {
    if (!selected) return;
    setStep("running");
    try {
      const out = await RunSetup(dirPath, selected);
      setRunOutput(out);
      setRunError(null);
    } catch (e: any) {
      setRunError(String(e));
      setRunOutput("");
    }
    setStep("done");
  };

  return (
    <div className="modal-overlay">
      <div className="import-wizard">
        {/* Header */}
        <div className="import-wizard-header">
          <div className="import-wizard-header-left">
            <FolderOpen size={16} style={{ color: "var(--accent)" }} />
            <span className="import-wizard-title">{t("import.title")}</span>
          </div>
          <button className="modal-close-btn" onClick={onCancel} title={t("import.cancel")}>
            <X size={14} />
          </button>
        </div>

        {/* Path bar */}
        <div className="import-wizard-path">
          <span className="import-wizard-path-label">{t("import.projectPath")}</span>
          <span className="import-wizard-path-value">{dirPath}</span>
        </div>

        {/* Steps indicator */}
        <div className="import-wizard-steps">
          <div className={`wizard-step${step === "select-type" ? " active" : " done"}`}>
            <span className="wizard-step-num">1</span>
            <span>{t("import.selectEnv")}</span>
          </div>
          <ChevronRight size={14} style={{ color: "var(--text-muted)" }} />
          <div className={`wizard-step${step === "running" ? " active" : step === "done" ? " done" : ""}`}>
            <span className="wizard-step-num">2</span>
            <span>{t("import.installDeps")}</span>
          </div>
        </div>

        {/* Body */}
        <div className="import-wizard-body">

          {/* Step 1: select type */}
          {step === "select-type" && (
            <>
              {loading ? (
                <div className="wizard-loading">
                  <Loader2 size={20} className="spin" />
                  <span>{t("import.detecting")}</span>
                </div>
              ) : (
                <>
                  {detected.length > 0 && (
                    <div className="wizard-section">
                      <div className="wizard-section-label">
                        {t("import.autoDetected", { count: detected.length })}
                      </div>
                      <div className="wizard-type-grid">
                        {detected.map((t) => (
                          <button
                            key={t.type}
                            className={`wizard-type-card${selected === t.type ? " selected" : ""} detected`}
                            onClick={() => setSelected(t.type)}
                          >
                            <span className="wizard-type-icon">{t.icon}</span>
                            <span className="wizard-type-name">{t.displayName}</span>
                            <span className="wizard-type-desc">{t.description}</span>
                            {selected === t.type && <CheckCircle2 size={14} className="wizard-type-check" />}
                          </button>
                        ))}
                      </div>
                    </div>
                  )}

                  <div className="wizard-section">
                    <div className="wizard-section-label">
                      {detected.length > 0 ? t("import.otherEnvs") : t("import.selectProjectEnv")}
                    </div>
                    <div className="wizard-type-grid">
                      {others.map((t) => (
                        <button
                          key={t.type}
                          className={`wizard-type-card${selected === t.type ? " selected" : ""}`}
                          onClick={() => setSelected(t.type)}
                        >
                          <span className="wizard-type-icon">{t.icon}</span>
                          <span className="wizard-type-name">{t.displayName}</span>
                          <span className="wizard-type-desc">{t.description}</span>
                          {selected === t.type && <CheckCircle2 size={14} className="wizard-type-check" />}
                        </button>
                      ))}
                    </div>
                  </div>
                </>
              )}

            </>
          )}

          {/* Step 2: running */}
          {step === "running" && (
            <div className="wizard-running">
              <Loader2 size={32} className="spin" style={{ color: "var(--accent)" }} />
              <div className="wizard-running-title">{t("import.installingDeps")}</div>
              <div className="wizard-running-sub">
                {t("import.executing")}: <code>{types.find((tp) => tp.type === selected)?.setupCmd}</code>
              </div>
              <div className="wizard-running-note">{t("install.pleaseWait")}</div>
            </div>
          )}

          {/* Step 3: done */}
          {step === "done" && (
            <div className="wizard-done">
              {runError ? (
                <>
                  <XCircle size={32} style={{ color: "var(--error)" }} />
                  <div className="wizard-done-title error">{t("import.installFailed")}</div>
                  <pre className="wizard-output error">{runError}</pre>
                </>
              ) : (
                <>
                  <CheckCircle2 size={32} style={{ color: "var(--success)" }} />
                  <div className="wizard-done-title">{t("import.installSuccess")}</div>
                  {runOutput && (
                    <pre className="wizard-output">{runOutput.slice(0, 2000)}{runOutput.length > 2000 ? `\n${t("import.outputTruncated")}` : ""}</pre>
                  )}
                </>
              )}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="import-wizard-footer">
          <button className="btn-ghost" onClick={onCancel}>{t("import.cancel")}</button>

          {step === "select-type" && (
            <>
              <button
                className="btn-ghost"
                onClick={() => onConfirm(dirPath)}
                disabled={loading}
              >
                <FolderOpen size={13} /> {t("import.skipInstall")}
              </button>
              <button
                className="btn-primary"
                onClick={handleStart}
                disabled={loading || !selected}
              >
                <Terminal size={13} /> {t("import.importAndConfig")}
              </button>
            </>
          )}

          {step === "done" && (
            <button className="btn-primary" onClick={() => onConfirm(dirPath)}>
              <FolderOpen size={13} /> {t("import.openProject")}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
