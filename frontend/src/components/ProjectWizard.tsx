import React, { useState, useEffect } from "react";
import { useTranslation } from "../i18n";
import {
  X, FolderOpen, ChevronRight, ChevronLeft, CheckCircle2, Loader2,
  FolderPlus, Download, Plus, Globe, Smartphone, Coffee, Code, Zap,
  Shield, Layers, Cpu, Server, Package, FilePlus2, Gem,
} from "lucide-react";
import { SelectDirectory, WriteFile } from "../bindings/filesystem";
import { DetectProjectTypes, CreateProject } from "../bindings/project";
import { IsDlvInstalled, InstallDlv } from "../bindings/debug";
import type { DetectResult } from "../bindings/project";
import { PROJECT_CATEGORIES, type ProjectTypeItem } from "../data/projectTypes";

export interface WizardResult {
  mode: "create" | "import";
  dirPath: string;
  extensionsToInstall: string[];
  setupType?: string;
  projectName: string;
}

interface Props {
  onConfirm: (result: WizardResult) => void;
  onCancel: () => void;
}

// Map category id → lucide icon
const CAT_ICONS: Record<string, React.ReactNode> = {
  web:    <Globe size={20} />,
  mobile: <Smartphone size={20} />,
  java:   <Coffee size={20} />,
  python: <Code size={20} />,
  go:     <Zap size={20} />,
  rust:   <Shield size={20} />,
  dotnet: <Layers size={20} />,
  cpp:    <Cpu size={20} />,
  php:    <Server size={20} />,
  ruby:   <Gem size={20} />,
  other:  <Package size={20} />,
};

// create: step 1=category, 2=type, 3=location
// import: step 1=detect env
const CREATE_STEP_KEYS = ["wizard.stepProjectType", "wizard.stepLangFramework", "wizard.stepProjectLocation"];
const IMPORT_STEP_KEYS = ["wizard.stepDetectEnv"];

const EXT_MAP: Record<string, string[]> = {
  maven: ["redhat.java"], gradle: ["redhat.java"],
  go: ["golang.go"], cargo: ["rust-lang.rust-analyzer"],
  pip: ["ms-python.python"], poetry: ["ms-python.python"],
  npm: ["esbenp.prettier-vscode"], yarn: ["esbenp.prettier-vscode"],
  pnpm: ["esbenp.prettier-vscode"], dotnet: ["ms-dotnettools.csharp"],
  ruby: [], composer: [],
};

export default function ProjectWizard({ onConfirm, onCancel }: Props) {
  const { t } = useTranslation();
  // null = choose screen, "create" / "import" = in flow
  const [mode, setMode] = useState<"create" | "import" | null>(null);
  const [step, setStep] = useState(1);

  // Create state
  const [categoryId, setCategoryId]     = useState<string | null>(null);
  const [selectedType, setSelectedType] = useState<ProjectTypeItem | null>(null);
  const [projectName, setProjectName]   = useState("my-project");
  const [parentDir, setParentDir]       = useState("");
  const [creating, setCreating]         = useState(false);

  // Import state
  const [importDir, setImportDir]         = useState("");
  const [detectedTypes, setDetectedTypes] = useState<DetectResult[]>([]);
  const [detectLoading, setDetectLoading] = useState(false);
  const [selectedSetup, setSelectedSetup] = useState<string | null>(null);
  const [skipSetup, setSkipSetup]         = useState(false);
  const [installingDlv, setInstallingDlv] = useState(false);

  const category  = PROJECT_CATEGORIES.find((c) => c.id === categoryId);
  const stepKeys  = mode === "import" ? IMPORT_STEP_KEYS : CREATE_STEP_KEYS;
  const finalPath = parentDir ? `${parentDir}/${projectName}` : "";

  // Auto-detect when importDir changes
  useEffect(() => {
    if (!importDir) return;
    setDetectLoading(true);
    DetectProjectTypes(importDir)
      .then((res: DetectResult[]) => {
        setDetectedTypes(res);
        const first = res.find((r: DetectResult) => r.detected);
        if (first) setSelectedSetup(first.type);
      })
      .catch(() => setDetectedTypes([]))
      .finally(() => setDetectLoading(false));
  }, [importDir]);

  // ── Trigger: click "导入项目" → open folder picker immediately ──
  const handleStartImport = async () => {
    const p = await SelectDirectory().catch(() => "");
    if (!p) return;
    setMode("import");
    setImportDir(p);
    setStep(1);
  };

  // ── Trigger: click "新建项目" → go to category step ──
  const handleStartCreate = () => {
    setMode("create");
    setStep(1);
  };

  const handleSkipToEmpty = () => {
    const otherCat  = PROJECT_CATEGORIES.find((c) => c.id === "other")!;
    const emptyType = otherCat.types.find((t) => t.id === "empty")!;
    setCategoryId("other");
    setSelectedType(emptyType);
    setStep(3);
  };

  const handleSelectParentDir = async () => {
    const p = await SelectDirectory().catch(() => "");
    if (p) setParentDir(p);
  };

  const handleChangeImportDir = async () => {
    const p = await SelectDirectory().catch(() => "");
    if (p) { setImportDir(p); setSelectedSetup(null); setDetectedTypes([]); }
  };

  const handleBack = () => {
    if (mode === null || step <= 1) { setMode(null); setCategoryId(null); setSelectedType(null); return; }
    if (mode === "create") {
      if (step === 3 && categoryId === "other" && selectedType?.id === "empty") {
        // came from skip → go back to category
        setSelectedType(null); setStep(1); return;
      }
      setStep((s) => s - 1);
      if (step === 2) setSelectedType(null);
      if (step === 1) { setMode(null); setCategoryId(null); }
    }
    if (mode === "import") {
      setMode(null); setImportDir(""); setDetectedTypes([]);
    }
  };

  const maybeInstallDlv = async (isGo: boolean) => {
    if (!isGo) return;
    const installed = await IsDlvInstalled().catch(() => false);
    if (!installed) {
      setInstallingDlv(true);
      await InstallDlv().catch(() => {});
      setInstallingDlv(false);
    }
  };

  const handleConfirmCreate = async () => {
    if (!selectedType || !parentDir || !projectName.trim()) return;
    setCreating(true);
    const dirPath = await CreateProject(parentDir, projectName).catch(() => "");
    if (!dirPath) { setCreating(false); return; }
    if (selectedType.templateFiles) {
      for (const f of selectedType.templateFiles) {
        await WriteFile(`${dirPath}/${f.name}`, f.content).catch(() => null);
      }
    }
    await maybeInstallDlv(categoryId === "go");
    onConfirm({
      mode: "create", dirPath,
      extensionsToInstall: selectedType.extensions,
      setupType: selectedType.setupType,
      projectName: projectName.trim(),
    });
  };

  const handleConfirmImport = async () => {
    if (!importDir) return;
    const ext: string[] = [];
    detectedTypes.filter((t) => t.detected).forEach((t) => {
      (EXT_MAP[t.type] ?? []).forEach((e) => { if (!ext.includes(e)) ext.push(e); });
    });
    const isGo = detectedTypes.some((t) => t.detected && t.type === "go");
    await maybeInstallDlv(isGo);
    onConfirm({
      mode: "import", dirPath: importDir, extensionsToInstall: ext,
      setupType: skipSetup ? undefined : (selectedSetup ?? undefined),
      projectName: importDir.split("/").pop() ?? "project",
    });
  };

  const isNextDisabled = () => {
    if (mode === "create") {
      if (step === 1) return !categoryId;
      if (step === 2) return !selectedType;
      if (step === 3) return !parentDir || !projectName.trim() || creating || installingDlv;
    }
    if (mode === "import") {
      if (step === 1) return !importDir || detectLoading || installingDlv;
    }
    return false;
  };

  const handleNext = () => {
    if (mode === "create" && step === 3) { handleConfirmCreate(); return; }
    if (mode === "import" && step === 1) { handleConfirmImport(); return; }
    setStep((s) => s + 1);
  };

  const nextLabel = () => {
    if (mode === "create" && step === 3) return creating
      ? <><Loader2 size={13} className="spin" /> {t("wizard.creating")}</>
      : <><FolderPlus size={13} /> {t("wizard.createProject")}</>;
    if (mode === "import" && step === 1) return installingDlv
      ? <><Loader2 size={13} className="spin" /> {t("wizard.installingDelve")}</>
      : <><Download size={13} /> {t("wizard.importAndConfig")}</>;
    return <>{t("wizard.nextStep")} <ChevronRight size={13} /></>;
  };

  // ─── Choose screen (mode === null) ───────────────────────────────
  if (mode === null) {
    return (
      <div className="pw-overlay">
        <div className="pw-choose-dialog">
          <button className="pw-close-btn pw-choose-close" onClick={onCancel}><X size={14} /></button>
          <div className="pw-choose-title">{t("wizard.start")}</div>
          <div className="pw-choose-subtitle">{t("wizard.chooseOperation")}</div>
          <div className="pw-choose-cards">
            <button className="pw-choose-card" onClick={handleStartCreate}>
              <div className="pw-choose-card-icon"><FilePlus2 size={28} /></div>
              <div className="pw-choose-card-label">{t("wizard.newProject")}</div>
              <div className="pw-choose-card-desc">{t("wizard.newProjectDesc")}</div>
            </button>
            <button className="pw-choose-card" onClick={handleStartImport}>
              <div className="pw-choose-card-icon"><FolderOpen size={28} /></div>
              <div className="pw-choose-card-label">{t("wizard.importProject")}</div>
              <div className="pw-choose-card-desc">{t("wizard.importProjectDesc")}</div>
            </button>
          </div>
        </div>
      </div>
    );
  }

  // ─── Wizard screen (mode set) ─────────────────────────────────────
  return (
    <div className="pw-overlay">
      <div className="pw-dialog">
        <div className="pw-header">
          <span className="pw-header-title">
            {mode === "create" ? t("wizard.newProject") : t("wizard.importProject")}
          </span>
          <button className="pw-close-btn" onClick={onCancel}><X size={14} /></button>
        </div>

        <div className="pw-body">
          {/* Sidebar */}
          <div className="pw-sidebar">
            {stepKeys.map((key, i) => {
              const idx     = i + 1;
              const isDone  = idx < step;
              const isActive = idx === step;
              return (
                <div key={i} className={`pw-step${isActive ? " active" : ""}${isDone ? " done" : ""}`}>
                  <div className="pw-step-indicator">
                    <div className="pw-step-num">
                      {isDone ? <CheckCircle2 size={14} /> : <span>{idx}</span>}
                    </div>
                    {i < stepKeys.length - 1 && <div className="pw-step-line" />}
                  </div>
                  <span className="pw-step-label">{t(key)}</span>
                </div>
              );
            })}
          </div>

          {/* Content */}
          <div className="pw-content">

            {/* Create – Step 1: category */}
            {mode === "create" && step === 1 && (
              <div className="pw-category-select">
                <div className="pw-section-title-row">
                  <span className="pw-section-title" style={{ marginBottom: 0 }}>{t("wizard.selectProjectType")}</span>
                  <button className="pw-skip-link" onClick={handleSkipToEmpty}>{t("wizard.skipCreateEmpty")} →</button>
                </div>
                <div className="pw-category-grid">
                  {PROJECT_CATEGORIES.map((cat) => (
                    <button
                      key={cat.id}
                      className={`pw-category-card${categoryId === cat.id ? " selected" : ""}`}
                      style={{ "--cat-color": cat.color } as React.CSSProperties}
                      onClick={() => { setCategoryId(cat.id); setSelectedType(null); }}
                    >
                      <span className="pw-cat-icon">{CAT_ICONS[cat.id] ?? <Package size={20} />}</span>
                      <span className="pw-cat-label">{cat.label}</span>
                      <span className="pw-cat-desc">{cat.description}</span>
                      {categoryId === cat.id && <CheckCircle2 size={14} className="pw-cat-check" />}
                    </button>
                  ))}
                </div>
              </div>
            )}

            {/* Create – Step 2: type */}
            {mode === "create" && step === 2 && category && (
              <div className="pw-type-select">
                <div className="pw-section-title">
                  <span className="pw-section-title-icon">{CAT_ICONS[category.id]}</span>
                  {category.label}
                </div>
                <div className="pw-type-list">
                  {category.types.map((t) => (
                    <button
                      key={t.id}
                      className={`pw-type-item${selectedType?.id === t.id ? " selected" : ""}`}
                      onClick={() => setSelectedType(t)}
                    >
                      <div className="pw-type-item-body">
                        <span className="pw-type-item-label">{t.label}</span>
                        <span className="pw-type-item-desc">{t.description}</span>
                        {t.extensions.length > 0 && (
                          <span className="pw-type-item-exts">
                            <Download size={10} /> {t.extensions.join("  ·  ")}
                          </span>
                        )}
                      </div>
                      {selectedType?.id === t.id && <CheckCircle2 size={16} className="pw-type-check" />}
                    </button>
                  ))}
                </div>
              </div>
            )}

            {/* Create – Step 3: location */}
            {mode === "create" && step === 3 && (
              <div className="pw-location">
                <div className="pw-section-title">{t("wizard.projectNameLocation")}</div>

                <div className="pw-field">
                  <label className="pw-field-label">{t("wizard.projectName")}</label>
                  <input
                    className="pw-field-input"
                    value={projectName}
                    onChange={(e) => setProjectName(e.target.value.replace(/[/\\:*?"<>|]/g, ""))}
                    placeholder="my-project"
                    autoFocus
                  />
                </div>

                <div className="pw-field">
                  <label className="pw-field-label">{t("wizard.saveLocation")}</label>
                  <button
                    className={`pw-dir-picker${parentDir ? " has-value" : ""}`}
                    onClick={handleSelectParentDir}
                  >
                    <FolderOpen size={14} className="pw-dir-picker-icon" />
                    <span className="pw-dir-picker-text">
                      {parentDir || t("wizard.clickSelectDir")}
                    </span>
                    <ChevronRight size={13} className="pw-dir-picker-arrow" />
                  </button>
                </div>

                {finalPath && (
                  <div className="pw-path-preview">
                    <span className="pw-path-preview-label">{t("wizard.createPath")}</span>
                    <code className="pw-path-preview-val">{finalPath}</code>
                  </div>
                )}

                {selectedType && selectedType.extensions.length > 0 && (
                  <div className="pw-ext-preview">
                    <div className="pw-ext-preview-title"><Download size={11} /> {t("wizard.willAutoInstallExt")}</div>
                    {selectedType.extensions.map((id) => (
                      <div key={id} className="pw-ext-preview-item">{id}</div>
                    ))}
                    {selectedType.setupType && (
                      <div className="pw-ext-preview-item setup">
                        <Plus size={10} /> {t("wizard.runDepInstall", { type: selectedType.setupType })}
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}

            {/* Import – Step 1: env detection */}
            {mode === "import" && step === 1 && (
              <div className="pw-import-env">
                <div className="pw-section-title">{t("wizard.detectProjectEnv")}</div>

                <button className="pw-dir-picker has-value" onClick={handleChangeImportDir}>
                  <FolderOpen size={14} className="pw-dir-picker-icon" />
                  <span className="pw-dir-picker-text">{importDir}</span>
                  <span className="pw-dir-picker-change">{t("wizard.change")}</span>
                </button>

                {detectLoading ? (
                  <div className="pw-detect-loading">
                    <Loader2 size={18} className="spin" />
                    <span>{t("wizard.detectingProjectType")}</span>
                  </div>
                ) : (
                  <>
                    {detectedTypes.filter((t) => t.detected).length > 0 && (
                      <div className="pw-detect-section">
                        <div className="pw-detect-label">
                          <CheckCircle2 size={12} style={{ color: "var(--success)" }} /> {t("wizard.autoDetected")}
                        </div>
                        <div className="pw-detect-grid">
                          {detectedTypes.filter((t) => t.detected).map((t) => (
                            <button
                              key={t.type}
                              className={`pw-detect-card detected${selectedSetup === t.type ? " selected" : ""}`}
                              onClick={() => setSelectedSetup(t.type)}
                            >
                              <span className="pw-det-icon">{t.icon}</span>
                              <span className="pw-det-name">{t.displayName}</span>
                              <code className="pw-det-cmd">{t.setupCmd}</code>
                              {selectedSetup === t.type && <CheckCircle2 size={12} className="pw-det-check" />}
                            </button>
                          ))}
                        </div>
                      </div>
                    )}

                    {detectedTypes.filter((t) => !t.detected).length > 0 && (
                      <div className="pw-detect-section">
                        <div className="pw-detect-label">{t("wizard.otherEnvs")}</div>
                        <div className="pw-detect-grid">
                          {detectedTypes.filter((t) => !t.detected).map((t) => (
                            <button
                              key={t.type}
                              className={`pw-detect-card${selectedSetup === t.type ? " selected" : ""}`}
                              onClick={() => setSelectedSetup(t.type)}
                            >
                              <span className="pw-det-icon">{t.icon}</span>
                              <span className="pw-det-name">{t.displayName}</span>
                              <code className="pw-det-cmd">{t.setupCmd}</code>
                              {selectedSetup === t.type && <CheckCircle2 size={12} className="pw-det-check" />}
                            </button>
                          ))}
                        </div>
                      </div>
                    )}

                    {detectedTypes.length === 0 && (
                      <div className="pw-detect-empty">{t("wizard.noBuildSystemDetected")}</div>
                    )}

                    <label className="pw-skip-row">
                      <input type="checkbox" checked={skipSetup} onChange={(e) => setSkipSetup(e.target.checked)} />
                      <span>{t("wizard.skipDepInstall")}</span>
                    </label>
                  </>
                )}
              </div>
            )}
          </div>
        </div>

        <div className="pw-footer">
          <button className="pw-btn-ghost" onClick={handleBack}>
            <ChevronLeft size={13} /> {t("wizard.back")}
          </button>
          <button className="pw-btn-primary" onClick={handleNext} disabled={isNextDisabled()}>
            {nextLabel()}
          </button>
        </div>
      </div>
    </div>
  );
}
