import React, { useCallback, useRef, useEffect } from "react";
import { useTranslation } from "./i18n";
import {
  BrainCircuit, Code2, Files, FolderPlus, GitBranch, Globe,
  LocateFixed, RefreshCw, Save, Settings, Wrench, Terminal, Bug,
  Plus, X, Puzzle, Search, Download, Trash2, Store, PackageCheck,
  ListTree, Database, FlaskConical, Rocket, ScrollText, Server,
  Network,
} from "lucide-react";

import FileTree from "./components/FileTree";
import SplitEditorArea from "./components/SplitEditorArea";
import AIPanel, { Message } from "./components/AIPanel";
import StatusBar from "./components/StatusBar";
import GitPanel from "./components/GitPanel";
// OutlinePanel and CallHierarchyPanel moved into AnalysisPanel accordion
import RemotePanel from "./components/RemotePanel";

import { ReadFile, WriteFile, GetDirectoryTree, SelectDirectory, FileNode } from "./bindings/filesystem";
import { GetStatus, InitRepo, StageAll, Commit } from "./bindings/git";
import {
  SearchExtensions, GetInstalledExtensions, InstallExtension, UninstallExtension,
  InstallLocalVSIX, GetExtensionIcon, GetExtensionContributes,
  Extension as ExtType, ExtensionContributes,
} from "./bindings/extension";
import { StartAndGetPort } from "./bindings/terminal";
import { StartAndGetPort as StartLspPort } from "./bindings/lsp";
import { AgentRun, AgentRunAPI, AgentRunWeb, StartAgentStream, RespondPermission } from "./bindings/ai";
import { Events } from "@wailsio/runtime";
import type { ModelSource } from "./components/AIPanel";
import PermissionConfirmModal, { type PermissionRequest } from "./components/PermissionConfirmModal";
import { applyExtensionContributes } from "./extensions/contributes";
import { RunSetup } from "./bindings/project";
import { Project, ProjectType } from "./types";
import { RevealSignal } from "./components/FileTree";
import TerminalPanel from "./components/TerminalPanel";
import ProjectWizard, { type WizardResult } from "./components/ProjectWizard";
import GlobalProgress, { type InstallTask } from "./components/GlobalProgress";
import ExtensionDrawer from "./components/ExtensionDrawer";
import AnalysisPanel from "./components/AnalysisPanel";
import SearchPanel from "./components/SearchPanel";
import DatabasePanel from "./components/DatabasePanel";
import TestingPanel from "./components/TestingPanel";
import ApiTestPanel from "./components/ApiTestPanel";
import PlaywrightPanel from "./components/PlaywrightPanel";
import DeployWorkspace from "./components/DeployWorkspace";
import DeployPanel from "./components/DeployPanel";
import GitDiffViewer from "./components/GitDiffViewer";
import GitCommitDetail from "./components/GitCommitDetail";
import RunEnvConfigModal from "./components/RunEnvConfigModal";
import DebugPanel, { type Breakpoint, type DebugPanelHandle } from "./components/DebugPanel";
import CommitFileDiffViewer from "./components/CommitFileDiffViewer";
import OutputPanel from "./components/OutputPanel";
import SettingsModal from "./components/SettingsModal";
import { PLUGIN_RECOMMENDATIONS, type PluginRec } from "./data/pluginRecommendations";

// ── Zustand stores ──────────────────────────────────────────────────────────
import { useEditorStore, Tab } from "./store/editorStore";
import { useProjectStore } from "./store/projectStore";
import { useUIStore, SidebarTab } from "./store/uiStore";

// ── Local state interfaces ──────────────────────────────────────────────────
interface Extension {
  id: string; name: string; technicalName: string; limited: boolean;
  publisher: string; version: string; description: string;
  installed: boolean; installing?: boolean; iconUrl?: string;
}
type ExtensionTab = "marketplace" | "installed";

// ── Helpers ─────────────────────────────────────────────────────────────────
function extOf(path: string) {
  const parts = path.split(".");
  return parts.length > 1 ? parts[parts.length - 1].toLowerCase() : "";
}

const LANG_MAP: Record<string, string> = {
  ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
  go: "go", py: "python", rs: "rust", java: "java", cpp: "cpp", c: "c",
  cs: "csharp", rb: "ruby", php: "php", swift: "swift", kt: "kotlin",
};

function langOf(path: string): string {
  const base = path.split("/").pop()?.toLowerCase() ?? "";
  if (base === "dockerfile") return "dockerfile";
  return LANG_MAP[extOf(path)] ?? "plaintext";
}

const BINARY_EXTS = new Set(["pdf", "doc", "docx", "xls", "xlsx", "csv", "ppt", "pptx"]);
const ANALYSIS_EXTS = new Set([
  "html", "htm", "xml", "css", "scss", "less", "sass",
  "ts", "tsx", "js", "jsx", "go", "py", "java", "kt", "scala",
  "rs", "c", "cpp", "cc", "h", "hpp", "php", "rb", "vue", "md", "mdx", "json",
]);

export default function App() {
  // ── Zustand ──────────────────────────────────────────────────────────────
  const editor = useEditorStore();
  const project = useProjectStore();
  const ui = useUIStore();

  // ── Local state (too small/specific for stores) ──────────────────────────
  const [messages, setMessages] = React.useState<Message[]>([]);
  const [aiLoading, setAiLoading] = React.useState(false);
  const streamRef = React.useRef<((evt: any) => void) | null>(null);
  const [permRequest, setPermRequest] = React.useState<PermissionRequest | null>(null);
  const [revealSignal, setRevealSignal] = React.useState<RevealSignal | null>(null);
  const [runEnvTarget, setRunEnvTarget] = React.useState<{ path: string; name: string; projectId?: string } | null>(null);
  const [installTasks, setInstallTasks] = React.useState<InstallTask[]>([]);
  const [extensions, setExtensions] = React.useState<Extension[]>([]);
  const [extensionSearch, setExtensionSearch] = React.useState("");
  const [extensionLoading, setExtensionLoading] = React.useState(false);
  const [extensionTab, setExtensionTab] = React.useState<ExtensionTab>("marketplace");
  const [installedExtensions, setInstalledExtensions] = React.useState<Extension[]>([]);
  const [extDragOver, setExtDragOver] = React.useState(false);
  const [extensionDetailExt, setExtensionDetailExt] = React.useState<Extension | null>(null);
  const [pluginRec, setPluginRec] = React.useState<(PluginRec & { ext: string }) | null>(null);
  const [breakpoints, setBreakpoints] = React.useState<Breakpoint[]>([]);
  const [debugPausedAt, setDebugPausedAt] = React.useState<{ file: string; line: number } | null>(null);
  const [analysisContent, setAnalysisContent] = React.useState<string | null>(null);
  const [showSettings, setShowSettings] = React.useState(false);

  const debugPanelRef = useRef<DebugPanelHandle>(null);
  const monacoGlobalRef = useRef<any>(null);
  const [monacoInstance, setMonacoInstance] = React.useState<any>(null);
  const editorInstanceRef = useRef<any>(null);
  const openedPathsRef = useRef<Set<string>>(new Set());
  const dismissedRecs = useRef<Set<string>>(new Set());
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // ── Initialization ───────────────────────────────────────────────────────
  useEffect(() => {
    StartAndGetPort().then(ui.setTerminalPort).catch(console.error);
    StartLspPort().then(ui.setLspPort).catch(console.error);
    project.loadPersistedProjects();
  }, []);

  // Listen for filetree:refresh events from backend (emitted after AI write operations)
  useEffect(() => {
    const off = Events.On("filetree:refresh", (rootPath: any) => {
      const projId = typeof rootPath === "string" ? rootPath : project.activeProject?.id;
      if (projId) project.refreshProject(projId);
    });
    return () => { Events.Off("filetree:refresh"); };
  }, [project]);

  useEffect(() => {
    loadInstalledExtensions();
  }, []);

  // Sync analysis content with active tab
  useEffect(() => {
    const activeTabData = editor.tabs.find((t) => t.path === editor.activeTab);
    setAnalysisContent(activeTabData?.content ?? null);
  }, [editor.activeTab, editor.tabs]);

  // ── Extension management ─────────────────────────────────────────────────
  const loadInstalledExtensions = useCallback(async () => {
    try {
      const list = await GetInstalledExtensions();
      const withIcons = await Promise.all(
        list.map(async (ext: ExtType) => {
          const iconUrl = await GetExtensionIcon(ext.publisher, ext.technicalName).catch(() => "");
          return { ...ext, iconUrl, installing: false };
        })
      );
      setInstalledExtensions(withIcons);
      if (monacoGlobalRef.current) {
        for (const ext of list) {
          GetExtensionContributes(ext.publisher, ext.technicalName)
            .then((contrib) => contrib && applyExtensionContributes(monacoGlobalRef.current, contrib))
            .catch(() => {});
        }
      }
    } catch (err) {
      console.error("load installed extensions:", err);
    }
  }, []);

  const doSearch = useCallback(async (query: string) => {
    setExtensionLoading(true);
    try {
      const results = await SearchExtensions(query);
      setExtensions(results);
    } catch (err) {
      console.error("搜索扩展失败:", err);
    }
    setExtensionLoading(false);
  }, []);

  const handleExtensionSearchChange = useCallback((value: string) => {
    setExtensionSearch(value);
    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(() => doSearch(value.trim()), 400);
  }, [doSearch]);

  const handleExtDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    setExtDragOver(false);
    const files = Array.from(e.dataTransfer.files).filter((f) => f.name.endsWith(".vsix"));
    for (const file of files) {
      const filePath = (file as any).path as string;
      if (!filePath) continue;
      await InstallLocalVSIX(filePath).catch(console.error);
    }
    await loadInstalledExtensions();
  }, [loadInstalledExtensions]);

  const handleUninstallExtension = useCallback(async (ext: Extension) => {
    await UninstallExtension(ext.publisher, ext.technicalName).catch(console.error);
    await loadInstalledExtensions();
  }, [loadInstalledExtensions]);

  // ── Resize handlers ──────────────────────────────────────────────────────
  const handleSidebarResize = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    const start = e.clientX, startW = ui.sidebarWidth;
    const onMove = (ev: MouseEvent) => ui.setSidebarWidth(Math.max(160, Math.min(500, startW + (ev.clientX - start))));
    const onUp = () => { document.removeEventListener("mousemove", onMove); document.removeEventListener("mouseup", onUp); };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [ui.sidebarWidth]);

  const handleToolPanelResize = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    const start = e.clientX, startW = ui.toolPanelWidth;
    const onMove = (ev: MouseEvent) => ui.setToolPanelWidth(Math.max(240, Math.min(600, startW - (ev.clientX - start))));
    const onUp = () => { document.removeEventListener("mousemove", onMove); document.removeEventListener("mouseup", onUp); };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, [ui.toolPanelWidth]);

  // ── File operations ──────────────────────────────────────────────────────
  const openFileAtLine = useCallback(async (filePath: string, line: number) => {
    try {
      const existing = editor.tabs.find((t) => t.path === filePath);
      if (!existing) {
        const content = await ReadFile(filePath);
        const name = filePath.split("/").pop() ?? filePath;
        editor.openTab({ path: filePath, name, content, isDirty: false });
      } else {
        editor.setActiveTab(filePath);
      }
      setRevealSignal({ path: filePath, ts: Date.now() });
      setTimeout(() => {
        if (editorInstanceRef.current && line > 0) {
          editorInstanceRef.current.revealLineInCenter(line);
          editorInstanceRef.current.setPosition({ lineNumber: line, column: 1 });
          editorInstanceRef.current.focus();
        }
      }, 80);
    } catch (err) { console.error("openFileAtLine:", err); }
  }, [editor]);

  const openFile = useCallback(async (node: FileNode) => {
    if (node.isDir) return;
    if (openedPathsRef.current.has(node.path)) { editor.setActiveTab(node.path); return; }
    const ext = extOf(node.path);
    openedPathsRef.current.add(node.path);

    if (BINARY_EXTS.has(ext)) {
      editor.openTab({ path: node.path, name: node.name, content: "", isDirty: false });
      ui.setActiveToolTab("analysis");
      ui.setToolPanelOpen(true);
      return;
    }
    try {
      const content = await ReadFile(node.path);
      editor.openTab({ path: node.path, name: node.name, content, isDirty: false });
      if (ANALYSIS_EXTS.has(ext)) {
        ui.setActiveToolTab("analysis");
        ui.setToolPanelOpen(true);
      }
      const rec = PLUGIN_RECOMMENDATIONS[ext];
      if (rec && !dismissedRecs.current.has(ext) && !installedExtensions.some((e) => e.id === rec.id)) {
        setPluginRec({ ...rec, ext });
      } else {
        setPluginRec(null);
      }
    } catch (err) {
      openedPathsRef.current.delete(node.path);
      console.error("open:", err);
    }
  }, [editor, ui, installedExtensions]);

  const handleSave = useCallback(async (path: string) => {
    const tab = editor.tabs.find((t) => t.path === path);
    if (!tab) return;
    await WriteFile(path, tab.content);
    editor.markTabSaved(path);
    if (project.activeProject) {
      const gitStatus = await GetStatus(project.activeProject.rootPath).catch(() => null);
      if (gitStatus) {
        project.refreshProject(project.activeProject.id);
      }
    }
  }, [editor, project]);

  // ── Extension install flow ───────────────────────────────────────────────
  const runExtensionInstalls = useCallback(async (extensionIds: string[], setupType?: string, dirPath?: string) => {
    if (extensionIds.length === 0 && !setupType) return;
    const tasks: InstallTask[] = extensionIds.map((id) => ({
      id, displayName: id.split(".").pop() ?? id, status: "pending",
    }));
    if (setupType && dirPath) tasks.push({ id: "__setup__", displayName: `运行 ${setupType} 依赖安装`, status: "pending" });
    setInstallTasks(tasks);
    ui.setShowProgress(true);

    for (const id of extensionIds) {
      if (installedExtensions.some((e) => e.id === id)) {
        setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "done" } : t));
        continue;
      }
      setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "installing" } : t));
      const parts = id.split(".");
      try {
        await InstallExtension(parts[0], parts.slice(1).join("."));
        setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "done" } : t));
      } catch {
        setInstallTasks((p) => p.map((t) => t.id === id ? { ...t, status: "error" } : t));
      }
    }

    if (setupType && dirPath) {
      setInstallTasks((p) => p.map((t) => t.id === "__setup__" ? { ...t, status: "installing" } : t));
      try {
        await RunSetup(dirPath, setupType);
        setInstallTasks((p) => p.map((t) => t.id === "__setup__" ? { ...t, status: "done" } : t));
      } catch {
        setInstallTasks((p) => p.map((t) => t.id === "__setup__" ? { ...t, status: "error" } : t));
      }
    }
    await loadInstalledExtensions();
  }, [installedExtensions, ui, loadInstalledExtensions]);

  // ── Git ──────────────────────────────────────────────────────────────────
  const handleInitRepo = useCallback(async () => {
    if (!project.activeProject) return;
    await InitRepo(project.activeProject.rootPath);
    await project.refreshProject(project.activeProject.id);
  }, [project]);

  const handleCommit = useCallback(async (message: string) => {
    if (!project.activeProject) return;
    const hash = await Commit(project.activeProject.rootPath, message, "", "");
    await project.refreshProject(project.activeProject.id);
    setMessages((p) => [...p, { role: "system", content: `已提交: ${hash.slice(0, 8)} — ${message}` }]);
  }, [project]);

  // ── Titlebar ─────────────────────────────────────────────────────────────
  const activeTabData = editor.tabs.find((t) => t.path === editor.activeTab);
  const { t, i18n, toggleLang } = useTranslation();

  const titlebarText = editor.activeTab
    ? (() => {
        const proj = project.projects.find((p) => editor.activeTab!.startsWith(p.rootPath + "/"));
        if (proj) {
          const rel = editor.activeTab!.slice(proj.rootPath.length + 1);
          const crumb = [proj.name, ...rel.split("/")].join(" › ");
          return `${crumb}${activeTabData?.isDirty ? " ●" : ""}`;
        }
        return `${activeTabData?.name ?? editor.activeTab}${activeTabData?.isDirty ? " ●" : ""}`;
      })()
    : t("app.title");

  // ── Active file lang for Outline / CallHierarchy ─────────────────────────
  const activeFileLang = editor.activeTab ? langOf(editor.activeTab) : null;

  // ── Render ───────────────────────────────────────────────────────────────
  return (
    <div className="ide-root">
      {/* Titlebar */}
      <div className="titlebar">
        <div className="titlebar-section" />
        <div className="titlebar-center">
          <span className="titlebar-title">{titlebarText}</span>
        </div>
        <div className="titlebar-section titlebar-section-right">
          <div className="mode-switch" title={ui.appMode === "editor" ? t("mode.switchToAi") : t("mode.switchToEditor")}>
            <span className="mode-label active">{ui.appMode === "editor" ? t("mode.editor") : t("mode.ai")}</span>
            <button
              className={`switch-track${ui.appMode === "ai" ? " switch-on" : ""}`}
              onClick={() => ui.setAppMode(ui.appMode === "editor" ? "ai" : "editor")}
            >
              <span className="switch-thumb" />
            </button>
          </div>
          <button className="titlebar-icon-btn" onClick={toggleLang} title={i18n.language === "zh" ? t("langSwitch.toEn") : t("langSwitch.toZh")}>
            <Globe size={13} />
            <span>{i18n.language === "zh" ? "EN" : "中"}</span>
          </button>
        </div>
      </div>

      <div className="workspace">
        {/* Activity Bar */}
        <div className="activity-bar">
          <div className="activity-bar-top">
            {(["projects", "search", "git", "database", "testing", "deploy", "extensions", "remote"] as SidebarTab[]).map((tab) => {
              const icons: Record<SidebarTab, React.ReactNode> = {
                projects: <Files size={20} strokeWidth={1.5} />,
                search: <Search size={20} strokeWidth={1.5} />,
                git: <>{<GitBranch size={20} strokeWidth={1.5} />}{project.activeProject?.gitStatus?.isDirty && <span className="activity-badge" />}</>,
                database: <Database size={20} strokeWidth={1.5} />,
                testing: <FlaskConical size={20} strokeWidth={1.5} />,
                deploy: <Rocket size={20} strokeWidth={1.5} />,
                extensions: <Puzzle size={20} strokeWidth={1.5} />,
                outline: <ListTree size={20} strokeWidth={1.5} />,
                callhierarchy: <Network size={20} strokeWidth={1.5} />,
                remote: <Server size={20} strokeWidth={1.5} />,
              };
              const titles: Record<SidebarTab, string> = {
                projects: t("sidebar.projects"), search: t("sidebar.search"), git: t("sidebar.git"),
                database: t("sidebar.database"), testing: t("sidebar.testing"), deploy: t("sidebar.deploy"),
                extensions: t("sidebar.extensions"), outline: t("sidebar.outline"), callhierarchy: t("sidebar.callhierarchy"),
                remote: t("sidebar.remote"),
              };
              return (
                <button
                  key={tab}
                  className={`activity-item${ui.sidebarOpen && ui.sidebarTab === tab ? " active" : ""}`}
                  onClick={() => ui.handleActivityClick(tab)}
                  title={titles[tab]}
                >
                  {icons[tab]}
                </button>
              );
            })}
            <button
              className={`activity-item${ui.toolPanelOpen ? " active" : ""}`}
              onClick={() => ui.setToolPanelOpen(!ui.toolPanelOpen)}
              title={t("tool.panel")}
            >
              <Wrench size={20} strokeWidth={1.5} />
            </button>
          </div>
          <div className="activity-bar-bottom">
            <button className="activity-item" title={t("action.settings")} onClick={() => setShowSettings(true)}>
              <Settings size={20} strokeWidth={1.5} />
            </button>
          </div>
        </div>

        {/* Sidebar */}
        <div className={`sidebar${ui.sidebarOpen ? "" : " sidebar-collapsed"}`} style={{ width: ui.sidebarOpen ? ui.sidebarWidth : 0 }}>

          {/* Projects */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "projects" ? "contents" : "none" }}>
            <div className="sidebar-header">
              <span className="sidebar-header-path" title={project.activeProject?.rootPath}>
                {project.activeProject ? project.activeProject.name : t("sidebar.projects")}
              </span>
              <div style={{ display: "flex", gap: 2 }}>
                {project.activeProject && (
                  <button className="sidebar-header-btn sidebar-header-btn-always"
                    onClick={() => project.refreshProject(project.activeProject!.id)} title={t("action.refresh")}>
                    <RefreshCw size={12} />
                  </button>
                )}
                <button className="sidebar-header-btn sidebar-header-btn-always"
                  onClick={() => editor.activeTab && setRevealSignal({ path: editor.activeTab, ts: Date.now() })}
                  title={t("tool.locateInTree")}
                  style={{ opacity: editor.activeTab ? 1 : 0.35 }}>
                  <LocateFixed size={12} />
                </button>
                {editor.activeTab && activeTabData?.isDirty && (
                  <button className="sidebar-header-btn sidebar-header-btn-always"
                    onClick={() => handleSave(editor.activeTab!)} title={t("action.saveFile")}>
                    <Save size={12} />
                  </button>
                )}
                <button className="sidebar-header-btn sidebar-header-btn-always"
                  onClick={() => ui.setShowWizard(true)} title={t("action.importProject")}>
                  <FolderPlus size={12} />
                </button>
              </div>
            </div>
            <FileTree
              projects={project.projects}
              activeProjectId={project.activeProjectId}
              activeFile={editor.activeTab}
              revealSignal={revealSignal}
              onFileClick={openFile}
              onProjectActivate={project.activateProject}
              onProjectRemove={project.removeProject}
              onProjectTypeChange={project.setProjectType}
              onProjectRename={project.renameProject}
              onFileRenamed={(oldPath, newPath) => {
                const name = newPath.split("/").pop() ?? newPath;
                editor.renameTab(oldPath, newPath, name);
                const proj = project.projects.find((p) => oldPath.startsWith(p.rootPath + "/"));
                if (proj) project.refreshProject(proj.id);
              }}
              onFileDeleted={(path) => {
                editor.deleteTabsByPrefix(path);
                const proj = project.projects.find((p) => path.startsWith(p.rootPath + "/"));
                if (proj) project.refreshProject(proj.id);
              }}
              onFileCreated={(path) => {
                const proj = project.projects.find((p) => path.startsWith(p.rootPath + "/"));
                if (proj) project.refreshProject(proj.id);
              }}
              onConfigureRunEnv={(path, name) => {
                const proj = project.projects.find((p) => path.startsWith(p.rootPath));
                setRunEnvTarget({ path, name, projectId: proj?.id });
              }}
            />
            {project.activeProject && (
              <div className="sidebar-footer">
                <button
                  className="sidebar-footer-btn"
                  onClick={() => project.activeProject && setRunEnvTarget({ path: project.activeProject.rootPath, name: project.activeProject.name, projectId: project.activeProject.id })}
                >
                  <Wrench size={12} />
                  <span>{project.activeProject.type ?? t("tool.configEnv")}</span>
                </button>
              </div>
            )}
          </div>

          {/* Search */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "search" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <div className="sidebar-header"><span className="sidebar-header-path">{t("sidebar.search")}</span></div>
            <SearchPanel rootPath={project.activeProject?.rootPath ?? null} projects={project.projects} onOpenFile={openFileAtLine} />
          </div>

          {/* Git */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "git" ? "contents" : "none" }}>
            <GitPanel
              status={project.activeProject?.gitStatus ?? null}
              workingDir={project.activeProject?.rootPath ?? null}
              onInitRepo={handleInitRepo}
              onStageAll={() => project.activeProject && StageAll(project.activeProject.rootPath)}
              onCommit={handleCommit}
              onRefresh={() => project.activeProject && project.refreshProject(project.activeProject.id)}
              onCommitClick={(commit) => {
                if (!project.activeProject) return;
                const tabPath = `__commit_detail__:${project.activeProject.rootPath}:${commit.hash}`;
                if (!editor.tabs.find((t) => t.path === tabPath)) {
                  editor.openTab({ path: tabPath, name: `${commit.hash.slice(0, 7)} ${commit.message.split("\n")[0].slice(0, 30)}`, content: "", isDirty: false });
                }
                editor.setActiveTab(tabPath);
              }}
              onOpenFile={(relPath) => {
                if (!project.activeProject) return;
                if (relPath.startsWith("__commit_diff__:")) {
                  const parts = relPath.split(":");
                  if (parts.length >= 3) {
                    const hash = parts[1], filePath = parts.slice(2).join(":");
                    const fileName = filePath.split("/").pop() ?? filePath;
                    const tabPath = `__commit_diff__:${hash}:${filePath}`;
                    if (!editor.tabs.find((t) => t.path === tabPath))
                      editor.openTab({ path: tabPath, name: `${fileName} (${hash.substring(0, 7)})`, content: "", isDirty: false });
                    editor.setActiveTab(tabPath);
                  }
                  return;
                }
                const diffPath = `__diff__:${project.activeProject.rootPath}:${relPath}`;
                const fileName = relPath.split("/").pop() ?? relPath;
                if (!editor.tabs.find((t) => t.path === diffPath))
                  editor.openTab({ path: diffPath, name: `${fileName} (${t("tool.compare")})`, content: "", isDirty: false });
                editor.setActiveTab(diffPath);
              }}
            />
          </div>

          {/* Remote */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "remote" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <div className="sidebar-header"><span className="sidebar-header-path">{t("remote.title")}</span></div>
            <RemotePanel onOpenRemoteFile={(connID, path, type) => console.log("remote open", connID, path, type)} />
          </div>

          {/* Database */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "database" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <DatabasePanel />
          </div>

          {/* Deploy */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "deploy" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <DeployPanel onOpenDeploy={() => {
              const path = "__deploy__";
              if (!editor.tabs.find((t) => t.path === path))
                editor.openTab({ path, name: t("deploy.console"), content: "", isDirty: false });
              editor.setActiveTab(path);
            }} />
          </div>

          {/* Testing */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "testing" ? "flex" : "none", flexDirection: "column", height: "100%", overflow: "hidden" }}>
            <TestingPanel
              onOpenApiTest={() => {
                const path = "__api_test__";
                if (!editor.tabs.find((t) => t.path === path))
                  editor.openTab({ path, name: t("testing.apiName"), content: "", isDirty: false });
                editor.setActiveTab(path);
              }}
              onOpenPlaywright={() => {
                const path = "__playwright__";
                if (!editor.tabs.find((t) => t.path === path))
                  editor.openTab({ path, name: "Playwright", content: "", isDirty: false });
                editor.setActiveTab(path);
              }}
            />
          </div>

          {/* Extensions */}
          <div className="sidebar-panel" style={{ display: ui.sidebarTab === "extensions" ? "contents" : "none" }}>
            <div className="sidebar-header"><span className="sidebar-header-path">{t("extension.title")}</span></div>
            <div className="extension-panel-sidebar">
              <div className="ext-tab-bar">
                <button className={`ext-tab${extensionTab === "marketplace" ? " active" : ""}`}
                  onClick={() => setExtensionTab("marketplace")}>
                  <Store size={12} />{t("extension.marketplace")}
                </button>
                <button className={`ext-tab${extensionTab === "installed" ? " active" : ""}`}
                  onClick={() => { setExtensionTab("installed"); loadInstalledExtensions(); }}>
                  <PackageCheck size={12} />{t("extension.installed")}
                  {installedExtensions.length > 0 && <span className="ext-tab-badge">{installedExtensions.length}</span>}
                </button>
              </div>

              {extensionTab === "marketplace" && (
                <>
                  <div className="extension-search">
                    <Search size={14} />
                    <input type="text" placeholder={t("extension.searchPlaceholder")}
                      value={extensionSearch} onChange={(e) => handleExtensionSearchChange(e.target.value)} />
                  </div>
                  <div className="extension-list">
                    {extensionLoading ? (
                      <div className="extension-loading">{t("extension.searching")}</div>
                    ) : extensions.length === 0 ? (
                      <div className="extension-empty">
                        <Puzzle size={48} strokeWidth={1} />
                        <span>{t("extension.emptyTitle")}</span>
                        <p>{t("extension.emptyDesc")}</p>
                      </div>
                    ) : extensions.filter((ext) =>
                      ext.name.toLowerCase().includes(extensionSearch.toLowerCase()) ||
                      ext.publisher.toLowerCase().includes(extensionSearch.toLowerCase())
                    ).map((ext) => (
                      <div key={ext.id} className="extension-item">
                        <div className="extension-icon">
                          {ext.iconUrl ? <img src={ext.iconUrl} alt={ext.name} className="ext-icon-img" /> : <Puzzle size={24} />}
                        </div>
                        <div className="extension-info">
                          <div className="extension-name">{ext.name}{ext.limited && <span className="ext-limited-badge" title={t("tool.limited")}>⚠ {t("tool.limited")}</span>}</div>
                          <div className="extension-publisher">{ext.publisher} · v{ext.version}</div>
                          <div className="extension-desc">{ext.description}</div>
                        </div>
                        <button className={`extension-btn${ext.installed ? " installed" : ""}`} disabled={ext.installing}
                          onClick={async () => {
                            if (ext.installed) return;
                            setExtensions((p) => p.map((e) => e.id === ext.id ? { ...e, installing: true } : e));
                            let ok = false;
                            try { await InstallExtension(ext.publisher, ext.technicalName); ok = true; }
                            catch (err) { alert(`${t("extension.installFailed")}：${err}`); }
                            finally {
                              setExtensions((p) => p.map((e) => e.id === ext.id ? { ...e, installing: false, installed: ok } : e));
                              if (ok) await loadInstalledExtensions();
                            }
                          }}>
                          {ext.installing ? t("extension.installing") : ext.installed ? t("extension.installedBtn") : <Download size={14} />}
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}

              {extensionTab === "installed" && (
                <>
                  <div className={`ext-drop-zone${extDragOver ? " drag-over" : ""}`}
                    onDragOver={(e) => { e.preventDefault(); setExtDragOver(true); }}
                    onDragLeave={() => setExtDragOver(false)} onDrop={handleExtDrop}>
                    <Download size={16} /><span>{t("extension.dropHint")}</span>
                  </div>
                  <div className="extension-list">
                    {installedExtensions.length === 0 ? (
                      <div className="extension-empty">
                        <PackageCheck size={48} strokeWidth={1} />
                        <span>{t("extension.noInstalled")}</span>
                      </div>
                    ) : installedExtensions.map((ext) => (
                      <div key={ext.id} className="extension-item" onClick={() => setExtensionDetailExt(ext)} style={{ cursor: "pointer" }}>
                        <div className="extension-icon">
                          {ext.iconUrl ? <img src={ext.iconUrl} alt={ext.name} className="ext-icon-img" /> : <Puzzle size={24} />}
                        </div>
                        <div className="extension-info">
                          <div className="extension-name">{ext.name}</div>
                          <div className="extension-publisher">{ext.publisher} · v{ext.version}</div>
                          <div className="extension-desc">{ext.description}</div>
                        </div>
                        <button className="extension-btn extension-btn-uninstall" title={t("extension.uninstall")}
                          onClick={(e) => { e.stopPropagation(); handleUninstallExtension(ext); }}>
                          <Trash2 size={14} />
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
          </div>
        </div>

        {ui.sidebarOpen && <div className="resize-handle" onMouseDown={handleSidebarResize} />}

        {/* Main editor area */}
        {ui.appMode === "editor" ? (
          <SplitEditorArea
            lspPort={ui.lspPort || undefined}
            rootPath={project.activeProject?.rootPath}
            onMonacoMount={(m) => { monacoGlobalRef.current = m; setMonacoInstance(m); }}
            onEditorMount={(e) => { editorInstanceRef.current = e; }}
            breakpoints={breakpoints}
            onBreakpointToggle={(file, line) => {
              setBreakpoints((p) => {
                const exists = p.some((bp) => bp.file === file && bp.line === line);
                return exists
                  ? p.filter((bp) => !(bp.file === file && bp.line === line))
                  : [...p, { file, line, enabled: true }];
              });
            }}
            pausedAt={debugPausedAt}
            onEvaluateExpr={(expr) => debugPanelRef.current?.evaluate(expr) ?? Promise.resolve(null)}
            onCursorChange={(line, col) => editor.setCursorInfo(line, col)}
            onSave={handleSave}
            renderSpecial={(path) => {
              if (path === "__api_test__") return <ApiTestPanel />;
              if (path === "__playwright__") return <PlaywrightPanel />;
              if (path === "__deploy__") return <DeployWorkspace />;
              if (path.startsWith("__commit_diff__:")) {
                const parts = path.split(":");
                if (parts.length >= 3 && project.activeProject) {
                  return <CommitFileDiffViewer repoPath={project.activeProject.rootPath} commitHash={parts[1]} filePath={parts.slice(2).join(":")} />;
                }
              }
              if (path.startsWith("__diff__:")) {
                const [, repoPath, ...rest] = path.split(":");
                return <GitDiffViewer repoPath={repoPath} relPath={rest.join(":")} />;
              }
              if (path.startsWith("__commit_detail__:")) {
                const [, repoPath, hash] = path.split(":");
                const tab = editor.tabs.find((t) => t.path === path);
                const commit = { hash, message: tab?.name?.split(" ").slice(1).join(" ") ?? "", author: "", when: "" };
                return <GitCommitDetail repoPath={repoPath} commit={commit} onRefresh={() => project.activeProject && project.refreshProject(project.activeProject.id)} />;
              }
              return null;
            }}
            pluginRecBanner={pluginRec ? (
              <div className="plugin-rec-banner">
                <span className="plugin-rec-icon">🔌</span>
                <span className="plugin-rec-text">
                  {t("extension.recDetected")} <strong>.{pluginRec.ext}</strong> {t("extension.recInstall")} <strong>{pluginRec.name}</strong>
                </span>
                <button className="plugin-rec-btn plugin-rec-install"
                  onClick={async () => { setPluginRec(null); dismissedRecs.current.add(pluginRec.ext); await runExtensionInstalls([pluginRec.id]); }}>
                  {t("extension.installBtn")}
                </button>
                <button className="plugin-rec-btn plugin-rec-dismiss"
                  onClick={() => { dismissedRecs.current.add(pluginRec.ext); setPluginRec(null); }}>
                  {t("extension.ignore")}
                </button>
              </div>
            ) : null}
          />
        ) : (
          <AIPanel messages={messages} onSend={async (text, source: ModelSource) => {
            if (text === "__clear__") {
              // Cleanup any active stream listener
              if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }
              setMessages([]); return;
            }
            setMessages((p) => [...p, { role: "user", content: text }]);
            setAiLoading(true);

            const rootPath = project.activeProject?.rootPath ?? "";

            // Use streaming for local mode (LM Studio supports ChatStream)
            if (source === "local") {
              // Remove previous listener if any
              if (streamRef.current) { Events.Off("agent:stream"); streamRef.current = null; }

              // Add a placeholder assistant message for streaming tokens
              setMessages((p) => [...p, { role: "assistant", content: "" }]);

              const handler = (evt: any) => {
                const data = evt?.data ?? evt;
                const eType = data?.type ?? "";
                const eContent = data?.content ?? "";
                const eName = data?.name ?? "";
                const eThinking = data?.thinking ?? "";
                switch (eType) {
                  case "token":
                    // Append token to the last assistant message
                    setMessages((p) => {
                      const updated = [...p];
                      const last = updated[updated.length - 1];
                      if (last && last.role === "assistant") {
                        updated[updated.length - 1] = { ...last, content: last.content + eContent };
                      }
                      return updated;
                    });
                    break;
                  case "tool_call":
                    setMessages((p) => {
                      const updated = [...p];
                      const last = updated[updated.length - 1];
                      // Convert last assistant message to thinking with backend-extracted content
                      if (last && last.role === "assistant") {
                        const thinkContent = eThinking || last.content;
                        updated[updated.length - 1] = {
                          ...last,
                          role: "thinking",
                          content: thinkContent.trim() ? thinkContent : last.content,
                        };
                      }
                      // Add tool_call card (empty assistant will be added by tool_result)
                      updated.push(
                        { role: "tool_call" as any, content: eContent, name: eName },
                      );
                      return updated;
                    });
                    break;
                  case "tool_result":
                    setMessages((p) => [...p, { role: "tool_result" as any, content: eContent, name: eName }, { role: "assistant", content: "" }]);
                    break;
                  case "permission_request":
                    setPermRequest({
                      id: data?.id ?? "",
                      toolName: eName,
                      args: eContent,
                      reason: data?.reason ?? "",
                    });
                    break;
                  case "done":
                    // Remove trailing empty assistant message if any
                    setMessages((p) => {
                      const updated = [...p];
                      const last = updated[updated.length - 1];
                      if (last && last.role === "assistant" && !last.content.trim()) {
                        updated.pop();
                      }
                      return updated;
                    });
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                  case "stream_end":
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                  case "error":
                    setMessages((p) => [...p, { role: "assistant", content: `❌ ${eContent}` }]);
                    setAiLoading(false);
                    Events.Off("agent:stream");
                    streamRef.current = null;
                    break;
                }
              };

              Events.On("agent:stream", handler);
              streamRef.current = handler;

              try {
                await StartAgentStream(text, rootPath, 20);
              } catch (err: any) {
                Events.Off("agent:stream");
                streamRef.current = null;
                setMessages((p) => [...p, { role: "assistant", content: `❌ AI request failed: ${err?.message ?? err}` }]);
                setAiLoading(false);
              }
            } else {
              // Non-local modes: use blocking AgentRun (API/Web don't support streaming yet)
              try {
                let result;
                if (source === "web") {
                  result = await AgentRunWeb(text, rootPath, 20);
                } else {
                  result = await AgentRunAPI(text, rootPath, 20);
                }
                // Show all assistant / tool messages returned by agent.
                // Intermediate assistant messages (followed by tool messages) are reasoning → 'thinking' role.
                const rawMsgs = (result.messages ?? []).filter((m: any) => m.role !== "user");
                const agentMsgs: Array<{role: any; content: string; name?: string}> = [];
                for (let mi = 0; mi < rawMsgs.length; mi++) {
                  const m = rawMsgs[mi];
                  const next = mi + 1 < rawMsgs.length ? rawMsgs[mi + 1] : null;
                  if (m.role === "assistant" && next && (next.role === "tool" || next.role === "tool_call" || next.role === "tool_result")) {
                    // This assistant msg is reasoning (not the final reply)
                    agentMsgs.push({ role: "thinking", content: m.content });
                  } else if (m.role === "tool") {
                    agentMsgs.push({ role: "tool_result" as any, content: m.content });
                  } else {
                    agentMsgs.push({ role: m.role as any, content: m.content });
                  }
                }
                if (agentMsgs.length > 0) {
                  setMessages((p) => [...p, ...agentMsgs]);
                } else {
                  setMessages((p) => [...p, {
                    role: "assistant",
                    content: result.error ? `❌ ${result.error}` : "(no response)",
                  }]);
                }
              } catch (err: any) {
                setMessages((p) => [...p, { role: "assistant", content: `❌ AI request failed: ${err?.message ?? err}` }]);
              } finally {
                setAiLoading(false);
              }
            }
          }} isLoading={aiLoading} fullWidth />
        )}

        {/* Tool Panel */}
        {ui.toolPanelOpen && (
          <>
            <div className="resize-handle" onMouseDown={handleToolPanelResize} />
            <div className="tool-panel" style={{ width: ui.toolPanelWidth }}>
              <div className="tool-panel-header">
                <span className="tool-panel-title">
                  {ui.activeToolTab === "terminal" ? t("terminal.title") :
                   ui.activeToolTab === "analysis" ? t("tool.analysis") :
                   ui.activeToolTab === "debug" ? t("tool.debug") :
                   ui.activeToolTab === "modify" ? t("tool.runOutput") :
                   installedExtensions.find((ext) => ext.id === ui.activeToolTab)?.name ?? t("tool.modify")}
                </span>
                <button className="tool-panel-close" onClick={() => ui.setToolPanelOpen(false)} title={t("tool.close")}>
                  <X size={14} />
                </button>
              </div>
              <div className="tool-panel-body">
                {ui.activeToolTab === "terminal" && (
                  <div className="tool-tab-content">
                    <div className="terminal-tabs">
                      <div className="terminal-tabs-scroll">
                        {ui.terminals.map((term, idx) => (
                          <div key={term.id} className={`terminal-tab${ui.activeTerminalId === term.id ? " active" : ""}`}
                            onClick={() => ui.setActiveTerminalId(term.id)}>
                            <span>{t("terminal.tabName").replace("{{n}}", String(idx + 1))}</span>
                            {ui.terminals.length > 1 && (
                              <button className="terminal-tab-close"
                                onClick={(e) => { e.stopPropagation(); ui.removeTerminal(term.id); }}>×</button>
                            )}
                          </div>
                        ))}
                      </div>
                      <button className="terminal-add-btn" onClick={() => ui.addTerminal()} title={t("terminal.add")}>
                        <Plus size={12} />
                      </button>
                    </div>
                    <div className="terminal-content">
                      {ui.terminals.map((term) => (
                        <TerminalPanel key={term.id} port={ui.terminalPort}
                          active={ui.activeToolTab === "terminal" && ui.toolPanelOpen && ui.activeTerminalId === term.id}
                          hidden={ui.activeTerminalId !== term.id}
                          initCmd={term.initCmd} />
                      ))}
                    </div>
                  </div>
                )}
                {ui.activeToolTab === "analysis" && (
                  <div className="tool-tab-content" style={{ overflowY: "auto", padding: 0 }}>
                    <AnalysisPanel
                      filePath={editor.activeTab}
                      content={analysisContent}
                      onGoToLine={(line) => editor.activeTab && openFileAtLine(editor.activeTab, line)}
                      rootPath={project.activeProject?.rootPath ?? null}
                      langId={activeFileLang}
                      cursorLine={editor.cursorInfo.line}
                      cursorCol={editor.cursorInfo.col}
                      onOpenFile={(path, line) => openFileAtLine(path, line)}
                      lspPort={ui.lspPort || undefined}
                      monaco={monacoInstance}
                    />
                  </div>
                )}
                <div className="tool-tab-content" style={{ padding: 0, overflow: "hidden", display: ui.activeToolTab === "debug" ? "flex" : "none", flexDirection: "column" }}>
                  <DebugPanel ref={debugPanelRef} rootPath={project.activeProject?.rootPath ?? null}
                    activeFile={editor.activeTab} breakpoints={breakpoints}
                    projectType={project.activeProject?.type ?? null}
                    onPauseAt={(file, line) => { setDebugPausedAt({ file, line }); openFileAtLine(file, line); }}
                    onNavigate={openFileAtLine}
                    onDeleteBreakpoint={(file, line) => setBreakpoints((p) => p.filter((bp) => !(bp.file === file && bp.line === line)))}
                    onClearBreakpoints={() => setBreakpoints([])}
                    onToggleBreakpoint={(file, line, enabled) => setBreakpoints((p) => p.map((bp) => bp.file === file && bp.line === line ? { ...bp, enabled } : bp))}
                    onUpdateBreakpoint={(file, line, patch) => setBreakpoints((p) => p.map((bp) => bp.file === file && bp.line === line ? { ...bp, ...patch } : bp))}
                    onDebugStateChange={(s) => { if (s !== "paused") setDebugPausedAt(null); }} />
                </div>
                {ui.activeToolTab === "modify" && (
                  <div className="tool-tab-content" style={{ overflowY: "auto", padding: 0 }}>
                    <OutputPanel projectRootPath={project.activeProject?.rootPath ?? null} />
                  </div>
                )}
              </div>
              <div className="tool-panel-footer">
                <button className={`tool-footer-tab${ui.activeToolTab === "analysis" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("analysis")} title={t("tool.analysis")}>
                  <ListTree size={16} />
                </button>
                <button className={`tool-footer-tab${ui.activeToolTab === "debug" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("debug")} title={t("tool.debug")}>
                  <Bug size={16} />
                </button>
                <button className={`tool-footer-tab${ui.activeToolTab === "terminal" ? " active" : ""}`}
                  onClick={() => ui.setActiveToolTab("terminal")} title={t("terminal.title")}>
                  <Terminal size={16} />
                </button>
              </div>
            </div>
          </>
        )}
      </div>

      <StatusBar
        activeFile={editor.activeTab}
        isDirty={activeTabData?.isDirty ?? false}
        gitBranch={project.activeProject?.gitStatus?.branch ?? null}
        gitDirty={project.activeProject?.gitStatus?.isDirty ?? false}
        workingDir={project.activeProject?.rootPath ?? null}
        language={editor.activeTab ? extOf(editor.activeTab) : ""}
        cursorInfo={editor.cursorInfo}
        activeProject={project.activeProject}
        onStartProject={() => { ui.setToolPanelOpen(true); ui.setActiveToolTab("modify"); }}
        onStartDebug={() => { ui.setToolPanelOpen(true); ui.setActiveToolTab("debug"); debugPanelRef.current?.startDebug(); }}
      />

      {runEnvTarget && (
        <RunEnvConfigModal
          projectName={runEnvTarget.name}
          rootPath={runEnvTarget.path}
          projectType={runEnvTarget.projectId ? project.projects.find((p) => p.id === runEnvTarget.projectId)?.type : undefined}
          onSaveType={(type) => runEnvTarget.projectId && project.setProjectType(runEnvTarget.projectId, type)}
          onClose={() => setRunEnvTarget(null)}
        />
      )}

      {ui.showWizard && (
        <ProjectWizard
          onCancel={() => ui.setShowWizard(false)}
          onConfirm={(result: WizardResult) => {
            ui.setShowWizard(false);
            project.importProject(result.dirPath).catch(console.error);
            runExtensionInstalls(result.extensionsToInstall, result.setupType, result.dirPath);
          }}
        />
      )}

      <GlobalProgress tasks={installTasks} visible={ui.showProgress} onDismiss={() => ui.setShowProgress(false)} />

      {showSettings && <SettingsModal onClose={() => setShowSettings(false)} />}

      <PermissionConfirmModal
        request={permRequest}
        onRespond={async (requestID, approved) => {
          setPermRequest(null);
          try { await RespondPermission(requestID, approved); } catch { /* ignore */ }
        }}
      />

      <ExtensionDrawer
        ext={extensionDetailExt}
        lspPort={ui.lspPort}
        rootPath={project.activeProject?.rootPath}
        monaco={monacoInstance}
        onClose={() => setExtensionDetailExt(null)}
        onUninstall={(e) => { handleUninstallExtension(e); setExtensionDetailExt(null); }}
      />
    </div>
  );
}
