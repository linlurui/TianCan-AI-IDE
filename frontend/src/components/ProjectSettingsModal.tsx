import React from "react";
import { useTranslation } from "../i18n";
import { X, Check } from "lucide-react";
import { Project, ProjectType } from "../types";

interface ProjectSettingsModalProps {
  project: Project;
  onClose: () => void;
  onSave: (type: ProjectType) => void;
}

const PROJECT_TYPES: { type: ProjectType; emoji: string; label?: string; cmd?: string; labelKey?: string; cmdKey?: string }[] = [
  { type: "golang",   emoji: "🐹", label: "Go",            cmd: "go run ."         },
  { type: "java",     emoji: "☕", label: "Java / Maven",  cmd: "mvn spring-boot:run" },
  { type: "frontend", emoji: "🌐", label: "Node.js / Web", cmd: "npm run dev"      },
  { type: "python",   emoji: "🐍", label: "Python",        cmd: "python3 main.py"  },
  { type: "rust",     emoji: "⚙️", label: "Rust",          cmd: "cargo run"        },
  { type: "other",    emoji: "📦", labelKey: "projectSettings.other", cmdKey: "projectSettings.custom" },
];

export default function ProjectSettingsModal({ project, onClose, onSave }: ProjectSettingsModalProps) {
  const { t } = useTranslation();
  return (
    <div className="proj-modal-overlay" onClick={onClose}>
      <div className="proj-modal" onClick={(e) => e.stopPropagation()}>
        <div className="proj-modal-header">
          <span className="proj-modal-title">{t("projectSettings.title")} — {project.name}</span>
          <button className="proj-modal-close" onClick={onClose}><X size={14} /></button>
        </div>

        <div className="proj-modal-section">{t("projectSettings.runEnvType")}</div>
        <div className="proj-type-grid">
          {PROJECT_TYPES.map(({ type, emoji, labelKey, cmdKey }) => {
            const active = (project.type ?? "other") === type;
            const label = labelKey ? t(labelKey) : (PROJECT_TYPES.find(p => p.type === type) as any).label;
            const cmd = cmdKey ? t(cmdKey) : (PROJECT_TYPES.find(p => p.type === type) as any).cmd;
            return (
              <button
                key={type}
                className={`proj-type-btn${active ? " active" : ""}`}
                onClick={() => { onSave(type); onClose(); }}
              >
                <span className="proj-type-emoji">{emoji}</span>
                <div className="proj-type-info">
                  <span className="proj-type-label">{label}</span>
                  <span className="proj-type-cmd">{cmd}</span>
                </div>
                {active && <Check size={13} className="proj-type-check" />}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
