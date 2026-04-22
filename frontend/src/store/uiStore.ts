/**
 * uiStore — 管理所有 UI 面板/布局状态
 */
import { create } from "zustand";

export type SidebarTab =
  | "projects"
  | "search"
  | "git"
  | "database"
  | "testing"
  | "deploy"
  | "extensions"
  | "outline"
  | "callhierarchy"
  | "remote";

export type ToolTab = "terminal" | "analysis" | "debug" | "modify" | string;

interface TerminalInstance {
  id: string;
  name: string;
  initCmd?: string; // command to run when terminal first opens
}

export type LspInstallStatus = "checking" | "missing" | "installing" | "ready" | "unsupported";

interface UIState {
  // Sidebar
  sidebarOpen: boolean;
  sidebarTab: SidebarTab;
  sidebarWidth: number;

  // Tool panel
  toolPanelOpen: boolean;
  activeToolTab: ToolTab;
  toolPanelWidth: number;

  // App mode
  appMode: "editor" | "ai";

  // Terminals
  terminals: TerminalInstance[];
  activeTerminalId: string;

  // Ports
  terminalPort: number;
  lspPort: number;

  // Modals & overlays
  showWizard: boolean;
  showProgress: boolean;

  // LSP install state
  lspStatus: Record<string, LspInstallStatus>;
  lspInstallLog: string[];

  // Actions
  setSidebarOpen: (open: boolean) => void;
  setSidebarTab: (tab: SidebarTab) => void;
  setSidebarWidth: (w: number) => void;
  setToolPanelOpen: (open: boolean) => void;
  setActiveToolTab: (tab: ToolTab) => void;
  setToolPanelWidth: (w: number) => void;
  setAppMode: (mode: "editor" | "ai") => void;
  handleActivityClick: (tab: SidebarTab) => void;

  addTerminal: (initCmd?: string) => void;
  removeTerminal: (id: string) => void;
  setActiveTerminalId: (id: string) => void;

  setTerminalPort: (port: number) => void;
  setLspPort: (port: number) => void;

  setShowWizard: (v: boolean) => void;
  setShowProgress: (v: boolean) => void;

  setLspStatus: (lang: string, status: LspInstallStatus) => void;
  appendInstallLog: (line: string) => void;
  clearInstallLog: () => void;
}

export const useUIStore = create<UIState>((set, get) => ({
  sidebarOpen: true,
  sidebarTab: "projects",
  sidebarWidth: 260,

  toolPanelOpen: false,
  activeToolTab: "terminal",
  toolPanelWidth: 420,

  appMode: "editor",

  terminals: [{ id: "1", name: "Terminal 1" }],
  activeTerminalId: "1",

  terminalPort: 0,
  lspPort: 0,

  showWizard: false,
  showProgress: false,

  lspStatus: {},
  lspInstallLog: [],

  setSidebarOpen: (open) => set({ sidebarOpen: open }),
  setSidebarTab: (tab) => set({ sidebarTab: tab }),
  setSidebarWidth: (w) => set({ sidebarWidth: w }),
  setToolPanelOpen: (open) => set({ toolPanelOpen: open }),
  setActiveToolTab: (tab) => set({ activeToolTab: tab }),
  setToolPanelWidth: (w) => set({ toolPanelWidth: w }),
  setAppMode: (mode) => set({ appMode: mode }),

  handleActivityClick: (tab) => {
    const { sidebarTab, sidebarOpen } = get();
    if (sidebarTab === tab && sidebarOpen) {
      set({ sidebarOpen: false });
    } else {
      set({ sidebarTab: tab, sidebarOpen: true });
    }
  },

  addTerminal: (initCmd?: string) => {
    set((s) => {
      const newId = Date.now().toString();
      const name = `Terminal ${s.terminals.length + 1}`;
      return {
        terminals: [...s.terminals, { id: newId, name, initCmd }],
        activeTerminalId: newId,
      };
    });
  },

  removeTerminal: (id) => {
    set((s) => {
      if (s.terminals.length <= 1) return s;
      const next = s.terminals.filter((t) => t.id !== id);
      return {
        terminals: next,
        activeTerminalId: s.activeTerminalId === id ? next[0].id : s.activeTerminalId,
      };
    });
  },

  setActiveTerminalId: (id) => set({ activeTerminalId: id }),
  setTerminalPort: (port) => set({ terminalPort: port }),
  setLspPort: (port) => set({ lspPort: port }),
  setShowWizard: (v) => set({ showWizard: v }),
  setShowProgress: (v) => set({ showProgress: v }),

  setLspStatus: (lang, status) => set((s) => ({ lspStatus: { ...s.lspStatus, [lang]: status } })),
  appendInstallLog: (line) => set((s) => ({ lspInstallLog: [...s.lspInstallLog, line] })),
  clearInstallLog: () => set({ lspInstallLog: [] }),
}));
