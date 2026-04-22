import React, { useState, useEffect, useCallback } from "react";
import { useTranslation } from "../i18n";
import { Play, Square, RotateCcw, Terminal } from "lucide-react";
import { Project } from "../types";
import { StartProject, StopProject, IsRunning, DetectRunCommand } from "../bindings/process";

interface RunBarProps {
  activeProject: Project | null;
}

export default function RunBar({ activeProject }: RunBarProps) {
  const { t } = useTranslation();
  const [running, setRunning] = useState(false);
  const [loading, setLoading] = useState(false);
  const [runCmd, setRunCmd] = useState("");

  useEffect(() => {
    if (!activeProject) {
      setRunning(false);
      setRunCmd("");
      return;
    }

    DetectRunCommand(activeProject.rootPath).then(setRunCmd).catch(() => setRunCmd(""));

    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const r = await IsRunning(activeProject.rootPath).catch(() => false);
      setRunning(r);
      timer = setTimeout(poll, 1500);
    };
    poll();
    return () => clearTimeout(timer);
  }, [activeProject?.rootPath]);

  const handleStart = useCallback(async () => {
    if (!activeProject) return;
    setLoading(true);
    try {
      await StartProject(activeProject.rootPath);
      setRunning(true);
    } catch (err) {
      alert(t("runBar.startFail") + ": " + err);
    } finally {
      setLoading(false);
    }
  }, [activeProject]);

  const handleStop = useCallback(async () => {
    if (!activeProject) return;
    setLoading(true);
    try {
      await StopProject(activeProject.rootPath);
      setRunning(false);
    } catch (err) {
      alert(t("runBar.stopFail") + ": " + err);
    } finally {
      setLoading(false);
    }
  }, [activeProject]);

  const handleRestart = useCallback(async () => {
    if (!activeProject) return;
    setLoading(true);
    try {
      await StopProject(activeProject.rootPath).catch(() => {});
      await StartProject(activeProject.rootPath);
      setRunning(true);
    } catch (err) {
      alert(t("runBar.restartFail") + ": " + err);
    } finally {
      setLoading(false);
    }
  }, [activeProject]);

  if (!activeProject) return null;

  return (
    <div className="run-bar">
      <Terminal size={12} style={{ color: "var(--text-muted)", flexShrink: 0 }} />
      <span className="run-bar-project" title={activeProject.rootPath}>
        {activeProject.name}
      </span>
      {runCmd && (
        <span className="run-bar-cmd">{runCmd}</span>
      )}
      <div className="run-bar-divider" />
      <div className="run-bar-actions">
        {!running ? (
          <button
            className="run-bar-btn run-bar-btn-play"
            onClick={handleStart}
            disabled={loading || !runCmd}
            title={runCmd ? t("runBar.startWith", { cmd: runCmd }) : t("runBar.unrecognizedType")}
          >
            <Play size={12} fill="currentColor" />
          </button>
        ) : (
          <>
            <button
              className="run-bar-btn run-bar-btn-restart"
              onClick={handleRestart}
              disabled={loading}
              title={t("runBar.restart")}
            >
              <RotateCcw size={12} />
            </button>
            <button
              className="run-bar-btn run-bar-btn-stop"
              onClick={handleStop}
              disabled={loading}
              title={t("runBar.stop")}
            >
              <Square size={12} fill="currentColor" />
            </button>
          </>
        )}
        <span className={`run-bar-status ${running ? "running" : "stopped"}`}>
          {running ? `● ${t("runBar.running")}` : `○ ${t("runBar.stopped")}`}
        </span>
      </div>
    </div>
  );
}
