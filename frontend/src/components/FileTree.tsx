import React, { useState, useEffect, useRef, useCallback } from "react";
import { useTranslation } from "../i18n";
import {
  Folder, FolderOpen, File, FileCode, FileText, FileJson,
  FileImage, ChevronRight, ChevronDown, Loader2, Pencil, Trash2,
  FilePlus, FolderPlus, Wrench,
} from "lucide-react";
import { FileNode, ListDirectory, RenameFile, DeleteFile, CreateFile, MkDir, WriteFile } from "../bindings/filesystem";
import { Project, ProjectType } from "../types";

export interface RevealSignal { path: string; ts: number }

interface FileTreeProps {
  projects: Project[];
  activeProjectId: string | null;
  activeFile: string | null;
  revealSignal?: RevealSignal | null;
  onFileClick: (node: FileNode) => void;
  onProjectActivate: (id: string) => void;
  onProjectRemove: (id: string) => void;
  onProjectTypeChange: (id: string, type: ProjectType) => void;
  onProjectRename: (id: string, newPath: string, newName: string) => void;
  onFileRenamed: (oldPath: string, newPath: string) => void;
  onFileDeleted: (path: string) => void;
  onFileCreated: (path: string) => void;
  onConfigureRunEnv?: (path: string, name: string) => void;
}

interface IconDef { Icon: React.ElementType; color: string }

const EXT_ICONS: Record<string, IconDef> = {
  ts:  { Icon: FileCode,  color: "#007acc" },
  tsx: { Icon: FileCode,  color: "#007acc" },
  js:  { Icon: FileCode,  color: "#f7df1e" },
  jsx: { Icon: FileCode,  color: "#f7df1e" },
  go:  { Icon: FileCode,  color: "#00add8" },
  py:  { Icon: FileCode,  color: "#4b8bbe" },
  rs:  { Icon: FileCode,  color: "#ce4a0c" },
  java:{ Icon: FileCode,  color: "#b07219" },
  json:{ Icon: FileJson,  color: "#f7d37e" },
  yaml:{ Icon: FileText,  color: "#6a9955" },
  yml: { Icon: FileText,  color: "#6a9955" },
  toml:{ Icon: FileText,  color: "#6a9955" },
  md:  { Icon: FileText,  color: "#519aba" },
  txt: { Icon: FileText,  color: "#858585" },
  html:{ Icon: FileCode,  color: "#e34c26" },
  css: { Icon: FileCode,  color: "#f076c0" },
  scss:{ Icon: FileCode,  color: "#f076c0" },
  svg: { Icon: FileImage, color: "#f5a524" },
  png: { Icon: FileImage, color: "#a37acc" },
  jpg: { Icon: FileImage, color: "#a37acc" },
  jpeg:{ Icon: FileImage, color: "#a37acc" },
  gif: { Icon: FileImage, color: "#a37acc" },
  sh:  { Icon: FileCode,  color: "#89d185" },
  bash:{ Icon: FileCode,  color: "#89d185" },
  zsh: { Icon: FileCode,  color: "#89d185" },
  env: { Icon: FileText,  color: "#858585" },
  mod: { Icon: FileText,  color: "#00add8" },
  sum: { Icon: FileText,  color: "#858585" },
};

const PROJECT_TYPE_COLORS: Record<ProjectType, string> = {
  java:     "#b07219",
  frontend: "#f7df1e",
  golang:   "#00add8",
  python:   "#4b8bbe",
  rust:     "#ce4a0c",
  other:    "#858585",
};

function getFileDef(node: FileNode): IconDef {
  if (node.isDir) return { Icon: Folder, color: "#e8c14b" };
  const ext = node.ext?.toLowerCase() ?? "";
  return EXT_ICONS[ext] ?? EXT_ICONS[node.name] ?? { Icon: File, color: "#858585" };
}

interface TreeNodeProps {
  node: FileNode;
  depth: number;
  activeFile: string | null;
  expandedDirs: Set<string>;
  lazyChildren: Map<string, FileNode[]>;
  loadingDirs: Set<string>;
  renamingPath: string | null;
  renameValue: string;
  creatingIn: { dir: string; kind: "file" | "dir" } | null;
  createName: string;
  onToggleDir: (path: string) => void;
  onFileClick: (node: FileNode) => void;
  onRenameValueChange: (v: string) => void;
  onRenameCommit: (path: string) => void;
  onRenameCancel: () => void;
  onNodeContextMenu: (e: React.MouseEvent, node: FileNode) => void;
  onCreateNameChange: (v: string) => void;
  onCreateCommit: () => void;
  onCreateCancel: () => void;
}

function TreeNode({
  node, depth, activeFile, expandedDirs, lazyChildren, loadingDirs,
  renamingPath, renameValue, creatingIn, createName,
  onToggleDir, onFileClick,
  onRenameValueChange, onRenameCommit, onRenameCancel, onNodeContextMenu,
  onCreateNameChange, onCreateCommit, onCreateCancel,
}: TreeNodeProps) {
  const { t } = useTranslation();
  const expanded = expandedDirs.has(node.path);
  // Prefer lazyChildren when present (post-refresh), fall back to static tree
  const resolvedChildren: FileNode[] = lazyChildren.has(node.path)
    ? (lazyChildren.get(node.path) ?? [])
    : (node.children ?? []);
  const isLoading = loadingDirs.has(node.path);
  const isRenaming = renamingPath === node.path;

  const handleClick = () => {
    if (isRenaming) return;
    if (node.isDir) onToggleDir(node.path);
    else onFileClick(node);
  };

  const { Icon, color } = node.isDir
    ? { Icon: expanded ? FolderOpen : Folder, color: "#e8c14b" }
    : getFileDef(node);

  return (
    <>
      <div
        className={`tree-item ${node.isDir ? "is-dir" : ""} ${activeFile === node.path ? "active" : ""}`}
        style={{ paddingLeft: `${8 + depth * 14}px` }}
        onClick={handleClick}
        onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); onNodeContextMenu(e, node); }}
        title={isRenaming ? undefined : node.path}
        data-filepath={node.path}
      >
        {node.isDir && (
          <span className="tree-chevron">
            {isLoading
              ? <Loader2 size={12} color="var(--accent)" className="spin" />
              : expanded
                ? <ChevronDown size={12} color="var(--text-muted)" />
                : <ChevronRight size={12} color="var(--text-muted)" />}
          </span>
        )}
        <span className="tree-icon"><Icon size={15} color={color} strokeWidth={1.5} /></span>
        {isRenaming ? (
          <input
            className="tree-rename-input"
            value={renameValue}
            autoFocus
            onFocus={(e) => e.target.select()}
            onChange={(e) => onRenameValueChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") { e.preventDefault(); onRenameCommit(node.path); }
              if (e.key === "Escape") onRenameCancel();
            }}
            onBlur={onRenameCancel}
            onClick={(e) => e.stopPropagation()}
          />
        ) : (
          <span className="tree-name">{node.name}</span>
        )}
      </div>
      {node.isDir && expanded && (
        <>
          {creatingIn?.dir === node.path && (
            <div className="tree-item" style={{ paddingLeft: `${8 + (depth + 1) * 14}px` }}>
              <span className="tree-icon">
                {creatingIn.kind === "dir"
                  ? <Folder size={15} color="#e8c14b" strokeWidth={1.5} />
                  : <File size={15} color="#858585" strokeWidth={1.5} />}
              </span>
              <input
                className="tree-rename-input"
                ref={(el) => { if (el) requestAnimationFrame(() => el.focus()); }}
                value={createName}
                placeholder={creatingIn.kind === "dir" ? t("fileTree.folderName") : t("fileTree.fileName")}
                onChange={(e) => onCreateNameChange(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") { e.preventDefault(); onCreateCommit(); }
                  if (e.key === "Escape") onCreateCancel();
                }}
                onBlur={onCreateCancel}
                onClick={(e) => e.stopPropagation()}
              />
            </div>
          )}
          {resolvedChildren.map((child) => (
            <TreeNode
              key={child.path} node={child} depth={depth + 1}
              activeFile={activeFile} expandedDirs={expandedDirs}
              lazyChildren={lazyChildren} loadingDirs={loadingDirs}
              renamingPath={renamingPath} renameValue={renameValue}
              creatingIn={creatingIn} createName={createName}
              onToggleDir={onToggleDir} onFileClick={onFileClick}
              onRenameValueChange={onRenameValueChange}
              onRenameCommit={onRenameCommit}
              onRenameCancel={onRenameCancel}
              onNodeContextMenu={onNodeContextMenu}
              onCreateNameChange={onCreateNameChange}
              onCreateCommit={onCreateCommit}
              onCreateCancel={onCreateCancel}
            />
          ))}
        </>
      )}
    </>
  );
}

/* ── File template helpers ──────────────────────────────────── */
function goPackageName(dirPath: string): string {
  const part = dirPath.split("/").filter(Boolean).pop() ?? "main";
  let name = part.toLowerCase().replace(/[-. ]/g, "_").replace(/[^a-z0-9_]/g, "");
  if (!name || /^[0-9]/.test(name)) name = "_" + name;
  return name || "main";
}

function toPascalCase(base: string): string {
  return base.split(/[-_\s]+/).map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join("");
}

function javaPackage(filePath: string): string {
  const marker = "src/main/java/";
  const idx = filePath.indexOf(marker);
  if (idx === -1) return "";
  const dir = filePath.slice(idx + marker.length, filePath.lastIndexOf("/"));
  return dir.replace(/\//g, ".");
}

function getFileTemplate(filePath: string): string {
  const ext = filePath.split(".").pop()?.toLowerCase() ?? "";
  const fileName = filePath.split("/").pop() ?? "";
  const dir = filePath.substring(0, filePath.lastIndexOf("/"));
  const base = fileName.replace(/\.[^.]+$/, "");

  switch (ext) {
    case "go": {
      const isMain = fileName === "main.go" || dir.split("/").pop()?.startsWith("cmd");
      const pkg = isMain ? "main" : goPackageName(dir);
      return pkg === "main"
        ? `package main\n\nfunc main() {\n}\n`
        : `package ${pkg}\n`;
    }
    case "tsx": {
      const comp = toPascalCase(base);
      return `import React from 'react';\n\ninterface ${comp}Props {}\n\nexport default function ${comp}({}: ${comp}Props) {\n  return (\n    <div></div>\n  );\n}\n`;
    }
    case "jsx": {
      const comp = toPascalCase(base);
      return `import React from 'react';\n\nexport default function ${comp}() {\n  return (\n    <div></div>\n  );\n}\n`;
    }
    case "java": {
      const pkg = javaPackage(filePath);
      const cls = toPascalCase(base);
      return pkg ? `package ${pkg};\n\npublic class ${cls} {\n}\n` : `public class ${cls} {\n}\n`;
    }
    case "kt": {
      const pkg = javaPackage(filePath);
      const cls = toPascalCase(base);
      return pkg ? `package ${pkg}\n\nclass ${cls} {\n}\n` : `class ${cls} {\n}\n`;
    }
    case "rs":
      return fileName === "main.rs" ? `fn main() {\n}\n` : "";
    case "vue": {
      return `<template>\n  <div></div>\n</template>\n\n<script setup lang="ts">\n</script>\n\n<style scoped>\n</style>\n`;
    }
    case "html":
      return `<!DOCTYPE html>\n<html lang="zh-CN">\n<head>\n  <meta charset="UTF-8" />\n  <meta name="viewport" content="width=device-width, initial-scale=1.0" />\n  <title>${base}</title>\n</head>\n<body>\n</body>\n</html>\n`;
    case "json":
      return `{}\n`;
    case "md":
      return `# ${base}\n`;
    case "py":
      return fileName === "main.py" ? `def main():\n    pass\n\n\nif __name__ == "__main__":\n    main()\n` : "";
    case "sh":
    case "bash":
      return `#!/usr/bin/env bash\nset -euo pipefail\n`;
    default:
      return "";
  }
}

interface ProjectCtxMenu { x: number; y: number; projectId: string }
interface FileCtxMenu { x: number; y: number; node: FileNode }
const PROJECT_TYPES: ProjectType[] = ["java", "frontend", "golang", "python", "rust", "other"];

export default function FileTree({
  projects, activeProjectId, activeFile, revealSignal,
  onFileClick, onProjectActivate, onProjectRemove, onProjectTypeChange,
  onProjectRename, onFileRenamed, onFileDeleted, onFileCreated,
  onConfigureRunEnv,
}: FileTreeProps) {
  const { t } = useTranslation();
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set());
  const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());
  const [ctxMenu, setCtxMenu] = useState<ProjectCtxMenu | null>(null);
  const [fileCtxMenu, setFileCtxMenu] = useState<FileCtxMenu | null>(null);
  const [renamingPath, setRenamingPath] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [creatingIn, setCreatingIn] = useState<{ dir: string; kind: "file" | "dir" } | null>(null);
  const [createName, setCreateName] = useState("");
  const [lazyChildren, setLazyChildren] = useState<Map<string, FileNode[]>>(new Map());
  const [loadingDirs, setLoadingDirs] = useState<Set<string>>(new Set());
  const [pendingDelete, setPendingDelete] = useState<FileNode | null>(null);
  const lazyRef = useRef<Set<string>>(new Set());
  const ctxRef = useRef<HTMLDivElement>(null);
  const fileCtxRef = useRef<HTMLDivElement>(null);
  const isCommittingRef = useRef(false);

  useEffect(() => {
    const close = (e: MouseEvent) => {
      if (ctxRef.current && !ctxRef.current.contains(e.target as Node)) setCtxMenu(null);
      if (fileCtxRef.current && !fileCtxRef.current.contains(e.target as Node)) setFileCtxMenu(null);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, []);

  // Reveal file: expand project + all ancestor dirs, then scroll
  useEffect(() => {
    if (!revealSignal) return;
    const { path } = revealSignal;
    const proj = projects.find((p) => path.startsWith(p.rootPath + "/"));
    if (!proj) return;

    setExpandedIds((prev) => new Set([...prev, proj.id]));
    onProjectActivate(proj.id);

    const rel = path.slice(proj.rootPath.length + 1);
    const segments = rel.split("/");
    const ancestors = new Set<string>();
    let cur = proj.rootPath;
    for (let i = 0; i < segments.length - 1; i++) {
      cur = cur + "/" + segments[i];
      ancestors.add(cur);
    }
    setExpandedDirs((prev) => new Set([...prev, ...ancestors]));

    setTimeout(() => {
      const items = document.querySelectorAll<HTMLElement>(".tree-item[data-filepath]");
      for (const el of items) {
        if (el.dataset.filepath === path) {
          el.scrollIntoView({ behavior: "smooth", block: "nearest" });
          break;
        }
      }
    }, 80);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealSignal]);

  const loadDirIfNeeded = useCallback(async (path: string) => {
    if (lazyRef.current.has(path)) return;
    lazyRef.current.add(path);
    setLoadingDirs((prev) => { const s = new Set(prev); s.add(path); return s; });
    try {
      const children = await ListDirectory(path);
      setLazyChildren((prev) => new Map(prev).set(path, children));
    } catch (e) {
      lazyRef.current.delete(path);
      console.error("lazy load:", e);
    } finally {
      setLoadingDirs((prev) => { const s = new Set(prev); s.delete(path); return s; });
    }
  }, []);

  const refreshDir = useCallback((dirPath: string) => {
    lazyRef.current.delete(dirPath);
    setLazyChildren((prev) => { const m = new Map(prev); m.delete(dirPath); return m; });
    loadDirIfNeeded(dirPath);
  }, [loadDirIfNeeded]);

  const toggleDir = useCallback((path: string) => {
    setExpandedDirs((prev) => {
      const next = new Set(prev);
      if (next.has(path)) { next.delete(path); return next; }
      next.add(path);
      return next;
    });
    loadDirIfNeeded(path);
  }, [loadDirIfNeeded]);

  const toggleExpand = (id: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    onProjectActivate(id);
  };

  const handleRootContextMenu = (e: React.MouseEvent, id: string) => {
    e.preventDefault();
    setFileCtxMenu(null);
    setCtxMenu({ x: e.clientX, y: e.clientY, projectId: id });
  };

  const handleNodeContextMenu = useCallback((e: React.MouseEvent, node: FileNode) => {
    setCtxMenu(null);
    setFileCtxMenu({ x: e.clientX, y: e.clientY, node });
  }, []);

  // ── Rename ──
  const startRename = useCallback((path: string, currentName: string) => {
    setCtxMenu(null);
    setFileCtxMenu(null);
    setRenamingPath(path);
    setRenameValue(currentName);
  }, []);

  const cancelRename = useCallback(() => {
    setRenamingPath(null);
    setRenameValue("");
  }, []);

  const commitRename = useCallback(async (oldPath: string) => {
    const newName = renameValue.trim();
    const lastName = oldPath.split("/").pop() ?? "";
    if (!newName || newName === lastName) { cancelRename(); return; }
    const dir = oldPath.substring(0, oldPath.lastIndexOf("/"));
    const newPath = dir + "/" + newName;
    try {
      await RenameFile(oldPath, newPath);
      const proj = projects.find((p) => p.rootPath === oldPath);
      if (proj) {
        onProjectRename(proj.id, newPath, newName);
      } else {
        onFileRenamed(oldPath, newPath);
        refreshDir(dir);
      }
    } catch (e) {
      alert(t("fileTree.renameFail") + ": " + e);
    }
    cancelRename();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [renameValue, projects, cancelRename, onProjectRename, onFileRenamed, refreshDir]);

  // ── Create ──
  const startCreate = useCallback((dir: string, kind: "file" | "dir") => {
    setCtxMenu(null);
    setFileCtxMenu(null);
    setCreatingIn({ dir, kind });
    setCreateName("");
    // Ensure the dir is expanded
    setExpandedDirs((prev) => new Set([...prev, dir]));
    const proj = projects.find((p) => p.rootPath === dir);
    if (proj) setExpandedIds((prev) => new Set([...prev, proj.id]));
    loadDirIfNeeded(dir);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projects, loadDirIfNeeded]);

  const cancelCreate = useCallback(() => {
    if (isCommittingRef.current) return;
    setCreatingIn(null);
    setCreateName("");
  }, []);  

  const commitCreate = useCallback(async () => {
    const name = createName.trim();
    if (!name || !creatingIn) { setCreatingIn(null); setCreateName(""); return; }
    const dir = creatingIn.dir;
    const kind = creatingIn.kind;
    const newPath = dir + "/" + name;
    isCommittingRef.current = true;
    try {
      if (kind === "dir") {
        await MkDir(newPath);
      } else {
        await CreateFile(newPath);
        const tpl = getFileTemplate(newPath);
        if (tpl) await WriteFile(newPath, tpl).catch(() => {});
        const ext = name.includes(".") ? name.split(".").pop()! : "";
        onFileClick({ path: newPath, name, isDir: false, children: undefined, ext });
      }
      onFileCreated(newPath);
      refreshDir(dir);
    } catch (e) {
      alert(t("fileTree.createFail") + ": " + e);
    } finally {
      isCommittingRef.current = false;
      setCreatingIn(null);
      setCreateName("");
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [createName, creatingIn, onFileCreated, refreshDir]);  

  // ── Delete ──
  const handleDeleteFile = useCallback(async (node: FileNode) => {
    setPendingDelete(node);
  }, []);

  const confirmDelete = useCallback(async () => {
    const node = pendingDelete;
    if (!node) return;
    setPendingDelete(null);
    setFileCtxMenu(null);
    const dir = node.path.substring(0, node.path.lastIndexOf("/"));
    try {
      await DeleteFile(node.path);
      onFileDeleted(node.path);
      refreshDir(dir);
    } catch (e) {
      alert(t("fileTree.deleteFail") + ": " + e);
    }
  }, [pendingDelete, onFileDeleted, refreshDir]);

  if (projects.length === 0) {
    return (
      <div className="file-tree" style={{ padding: "16px", color: "var(--text-muted)", fontSize: "12px" }}>
        {t("filetree.noProject")}<br />
        <span style={{ fontSize: "11px" }}>{t("filetree.hint")}</span>
      </div>
    );
  }

  return (
    <div className="file-tree" onClick={() => { setCtxMenu(null); setFileCtxMenu(null); }}>
      {projects.map((proj) => {
        const isExpanded = expandedIds.has(proj.id);
        const isActive = proj.id === activeProjectId;
        const typeColor = proj.type ? PROJECT_TYPE_COLORS[proj.type] : "var(--text-muted)";
        const isRenamingThis = renamingPath === proj.rootPath;

        return (
          <div key={proj.id}>
            <div
              className={`tree-project-root ${isActive ? "active" : ""}`}
              onClick={isRenamingThis ? undefined : () => toggleExpand(proj.id)}
              onContextMenu={(e) => handleRootContextMenu(e, proj.id)}
              title={isRenamingThis ? undefined : proj.rootPath}
            >
              <span className="tree-chevron">
                {isExpanded
                  ? <ChevronDown size={12} color="var(--text-muted)" />
                  : <ChevronRight size={12} color="var(--text-muted)" />}
              </span>
              <span className="tree-icon">
                {isExpanded
                  ? <FolderOpen size={15} color="#e8c14b" strokeWidth={1.5} />
                  : <Folder size={15} color="#e8c14b" strokeWidth={1.5} />}
              </span>
              {isRenamingThis ? (
                <input
                  className="tree-rename-input"
                  value={renameValue}
                  autoFocus
                  onFocus={(e) => e.target.select()}
                  onChange={(e) => setRenameValue(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") { e.preventDefault(); commitRename(proj.rootPath); }
                    if (e.key === "Escape") cancelRename();
                  }}
                  onBlur={cancelRename}
                  onClick={(e) => e.stopPropagation()}
                />
              ) : (
                <>
                  <span className="tree-project-name">{proj.name.toUpperCase()}</span>
                  {proj.type && (
                    <span className="tree-project-badge" style={{ color: typeColor }}>
                      {proj.type}
                    </span>
                  )}
                </>
              )}
            </div>

            {isExpanded && (
              <>
                {creatingIn?.dir === proj.rootPath && (
                  <div className="tree-item" style={{ paddingLeft: "22px" }}>
                    <span className="tree-icon">
                      {creatingIn.kind === "dir"
                        ? <Folder size={15} color="#e8c14b" strokeWidth={1.5} />
                        : <File size={15} color="#858585" strokeWidth={1.5} />}
                    </span>
                    <input
                      className="tree-rename-input"
                      ref={(el) => { if (el) requestAnimationFrame(() => el.focus()); }}
                      value={createName}
                      placeholder={creatingIn.kind === "dir" ? t("fileTree.folderName") : t("fileTree.fileName")}
                      onChange={(e) => setCreateName(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") { e.preventDefault(); commitCreate(); }
                        if (e.key === "Escape") cancelCreate();
                      }}
                      onBlur={cancelCreate}
                      onClick={(e) => e.stopPropagation()}
                    />
                  </div>
                )}
                {proj.tree && (lazyChildren.get(proj.rootPath) ?? proj.tree.children ?? []).map((node) => (
                  <TreeNode
                    key={node.path}
                    node={node}
                    depth={0}
                    activeFile={activeFile}
                    expandedDirs={expandedDirs}
                    lazyChildren={lazyChildren}
                    loadingDirs={loadingDirs}
                    renamingPath={renamingPath}
                    renameValue={renameValue}
                    creatingIn={creatingIn}
                    createName={createName}
                    onToggleDir={toggleDir}
                    onFileClick={onFileClick}
                    onRenameValueChange={setRenameValue}
                    onRenameCommit={commitRename}
                    onRenameCancel={cancelRename}
                    onNodeContextMenu={handleNodeContextMenu}
                    onCreateNameChange={setCreateName}
                    onCreateCommit={commitCreate}
                    onCreateCancel={cancelCreate}
                  />
                ))}
              </>
            )}

            {isExpanded && !proj.tree && (
              <div style={{ padding: "6px 24px", color: "var(--text-muted)", fontSize: "11px" }}>
                {t("fileTree.loading")}
              </div>
            )}
          </div>
        );
      })}

      {/* Project context menu */}
      {ctxMenu && (
        <div
          ref={ctxRef}
          className="context-menu"
          style={{ top: ctxMenu.y, left: ctxMenu.x }}
          onClick={(e) => e.stopPropagation()}
        >
          <div className="context-menu-label">{t("project.setType")}</div>
          {PROJECT_TYPES.map((pt) => (
            <div
              key={pt}
              className="context-menu-item"
              onClick={() => { onProjectTypeChange(ctxMenu.projectId, pt); setCtxMenu(null); }}
            >
              <span style={{ color: PROJECT_TYPE_COLORS[pt], marginRight: 6, fontSize: 10, fontWeight: 700 }}>●</span>
              {t(`project.type.${pt}`)}
            </div>
          ))}
          <div className="context-menu-divider" />
          <div
            className="context-menu-item"
            onClick={() => {
              const proj = projects.find((p) => p.id === ctxMenu.projectId);
              if (proj) startCreate(proj.rootPath, "file");
            }}
          >
            <FilePlus size={11} style={{ marginRight: 6, opacity: 0.7 }} />
            {t("fileTree.newFile")}
          </div>
          <div
            className="context-menu-item"
            onClick={() => {
              const proj = projects.find((p) => p.id === ctxMenu.projectId);
              if (proj) startCreate(proj.rootPath, "dir");
            }}
          >
            <FolderPlus size={11} style={{ marginRight: 6, opacity: 0.7 }} />
            {t("fileTree.newFolder")}
          </div>
          <div className="context-menu-divider" />
          <div
            className="context-menu-item"
            onClick={() => {
              const proj = projects.find((p) => p.id === ctxMenu.projectId);
              if (proj) startRename(proj.rootPath, proj.name);
            }}
          >
            <Pencil size={11} style={{ marginRight: 6, opacity: 0.7 }} />
            {t("fileTree.rename")}
          </div>
          <div className="context-menu-divider" />
          {onConfigureRunEnv && (
            <div
              className="context-menu-item"
              onClick={() => {
                const proj = projects.find((p) => p.id === ctxMenu.projectId);
                if (proj) { onConfigureRunEnv(proj.rootPath, proj.name); setCtxMenu(null); }
              }}
            >
              <Wrench size={11} style={{ marginRight: 6, opacity: 0.7 }} />
              {t("fileTree.configRunEnv")}
            </div>
          )}
          <div className="context-menu-divider" />
          <div
            className="context-menu-item context-menu-item-danger"
            onClick={() => { onProjectRemove(ctxMenu.projectId); setCtxMenu(null); }}
          >
            {t("project.remove")}
          </div>
        </div>
      )}

      {/* File / directory context menu */}
      {fileCtxMenu && (
        <div
          ref={fileCtxRef}
          className="context-menu"
          style={{ top: fileCtxMenu.y, left: fileCtxMenu.x }}
          onClick={(e) => e.stopPropagation()}
        >
          {!fileCtxMenu.node.isDir && (
            <div
              className="context-menu-item"
              onClick={() => { onFileClick(fileCtxMenu.node); setFileCtxMenu(null); }}
            >
              {t("fileTree.openFile")}
            </div>
          )}
          {fileCtxMenu.node.isDir && (
            <>
              <div
                className="context-menu-item"
                onClick={() => startCreate(fileCtxMenu.node.path, "file")}
              >
                <FilePlus size={11} style={{ marginRight: 6, opacity: 0.7 }} />
                {t("fileTree.newFile")}
              </div>
              <div
                className="context-menu-item"
                onClick={() => startCreate(fileCtxMenu.node.path, "dir")}
              >
                <FolderPlus size={11} style={{ marginRight: 6, opacity: 0.7 }} />
                {t("fileTree.newFolder")}
              </div>
              {onConfigureRunEnv && (
                <div
                  className="context-menu-item"
                  onClick={() => {
                    onConfigureRunEnv(fileCtxMenu.node.path, fileCtxMenu.node.name);
                    setFileCtxMenu(null);
                  }}
                >
                  <Wrench size={11} style={{ marginRight: 6, opacity: 0.7 }} />
                  {t("fileTree.configRunEnv")}
                </div>
              )}
              <div className="context-menu-divider" />
            </>
          )}
          <div
            className="context-menu-item"
            onClick={() => startRename(fileCtxMenu.node.path, fileCtxMenu.node.name)}
          >
            <Pencil size={11} style={{ marginRight: 6, opacity: 0.7 }} />
            {t("fileTree.rename")}
          </div>
          <div className="context-menu-divider" />
          {pendingDelete?.path === fileCtxMenu.node.path ? (
            <div className="context-menu-delete-confirm">
              <span>{t("fileTree.confirmDelete", { name: pendingDelete.name })}</span>
              <div className="context-menu-delete-actions">
                <button className="ctx-del-yes" onClick={confirmDelete}>{t("fileTree.delete")}</button>
                <button className="ctx-del-no" onClick={() => setPendingDelete(null)}>{t("fileTree.cancel")}</button>
              </div>
            </div>
          ) : (
            <div
              className="context-menu-item context-menu-item-danger"
              onClick={() => handleDeleteFile(fileCtxMenu.node)}
            >
              <Trash2 size={11} style={{ marginRight: 6, opacity: 0.7 }} />
              {t("fileTree.delete")}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
