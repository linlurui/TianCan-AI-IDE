import React, { useState, useEffect, useCallback, useRef } from "react";
import { useTranslation } from "../i18n";
import { GitBranch, Play, RotateCcw, Square, ChevronDown, RefreshCw, Loader2 } from "lucide-react";
import { Project } from "../types";
import { StopProject, IsRunning, ScanRunConfigs } from "../bindings/process";
import { GetDebugStatus } from "../bindings/debug";

interface RunConfig { label: string; cmd: string }

interface StatusBarProps {
  activeFile: string | null;
  isDirty: boolean;
  gitBranch: string | null;
  gitDirty: boolean;
  workingDir: string | null;
  language: string;
  cursorInfo: { line: number; col: number };
  activeProject: Project | null;
  onStartProject?: () => void;
  onStartDebug?: () => void;
}

export default function StatusBar({
  activeFile, isDirty, gitBranch, gitDirty, workingDir, language, cursorInfo, activeProject, onStartProject, onStartDebug,
}: StatusBarProps) {
  const filename = activeFile?.split("/").pop() ?? "";
  const { t } = useTranslation();

  const [running, setRunning] = useState(false);
  const [runLoading, setRunLoading] = useState(false);
  const [configs, setConfigs] = useState<RunConfig[]>([]);
  const [selectedCmd, setSelectedCmd] = useState("");
  const [showDropdown, setShowDropdown] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [debugActive, setDebugActive] = useState(false);
  const [debugAdapter, setDebugAdapter] = useState("");
  const dropdownRef = useRef<HTMLDivElement>(null);

  const scanConfigs = useCallback(async (rootPath: string) => {
    setScanning(true);
    try {
      const list = await ScanRunConfigs(rootPath);
      const cfgs = list ?? [];
      setConfigs(cfgs);
      setSelectedCmd(cfgs.length > 0 ? cfgs[0].cmd : "");
    } catch { setConfigs([]); setSelectedCmd(""); }
    finally { setScanning(false); }
  }, []);

  useEffect(() => {
    if (!activeProject) { setRunning(false); setConfigs([]); setSelectedCmd(""); setDebugActive(false); setDebugAdapter(""); return; }
    setSelectedCmd("");
    scanConfigs(activeProject.rootPath);
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const [r, ds] = await Promise.all([
        IsRunning(activeProject.rootPath).catch(() => false),
        GetDebugStatus(activeProject.rootPath).catch(() => null as any),
      ]);
      setRunning(r);
      setDebugActive(!!ds?.active);
      setDebugAdapter(ds?.adapter ?? "");
      timer = setTimeout(poll, 1500);
    };
    poll();
    return () => clearTimeout(timer);
  }, [activeProject?.rootPath]); // eslint-disable-line

  // Close dropdown on outside click
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node))
        setShowDropdown(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, []);

  const handleStart = useCallback(async () => {
    if (!activeProject) return;
    // All languages now go through the debug panel which handles
    // DAP (Go) or run-fallback (other languages) internally.
    if (onStartDebug) {
      onStartDebug();
      return;
    }
  }, [activeProject, onStartDebug]);

  const handleStop = useCallback(async () => {
    if (!activeProject) return;
    setRunLoading(true);
    try { await StopProject(activeProject.rootPath); setRunning(false); }
    catch (err) { alert(`停止失败: ${err}`); }
    finally { setRunLoading(false); }
  }, [activeProject]);

  const handleRestart = useCallback(async () => {
    if (!activeProject) return;
    if (onStartDebug) {
      onStartDebug();
      return;
    }
  }, [activeProject, onStartDebug]);

  const canRun = !!activeProject && !!onStartDebug;

  return (
    <div className="statusbar">
      {/* ── Git branch ── */}
      {gitBranch && (
        <div className="statusbar-item" title="当前 Git 分支">
          <GitBranch size={11} />
          <span>{gitBranch}{gitDirty ? " *" : ""}</span>
        </div>
      )}

      <div className="statusbar-spacer" />

      {/* ── Right: file info ── */}
      {activeFile && (
        <>
          <div className="statusbar-item">
            <span>{filename}{isDirty ? " ●" : ""}</span>
          </div>
          <div className="statusbar-item">
            <span>{language}</span>
          </div>
          <div className="statusbar-item">
            <span>行 {cursorInfo.line}，列 {cursorInfo.col}</span>
          </div>
        </>
      )}

      {/* ── Run controls ── */}
      {activeProject && (
        <>
          <div className="statusbar-sep" />

          {/* Play / restart / stop */}
          {runLoading && !running ? (
            <button className="statusbar-run-btn statusbar-run-play" disabled title="启动中...">
              <Loader2 size={11} className="spin" />
            </button>
          ) : !running ? (
            <button className="statusbar-run-btn statusbar-run-play" onClick={handleStart}
              disabled={!canRun}
              title={canRun ? "启动调试" : "请先输入或选择启动命令"}>
              <Play size={11} fill="currentColor" />
            </button>
          ) : (
            <>
              <button className="statusbar-run-btn statusbar-run-restart" onClick={handleRestart}
                disabled={runLoading} title="重启">
                <RotateCcw size={11} />
              </button>
              <button className="statusbar-run-btn statusbar-run-stop" onClick={handleStop}
                disabled={runLoading} title="停止">
                <Square size={11} fill="currentColor" />
              </button>
            </>
          )}

          {/* Config selector */}
          <div className="statusbar-config-wrap" ref={dropdownRef}>
            <div className="statusbar-config-selector" onClick={() => setShowDropdown(!showDropdown)}
              title="选择或输入启动命令">
              <input
                className="statusbar-cmd-input"
                value={selectedCmd}
                onChange={(e) => setSelectedCmd(e.target.value)}
                onClick={(e) => e.stopPropagation()}
                placeholder="输入或选择命令…"
                spellCheck={false}
              />
              <button className="statusbar-config-arrow" onClick={(e) => { e.stopPropagation(); setShowDropdown(!showDropdown); }}>
                <ChevronDown size={10} />
              </button>
            </div>

            {showDropdown && (
              <div className="statusbar-config-dropdown">
                <div className="statusbar-dropdown-header">
                  <span>检测到的配置</span>
                  <button className="statusbar-dropdown-scan" onClick={() => { activeProject && scanConfigs(activeProject.rootPath); }}
                    title="重新扫描" disabled={scanning}>
                    <RefreshCw size={10} className={scanning ? "spin" : ""} />
                  </button>
                </div>
                {configs.length === 0 && !scanning && (
                  <div className="statusbar-dropdown-empty">未检测到可运行配置</div>
                )}
                {configs.map((c) => (
                  <div key={c.cmd}
                    className={`statusbar-dropdown-item${selectedCmd === c.cmd ? " active" : ""}`}
                    onClick={() => { setSelectedCmd(c.cmd); setShowDropdown(false); }}>
                    {c.label}
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="statusbar-item statusbar-project" title={activeProject.rootPath}>
            <span>{activeProject.name}</span>
            <span className={`statusbar-runstatus ${debugActive ? "debugging" : running ? "running" : ""}`}>
              {debugActive ? "🐛" : running ? "●" : "○"}
            </span>
            {debugActive && debugAdapter && (
              <span className="statusbar-debug-adapter" title={`调试适配器: ${debugAdapter}`}>
                {debugAdapter === "dlv" ? "Go" : debugAdapter === "lldb-dap" ? "LLDB" : debugAdapter === "debugpy" ? "Python" : debugAdapter === "js-debug" ? "Node" : debugAdapter === "java-debug" ? "Java" : debugAdapter === "dart" ? "Dart" : debugAdapter}
              </span>
            )}
          </div>
        </>
      )}
    </div>
  );
}
