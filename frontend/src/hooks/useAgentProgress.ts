/**
 * useAgentProgress - 共享 SubAgentProgress 和 AIChangesPanel 的状态
 * 实现点击联动：点击子Agent进度 → 高亮右侧任务列表
 */
import { create } from "zustand";
import { AgentTask } from "../bindings/task";

interface FileModification {
  taskId: string;
  taskName: string;
  taskIndex: number;
  filePath?: string;
}

interface AgentProgressState {
  // 当前选中的任务
  selectedTaskId: string | null;
  selectedTask: AgentTask | null;

  // 当前选中的文件
  selectedFilePath: string | null;
  selectedFileOriginalContent: string | null;
  selectedFileCurrentContent: string | null;

  // AI Changes 导航状态
  fileModifications: FileModification[];
  currentModificationIndex: number;

  // Diff 查看器状态
  diffViewerOpen: boolean;

  // SubAgent 相关
  currentSubAgentTask: string | null; // 当前子Agent正在执行的任务描述
  subAgentProgressMap: Map<string, { progress: number; status: string }>;

  // Actions
  setSelectedTask: (taskId: string | null, task?: AgentTask) => void;
  setSelectedFile: (filePath: string | null, originalContent?: string, currentContent?: string) => void;
  updateFileModifications: (tasks: AgentTask[]) => void;
  navigateToPrevModification: () => boolean;
  navigateToNextModification: () => boolean;
  openDiffViewer: () => void;
  closeDiffViewer: () => void;
  setCurrentSubAgentTask: (task: string | null) => void;
  updateSubAgentProgress: (agentId: string, progress: number, status: string) => void;
  clearAll: () => void;

  // 联动：从 SubAgentProgress 跳转到 AIChangesPanel
  navigateToTask: (taskId: string, tasks: AgentTask[]) => void;
}

export const useAgentProgressStore = create<AgentProgressState>((set, get) => ({
  selectedTaskId: null,
  selectedTask: null,
  selectedFilePath: null,
  selectedFileOriginalContent: null,
  selectedFileCurrentContent: null,
  fileModifications: [],
  currentModificationIndex: -1,
  diffViewerOpen: false,
  currentSubAgentTask: null,
  subAgentProgressMap: new Map(),

  setSelectedTask: (taskId, task) => set({
    selectedTaskId: taskId,
    selectedTask: task || null
  }),

  setSelectedFile: (filePath, originalContent, currentContent) => set({
    selectedFilePath: filePath,
    selectedFileOriginalContent: originalContent ?? null,
    selectedFileCurrentContent: currentContent ?? null,
    diffViewerOpen: filePath !== null,
  }),

  updateFileModifications: (tasks) => {
    const modifications: FileModification[] = [];
    tasks.forEach((task, taskIndex) => {
      if (task.modifiedFiles && task.modifiedFiles.length > 0) {
        task.modifiedFiles.forEach((file: any) => {
          modifications.push({
            taskId: task.id,
            taskName: task.name || `Task ${taskIndex + 1}`,
            taskIndex,
            filePath: file.path,
          });
        });
      }
    });

    const { currentModificationIndex } = get();
    const newIndex = currentModificationIndex >= modifications.length ? modifications.length - 1 : currentModificationIndex;

    set({
      fileModifications: modifications,
      currentModificationIndex: Math.max(-1, newIndex),
    });
  },

  navigateToPrevModification: () => {
    const { fileModifications, currentModificationIndex } = get();
    if (fileModifications.length === 0) return false;

    let newIndex = currentModificationIndex - 1;
    if (newIndex < 0) {
      newIndex = fileModifications.length - 1;
    }

    const mod = fileModifications[newIndex];
    if (mod) {
      set({
        currentModificationIndex: newIndex,
        selectedTaskId: mod.taskId,
      });
      return true;
    }
    return false;
  },

  navigateToNextModification: () => {
    const { fileModifications, currentModificationIndex } = get();
    if (fileModifications.length === 0) return false;

    let newIndex = currentModificationIndex + 1;
    if (newIndex >= fileModifications.length) {
      newIndex = 0;
    }

    const mod = fileModifications[newIndex];
    if (mod) {
      set({
        currentModificationIndex: newIndex,
        selectedTaskId: mod.taskId,
      });
      return true;
    }
    return false;
  },

  openDiffViewer: () => set({ diffViewerOpen: true }),
  closeDiffViewer: () => set({ diffViewerOpen: false, selectedFilePath: null }),

  setCurrentSubAgentTask: (task) => set({ currentSubAgentTask: task }),

  updateSubAgentProgress: (agentId, progress, status) => {
    const { subAgentProgressMap } = get();
    const newMap = new Map(subAgentProgressMap);
    newMap.set(agentId, { progress, status });
    set({ subAgentProgressMap: newMap });
  },

  clearAll: () => set({
    selectedTaskId: null,
    selectedTask: null,
    selectedFilePath: null,
    selectedFileOriginalContent: null,
    selectedFileCurrentContent: null,
    fileModifications: [],
    currentModificationIndex: -1,
    diffViewerOpen: false,
    currentSubAgentTask: null,
  }),

  // 联动：从 SubAgentProgress 跳转到 AIChangesPanel
  navigateToTask: (taskId, tasks) => {
    const task = tasks.find((t: AgentTask) => t.id === taskId);
    set({
      selectedTaskId: taskId,
      selectedTask: task || null,
    });
  },
}));
