/**
 * projectStore — 管理多项目状态（包括会话隔离）
 */
import { create } from "zustand";
import { Project, ProjectType } from "../types";
import { GetDirectoryTree } from "@bindings/filesystem";
import { GetStatus } from "@bindings/git";

const STORAGE_KEY = "tiancan-projects";

// ── Types for session isolation ──────────────────────────────────────────────

export interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "system" | "tool_call" | "tool_result" | "thinking";
  content: string;
  name?: string;
  timestamp: number;
  model?: string;
  error?: string;
}

export interface AgentTaskItem {
  id: string;
  status: "pending" | "running" | "completed" | "failed" | "cancelled";
  description: string;
  filePath?: string;
  createdAt: number;
  completedAt?: number;
  error?: string;
}

interface ProjectSession {
  messages: ChatMessage[];
  agentTasks: AgentTaskItem[];
  lastActive: number;
}

// ── Storage helpers ───────────────────────────────────────────────────────────

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

function loadSessions(): Record<string, ProjectSession> {
  try {
    const raw = localStorage.getItem("tiancan-sessions");
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {};
  }
}

function saveSessions(sessions: Record<string, ProjectSession>) {
  localStorage.setItem("tiancan-sessions", JSON.stringify(sessions));
}

interface ProjectState {
  projects: Project[];
  activeProjectId: string | null;

  // computed
  activeProject: Project | null;

  // Session isolation (per-project messages & tasks)
  activeMessages: ChatMessage[];
  activeAgentTasks: AgentTaskItem[];

  // actions
  loadPersistedProjects: () => Promise<void>;
  importProject: (rootPath: string) => Promise<void>;
  removeProject: (id: string) => void;
  activateProject: (id: string) => void;
  renameProject: (id: string, newPath: string, newName: string) => void;
  setProjectType: (id: string, type: ProjectType) => void;
  refreshProject: (id: string) => Promise<void>;

  // Session actions
  addMessage: (msg: Omit<ChatMessage, "id" | "timestamp">) => void;
  replaceMessages: (messages: Array<Omit<ChatMessage, "id" | "timestamp"> | ChatMessage>) => void;
  clearMessages: () => void;
  addAgentTask: (task: Omit<AgentTaskItem, "id" | "createdAt">) => string;
  updateAgentTask: (id: string, updates: Partial<AgentTaskItem>) => void;
  removeAgentTask: (id: string) => void;
  clearAgentTasks: () => void;
}

export const useProjectStore = create<ProjectState>((set, get) => {
  // Load persisted sessions
  const sessions = loadSessions();

  return {
    projects: [],
    activeProjectId: null,
    activeProject: null,
    activeMessages: [],
    activeAgentTasks: [],

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
          if (!tree) return null;
          const gitStatus = await GetStatus(proj.rootPath).catch(() => null);
          return { ...proj, tree, gitStatus };
        })
      );
      const loaded = results.filter((p): p is NonNullable<typeof p> => p !== null);
      savePaths(loaded);

      // Load sessions for loaded projects
      const refreshedSessions = loadSessions();
      const firstId = loaded[0]?.id ?? null;
      const firstSession = firstId ? (refreshedSessions[firstId] ?? { messages: [], agentTasks: [], lastActive: Date.now() }) : { messages: [], agentTasks: [], lastActive: Date.now() };

      set({
        projects: loaded,
        activeProjectId: firstId,
        activeProject: loaded[0] ?? null,
        activeMessages: firstSession.messages,
        activeAgentTasks: firstSession.agentTasks,
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

      // Initialize empty session for new project
      const currentSessions = loadSessions();
      currentSessions[id] = { messages: [], agentTasks: [], lastActive: Date.now() };
      saveSessions(currentSessions);

      set((s) => {
        const next = [...s.projects, proj];
        savePaths(next);
        return {
          projects: next,
          activeProjectId: id,
          activeProject: proj,
          activeMessages: [],
          activeAgentTasks: [],
        };
      });
    },

    removeProject: (id) => {
      // Remove session data
      const currentSessions = loadSessions();
      delete currentSessions[id];
      saveSessions(currentSessions);

      set((s) => {
        const next = s.projects.filter((p) => p.id !== id);
        savePaths(next);

        // If removing active project, switch to first remaining or null
        if (s.activeProjectId === id) {
          const newSessions = loadSessions();
          const nextId = next[0]?.id ?? null;
          const nextSession = nextId ? (newSessions[nextId] ?? { messages: [], agentTasks: [], lastActive: Date.now() }) : { messages: [], agentTasks: [], lastActive: Date.now() };
          return {
            projects: next,
            activeProjectId: nextId,
            activeProject: next[0] ?? null,
            activeMessages: nextSession.messages,
            activeAgentTasks: nextSession.agentTasks,
          };
        }
        return { projects: next };
      });
    },

    activateProject: (id) => {
      const { projects, activeProjectId } = get();
      if (id === activeProjectId) return;

      // Save current project's session
      const currentSessions = loadSessions();
      if (activeProjectId) {
        currentSessions[activeProjectId] = {
          messages: get().activeMessages,
          agentTasks: get().activeAgentTasks,
          lastActive: Date.now(),
        };
        saveSessions(currentSessions);
      }

      // Load new project's session
      const newSession = currentSessions[id] ?? { messages: [], agentTasks: [], lastActive: Date.now() };
      const newProject = projects.find((p) => p.id === id) ?? null;

      set({
        activeProjectId: id,
        activeProject: newProject,
        activeMessages: newSession.messages,
        activeAgentTasks: newSession.agentTasks,
      });
    },

    renameProject: (id, newPath, newName) => {
      set((s) => {
        const next = s.projects.map((p) =>
          p.id === id ? { ...p, id: newPath, rootPath: newPath, name: newName } : p
        );
        savePaths(next);

        // Update session key if project path changed
        const currentSessions = loadSessions();
        if (currentSessions[id]) {
          currentSessions[newPath] = currentSessions[id];
          delete currentSessions[id];
          saveSessions(currentSessions);
        }

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

    // ── Session actions ──────────────────────────────────────────────────────

    addMessage: (msg) => {
      const { activeProjectId, activeMessages } = get();
      if (!activeProjectId) return;

      const newMsg: ChatMessage = {
        ...msg,
        id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
        timestamp: Date.now(),
      };

      // Update in-memory state
      set({ activeMessages: [...activeMessages, newMsg] });

      // Persist to storage
      const sessions = loadSessions();
      sessions[activeProjectId] = {
        messages: [...activeMessages, newMsg],
        agentTasks: get().activeAgentTasks,
        lastActive: Date.now(),
      };
      saveSessions(sessions);
    },

    replaceMessages: (messages) => {
      const { activeProjectId } = get();
      if (!activeProjectId) return;

      const now = Date.now();
      const nextMessages: ChatMessage[] = messages.map((msg, index) => ({
        ...msg,
        id: "id" in msg && msg.id ? msg.id : `${now}-${index}-${Math.random().toString(36).slice(2)}`,
        timestamp: "timestamp" in msg && msg.timestamp ? msg.timestamp : now,
      }));

      set({ activeMessages: nextMessages });

      const sessions = loadSessions();
      sessions[activeProjectId] = {
        messages: nextMessages,
        agentTasks: get().activeAgentTasks,
        lastActive: Date.now(),
      };
      saveSessions(sessions);
    },

    clearMessages: () => {
      const { activeProjectId } = get();
      if (!activeProjectId) return;

      set({ activeMessages: [] });

      const sessions = loadSessions();
      if (sessions[activeProjectId]) {
        sessions[activeProjectId].messages = [];
        sessions[activeProjectId].lastActive = Date.now();
        saveSessions(sessions);
      }
    },

    addAgentTask: (task) => {
      const { activeProjectId, activeAgentTasks } = get();
      if (!activeProjectId) return "";

      const id = `task-${Date.now()}-${Math.random().toString(36).slice(2)}`;
      const newTask: AgentTaskItem = {
        ...task,
        id,
        createdAt: Date.now(),
      };

      set({ activeAgentTasks: [...activeAgentTasks, newTask] });

      const sessions = loadSessions();
      sessions[activeProjectId] = {
        messages: get().activeMessages,
        agentTasks: [...activeAgentTasks, newTask],
        lastActive: Date.now(),
      };
      saveSessions(sessions);

      return id;
    },

    updateAgentTask: (id, updates) => {
      const { activeProjectId, activeAgentTasks } = get();
      if (!activeProjectId) return;

      const nextTasks = activeAgentTasks.map((t) =>
        t.id === id ? { ...t, ...updates } : t
      );
      set({ activeAgentTasks: nextTasks });

      const sessions = loadSessions();
      sessions[activeProjectId].agentTasks = nextTasks;
      sessions[activeProjectId].lastActive = Date.now();
      saveSessions(sessions);
    },

    removeAgentTask: (id) => {
      const { activeProjectId, activeAgentTasks } = get();
      if (!activeProjectId) return;

      const nextTasks = activeAgentTasks.filter((t) => t.id !== id);
      set({ activeAgentTasks: nextTasks });

      const sessions = loadSessions();
      sessions[activeProjectId].agentTasks = nextTasks;
      sessions[activeProjectId].lastActive = Date.now();
      saveSessions(sessions);
    },

    clearAgentTasks: () => {
      const { activeProjectId } = get();
      if (!activeProjectId) return;

      set({ activeAgentTasks: [] });

      const sessions = loadSessions();
      if (sessions[activeProjectId]) {
        sessions[activeProjectId].agentTasks = [];
        sessions[activeProjectId].lastActive = Date.now();
        saveSessions(sessions);
      }
    },
  };
});
