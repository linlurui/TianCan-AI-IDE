/**
 * editorStore — 管理所有编辑器标签页和分屏布局状态
 * 使用 Zustand 替代 App.tsx 里的 useState 散列状态
 */
import { create } from "zustand";

export interface Tab {
  path: string;
  name: string;
  content: string;
  isDirty: boolean;
}

export type SplitLayout = "single" | "horizontal" | "vertical";

export interface SplitPane {
  id: "left" | "right";
  tabs: Tab[];
  activeTab: string | null;
}

interface EditorState {
  // 单窗格模式
  tabs: Tab[];
  activeTab: string | null;

  // 分屏模式
  splitLayout: SplitLayout;
  panes: { left: SplitPane; right: SplitPane };
  activePaneId: "left" | "right";

  // 光标信息
  cursorInfo: { line: number; col: number };

  // 分析内容（与活动文件同步）
  analysisContent: string | null;

  // Actions
  openTab: (tab: Tab, paneId?: "left" | "right") => void;
  closeTab: (path: string, paneId?: "left" | "right") => void;
  setActiveTab: (path: string | null, paneId?: "left" | "right") => void;
  updateTabContent: (path: string, content: string) => void;
  markTabSaved: (path: string) => void;
  renameTab: (oldPath: string, newPath: string, newName: string) => void;
  deleteTabsByPrefix: (prefix: string) => void;
  setCursorInfo: (line: number, col: number) => void;
  setAnalysisContent: (content: string | null) => void;

  // 分屏
  setSplitLayout: (layout: SplitLayout) => void;
  setActivePaneId: (id: "left" | "right") => void;
  splitCurrentTab: (direction: "horizontal" | "vertical") => void;
  closeSplit: () => void;

  // 从 path 找对应 pane（分屏时用）
  getPaneForPath: (path: string) => "left" | "right";
}

export const useEditorStore = create<EditorState>((set, get) => ({
  tabs: [],
  activeTab: null,
  splitLayout: "single",
  panes: {
    left: { id: "left", tabs: [], activeTab: null },
    right: { id: "right", tabs: [], activeTab: null },
  },
  activePaneId: "left",
  cursorInfo: { line: 1, col: 1 },
  analysisContent: null,

  openTab: (tab, paneId) => {
    const state = get();
    if (state.splitLayout === "single") {
      const exists = state.tabs.find((t) => t.path === tab.path);
      if (!exists) {
        set({ tabs: [...state.tabs, tab] });
      }
      set({ activeTab: tab.path, analysisContent: tab.content });
    } else {
      const id = paneId ?? state.activePaneId;
      set((s) => {
        const pane = s.panes[id];
        const exists = pane.tabs.find((t) => t.path === tab.path);
        const newTabs = exists ? pane.tabs : [...pane.tabs, tab];
        return {
          panes: {
            ...s.panes,
            [id]: { ...pane, tabs: newTabs, activeTab: tab.path },
          },
          activePaneId: id,
        };
      });
    }
  },

  closeTab: (path, paneId) => {
    const state = get();
    if (state.splitLayout === "single") {
      const next = state.tabs.filter((t) => t.path !== path);
      const active = state.activeTab === path ? (next.at(-1)?.path ?? null) : state.activeTab;
      set({ tabs: next, activeTab: active });
    } else {
      const id = paneId ?? state.activePaneId;
      set((s) => {
        const pane = s.panes[id];
        const next = pane.tabs.filter((t) => t.path !== path);
        const active = pane.activeTab === path ? (next.at(-1)?.path ?? null) : pane.activeTab;
        return {
          panes: { ...s.panes, [id]: { ...pane, tabs: next, activeTab: active } },
        };
      });
    }
  },

  setActiveTab: (path, paneId) => {
    const state = get();
    if (state.splitLayout === "single") {
      set({ activeTab: path });
    } else {
      const id = paneId ?? state.activePaneId;
      set((s) => ({
        panes: { ...s.panes, [id]: { ...s.panes[id], activeTab: path } },
        activePaneId: id,
      }));
    }
  },

  updateTabContent: (path, content) => {
    set((s) => ({
      tabs: s.tabs.map((t) =>
        t.path === path ? { ...t, content, isDirty: true } : t
      ),
      panes: {
        left: {
          ...s.panes.left,
          tabs: s.panes.left.tabs.map((t) =>
            t.path === path ? { ...t, content, isDirty: true } : t
          ),
        },
        right: {
          ...s.panes.right,
          tabs: s.panes.right.tabs.map((t) =>
            t.path === path ? { ...t, content, isDirty: true } : t
          ),
        },
      },
    }));
  },

  markTabSaved: (path) => {
    set((s) => ({
      tabs: s.tabs.map((t) =>
        t.path === path ? { ...t, isDirty: false } : t
      ),
      panes: {
        left: {
          ...s.panes.left,
          tabs: s.panes.left.tabs.map((t) =>
            t.path === path ? { ...t, isDirty: false } : t
          ),
        },
        right: {
          ...s.panes.right,
          tabs: s.panes.right.tabs.map((t) =>
            t.path === path ? { ...t, isDirty: false } : t
          ),
        },
      },
    }));
  },

  renameTab: (oldPath, newPath, newName) => {
    set((s) => ({
      tabs: s.tabs.map((t) =>
        t.path === oldPath ? { ...t, path: newPath, name: newName } : t
      ),
      activeTab: s.activeTab === oldPath ? newPath : s.activeTab,
    }));
  },

  deleteTabsByPrefix: (prefix) => {
    set((s) => {
      const filterTabs = (tabs: Tab[]) =>
        tabs.filter((t) => !t.path.startsWith(prefix));
      const newTabs = filterTabs(s.tabs);
      return {
        tabs: newTabs,
        activeTab: s.activeTab?.startsWith(prefix)
          ? (newTabs.at(-1)?.path ?? null)
          : s.activeTab,
      };
    });
  },

  setCursorInfo: (line, col) => set({ cursorInfo: { line, col } }),
  setAnalysisContent: (content) => set({ analysisContent: content }),

  setSplitLayout: (layout) => set({ splitLayout: layout }),
  setActivePaneId: (id) => set({ activePaneId: id }),

  splitCurrentTab: (direction) => {
    const state = get();
    if (state.splitLayout !== "single") return;
    const layout = direction === "horizontal" ? "horizontal" : "vertical";
    const currentTab = state.tabs.find((t) => t.path === state.activeTab);
    set({
      splitLayout: layout,
      panes: {
        left: { id: "left", tabs: state.tabs, activeTab: state.activeTab },
        right: {
          id: "right",
          tabs: currentTab ? [currentTab] : [],
          activeTab: currentTab?.path ?? null,
        },
      },
      activePaneId: "right",
    });
  },

  closeSplit: () => {
    const state = get();
    const allTabs = [
      ...state.panes.left.tabs,
      ...state.panes.right.tabs.filter(
        (rt) => !state.panes.left.tabs.some((lt) => lt.path === rt.path)
      ),
    ];
    set({
      splitLayout: "single",
      tabs: allTabs,
      activeTab: state.panes[state.activePaneId].activeTab,
      panes: {
        left: { id: "left", tabs: [], activeTab: null },
        right: { id: "right", tabs: [], activeTab: null },
      },
    });
  },

  getPaneForPath: (path) => {
    const state = get();
    if (state.panes.right.tabs.some((t) => t.path === path)) return "right";
    return "left";
  },
}));
