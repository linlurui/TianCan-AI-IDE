/**
 * projectStore — 管理多项目状态
 */
import { create } from "zustand";
import { Project, ProjectType } from "../types";
import { GetDirectoryTree } from "../bindings/filesystem";
import { GetStatus } from "../bindings/git";

const STORAGE_KEY = "tiancan-projects";

function loadSavedPaths(): string[] {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "[]");
  } catch {
    return [];
  }
}

function savePaths(projects: Project[]) {
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify(projects.map((p) => p.rootPath))
  );
}

interface ProjectState {
  projects: Project[];
  activeProjectId: string | null;

  // computed
  activeProject: Project | null;

  // actions
  loadPersistedProjects: () => Promise<void>;
  importProject: (rootPath: string) => Promise<void>;
  removeProject: (id: string) => void;
  activateProject: (id: string) => void;
  renameProject: (id: string, newPath: string, newName: string) => void;
  setProjectType: (id: string, type: ProjectType) => void;
  refreshProject: (id: string) => Promise<void>;
}

export const useProjectStore = create<ProjectState>((set, get) => ({
  projects: [],
  activeProjectId: null,
  activeProject: null,

  loadPersistedProjects: async () => {
    const paths = loadSavedPaths();
    if (paths.length === 0) return;
    const initial: Project[] = paths.map((p) => ({
      id: p,
      rootPath: p,
      name: p.split("/").pop() ?? p,
      tree: null,
      gitStatus: null,
    }));
    const results = await Promise.all(
      initial.map(async (proj) => {
        const tree = await GetDirectoryTree(proj.rootPath).catch(() => null);
        if (!tree) return null; // path no longer exists, drop it
        const gitStatus = await GetStatus(proj.rootPath).catch(() => null);
        return { ...proj, tree, gitStatus };
      })
    );
    const loaded = results.filter((p): p is NonNullable<typeof p> => p !== null);
    savePaths(loaded);
    set({
      projects: loaded,
      activeProjectId: loaded[0]?.id ?? null,
      activeProject: loaded[0] ?? null,
    });
  },

  importProject: async (rootPath) => {
    rootPath = rootPath.trim();
    const { projects } = get();
    if (projects.some((p) => p.rootPath === rootPath)) {
      throw new Error("该项目已导入");
    }
    const id = rootPath;
    const name = rootPath.split("/").pop() ?? rootPath;
    const [tree, gitStatus] = await Promise.all([
      GetDirectoryTree(rootPath),
      GetStatus(rootPath).catch(() => null),
    ]);
    const proj: Project = { id, rootPath, name, tree, gitStatus };
    set((s) => {
      const next = [...s.projects, proj];
      savePaths(next);
      return { projects: next, activeProjectId: id, activeProject: proj };
    });
  },

  removeProject: (id) => {
    set((s) => {
      const next = s.projects.filter((p) => p.id !== id);
      savePaths(next);
      const newActive = s.activeProjectId === id
        ? (next[0] ?? null)
        : s.projects.find((p) => p.id === s.activeProjectId) ?? null;
      return {
        projects: next,
        activeProjectId: newActive?.id ?? null,
        activeProject: newActive,
      };
    });
  },

  activateProject: (id) => {
    set((s) => ({
      activeProjectId: id,
      activeProject: s.projects.find((p) => p.id === id) ?? null,
    }));
  },

  renameProject: (id, newPath, newName) => {
    set((s) => {
      const next = s.projects.map((p) =>
        p.id === id ? { ...p, id: newPath, rootPath: newPath, name: newName } : p
      );
      savePaths(next);
      return {
        projects: next,
        activeProjectId: s.activeProjectId === id ? newPath : s.activeProjectId,
        activeProject: next.find((p) => p.id === (s.activeProjectId === id ? newPath : s.activeProjectId)) ?? null,
      };
    });
  },

  setProjectType: (id, type) => {
    set((s) => {
      const next = s.projects.map((p) => p.id === id ? { ...p, type } : p);
      return {
        projects: next,
        activeProject: next.find((p) => p.id === s.activeProjectId) ?? null,
      };
    });
  },

  refreshProject: async (id) => {
    const { projects } = get();
    const proj = projects.find((p) => p.id === id);
    if (!proj) return;
    const [tree, gitStatus] = await Promise.all([
      GetDirectoryTree(proj.rootPath).catch(() => null),
      GetStatus(proj.rootPath).catch(() => null),
    ]);
    set((s) => {
      const next = s.projects.map((p) =>
        p.id === id ? { ...p, tree, gitStatus } : p
      );
      return {
        projects: next,
        activeProject: next.find((p) => p.id === s.activeProjectId) ?? null,
      };
    });
  },
}));
