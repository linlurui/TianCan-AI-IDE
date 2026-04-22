import React, { useState } from "react";
import { useTranslation } from "../i18n";
import { CheckCircle2, XCircle, Loader2, ChevronDown, ChevronUp, X } from "lucide-react";

export interface InstallTask {
  id: string;
  displayName: string;
  status: "pending" | "installing" | "done" | "error";
}

interface Props {
  tasks: InstallTask[];
  visible: boolean;
  onDismiss: () => void;
}

export default function GlobalProgress({ tasks, visible, onDismiss }: Props) {
  const { t } = useTranslation();
  const [minimized, setMinimized] = useState(false);

  if (!visible || tasks.length === 0) return null;

  const done = tasks.filter((t) => t.status === "done" || t.status === "error").length;
  const total = tasks.length;
  const allFinished = done === total;
  const pct = Math.round((done / total) * 100);

  return (
    <div className={`global-progress${minimized ? " minimized" : ""}${allFinished ? " finished" : ""}`}>
      <div className="gp-header">
        <div className="gp-header-left">
          {!allFinished && <Loader2 size={13} className="spin gp-spinner" />}
          {allFinished && <CheckCircle2 size={13} className="gp-done-icon" />}
          <span className="gp-title">
            {allFinished ? t("progress.envConfigDone") : t("progress.configuringEnv", { done, total })}
          </span>
        </div>
        <div className="gp-header-actions">
          <button className="gp-action-btn" onClick={() => setMinimized((v) => !v)} title={minimized ? t("progress.expand") : t("progress.collapse")}>
            {minimized ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
          </button>
          {allFinished && (
            <button className="gp-action-btn" onClick={onDismiss} title={t("progress.close")}>
              <X size={12} />
            </button>
          )}
        </div>
      </div>

      {!minimized && (
        <>
          <div className="gp-bar-track">
            <div className="gp-bar-fill" style={{ width: `${pct}%` }} />
          </div>

          <div className="gp-task-list">
            {tasks.map((task) => (
              <div key={task.id} className={`gp-task gp-task-${task.status}`}>
                <span className="gp-task-icon">
                  {task.status === "done" && <CheckCircle2 size={12} />}
                  {task.status === "error" && <XCircle size={12} />}
                  {task.status === "installing" && <Loader2 size={12} className="spin" />}
                  {task.status === "pending" && <span className="gp-task-dot" />}
                </span>
                <span className="gp-task-name">{task.displayName}</span>
                <span className="gp-task-label">
                  {task.status === "done" && t("progress.done")}
                  {task.status === "error" && t("progress.failed")}
                  {task.status === "installing" && t("progress.installing")}
                  {task.status === "pending" && t("progress.pending")}
                </span>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
