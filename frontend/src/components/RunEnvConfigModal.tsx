import React, { useState, useEffect } from "react";
import { useTranslation } from "../i18n";
import { X, Plus, Trash2, Loader2, Zap, Check } from "lucide-react";
import { GetRunEnvConfig, SetRunEnvConfig, DetectRunEnv } from "../bindings/process";
import type { ProjectType } from "../types";

const PROJECT_TYPES: { type: ProjectType; emoji: string; label?: string; cmd?: string; labelKey?: string; cmdKey?: string }[] = [
  { type: "golang",   emoji: "🐹", label: "Go",            cmd: "go run ."            },
  { type: "java",     emoji: "☕", label: "Java / Maven",  cmd: "mvn spring-boot:run"  },
  { type: "frontend", emoji: "🌐", label: "Node.js / Web", cmd: "npm run dev"          },
  { type: "python",   emoji: "🐍", label: "Python",        cmd: "python3 main.py"      },
  { type: "rust",     emoji: "⚙️", label: "Rust",          cmd: "cargo run"            },
  { type: "other",    emoji: "📦", labelKey: "projectSettings.other", cmdKey: "projectSettings.custom" },
];

interface Props {
  projectName: string;
  rootPath: string;
  projectType?: ProjectType;
  onSaveType?: (type: ProjectType) => void;
  onClose: () => void;
}

interface EnvRow { key: string; value: string; }

export default function RunEnvConfigModal({ projectName, rootPath, projectType, onSaveType, onClose }: Props) {
  const { t } = useTranslation();
  const [selectedType, setSelectedType] = useState<ProjectType>(projectType ?? "other");
  const [preCmd, setPreCmd] = useState("");
  const [buildFlags, setBuildFlags] = useState("");
  const [envRows, setEnvRows] = useState<EnvRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [detecting, setDetecting] = useState(false);

  useEffect(() => {
    setLoading(true);
    GetRunEnvConfig(rootPath)
      .then((cfg) => {
        setPreCmd(cfg?.preCmd ?? "");
        setBuildFlags((cfg as any)?.buildFlags ?? "");
        const rows: EnvRow[] = Object.entries(cfg?.env ?? {}).map(([key, value]) => ({ key, value: value as string }));
        setEnvRows(rows);
      })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [rootPath]);

  const handleDetect = async () => {
    setDetecting(true);
    try {
      const cfg = await DetectRunEnv(rootPath);
      if (cfg?.preCmd) setPreCmd(cfg.preCmd);
      if ((cfg as any)?.buildFlags) setBuildFlags((cfg as any).buildFlags);
    } catch {}
    finally { setDetecting(false); }
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      const env: Record<string, string> = {};
      for (const row of envRows) {
        if (row.key.trim()) env[row.key.trim()] = row.value;
      }
      await SetRunEnvConfig(rootPath, { preCmd: preCmd.trim(), buildFlags: buildFlags.trim(), env } as any);
      onSaveType?.(selectedType);
      onClose();
    } catch (err) {
      alert(t("runEnv.saveFail") + ": " + err);
    } finally {
      setSaving(false);
    }
  };

  const addRow = () => setEnvRows((prev) => [...prev, { key: "", value: "" }]);
  const removeRow = (i: number) => setEnvRows((prev) => prev.filter((_, idx) => idx !== i));
  const updateRow = (i: number, field: "key" | "value", val: string) =>
    setEnvRows((prev) => prev.map((r, idx) => idx === i ? { ...r, [field]: val } : r));

  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="run-env-modal">
        <div className="run-env-modal-header">
          <span className="run-env-modal-title">{t("runEnv.title")}</span>
          <span className="run-env-modal-project">{projectName}</span>
          <button className="modal-close-btn" onClick={onClose}><X size={14} /></button>
        </div>

        {loading ? (
          <div className="run-env-modal-loading"><Loader2 size={16} className="spin" /> {t("runEnv.loading")}</div>
        ) : (
          <div className="run-env-modal-body">
            {/* Project type */}
            <div className="run-env-section">
              <div className="run-env-section-header"><span>{t("runEnv.projectType")}</span></div>
              <div className="proj-type-grid" style={{ padding: 0 }}>
                {PROJECT_TYPES.map(({ type, emoji, label, cmd, labelKey, cmdKey }) => (
                  <button
                    key={type}
                    className={`proj-type-btn${selectedType === type ? " active" : ""}`}
                    onClick={() => setSelectedType(type)}
                  >
                    <span className="proj-type-emoji">{emoji}</span>
                    <div className="proj-type-info">
                      <span className="proj-type-label">{labelKey ? t(labelKey) : label}</span>
                      <span className="proj-type-cmd">{cmdKey ? t(cmdKey) : cmd}</span>
                    </div>
                    {selectedType === type && <Check size={13} className="proj-type-check" />}
                  </button>
                ))}
              </div>
            </div>

            {/* Build flags */}
            <div className="run-env-section">
              <div className="run-env-section-header"><span>{t("runEnv.debugBuildFlags")}</span></div>
              <div className="run-env-section-desc">{t("runEnv.debugBuildFlagsDesc")}</div>
              <input
                className="run-env-precmd-input"
                value={buildFlags}
                onChange={(e) => setBuildFlags(e.target.value)}
                placeholder={t("runEnv.buildFlagsPlaceholder")}
                spellCheck={false}
              />
            </div>

            {/* Pre-run command */}
            <div className="run-env-section">
              <div className="run-env-section-header">
                <span>{t("runEnv.preCommand")}</span>
                <button className="run-env-detect-btn" onClick={handleDetect} disabled={detecting} title={t("runEnv.autoDetectTitle")}>
                  {detecting ? <Loader2 size={11} className="spin" /> : <Zap size={11} />}
                  {t("runEnv.autoDetect")}
                </button>
              </div>
              <div className="run-env-section-desc">{t("runEnv.preCommandDesc")}</div>
              <textarea
                className="run-env-precmd-input"
                value={preCmd}
                onChange={(e) => setPreCmd(e.target.value)}
                placeholder={t("runEnv.preCmdPlaceholder")}
                rows={2}
                spellCheck={false}
              />
            </div>

            {/* Environment variables */}
            <div className="run-env-section">
              <div className="run-env-section-header">
                <span>{t("runEnv.envVars")}</span>
                <button className="run-env-add-btn" onClick={addRow}>
                  <Plus size={11} /> {t("runEnv.add")}
                </button>
              </div>
              <div className="run-env-section-desc">{t("runEnv.envVarsDesc")}</div>
              <div className="run-env-table">
                {envRows.length === 0 && (
                  <div className="run-env-empty">{t("runEnv.noEnvVars")}</div>
                )}
                {envRows.map((row, i) => (
                  <div key={i} className="run-env-row">
                    <input
                      className="run-env-key-input"
                      value={row.key}
                      onChange={(e) => updateRow(i, "key", e.target.value)}
                      placeholder="KEY"
                      spellCheck={false}
                    />
                    <span className="run-env-eq">=</span>
                    <input
                      className="run-env-val-input"
                      value={row.value}
                      onChange={(e) => updateRow(i, "value", e.target.value)}
                      placeholder="value"
                      spellCheck={false}
                    />
                    <button className="run-env-del-btn" onClick={() => removeRow(i)}>
                      <Trash2 size={11} />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          </div>
        )}

        <div className="run-env-modal-footer">
          <button className="btn-secondary" onClick={onClose}>{t("runEnv.cancel")}</button>
          <button className="btn-primary" onClick={handleSave} disabled={saving || loading}>
            {saving ? <><Loader2 size={12} className="spin" /> {t("runEnv.saving")}</> : t("runEnv.save")}
          </button>
        </div>
      </div>
    </div>
  );
}
