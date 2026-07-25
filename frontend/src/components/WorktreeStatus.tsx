import { useState, useEffect } from "react";
import { GitBranch, Trash2, AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

export interface WorktreeStatusProps {
  onSelectWorktree?: (worktreeId: string) => void;
}

interface WorktreeInfo {
  id: string;
  branch: string;
  path: string;
  status: "active" | "idle" | "merged" | "conflicted";
  agentId?: string;
  createdAt: string;
  lastActivity?: string;
}

interface WorktreeStats {
  total: number;
  active: number;
  idle: number;
  cleanup: number;
  maxParallel: number;
}

// Mock data for demo
const mockWorktrees: WorktreeInfo[] = [
  {
    id: "agent-001",
    branch: "agent-001",
    path: ".tiancan/worktrees/agent-001",
    status: "active",
    agentId: "subagent-123",
    createdAt: "2 minutes ago",
    lastActivity: "10 seconds ago",
  },
  {
    id: "agent-002",
    branch: "agent-002",
    path: ".tiancan/worktrees/agent-002",
    status: "idle",
    agentId: "subagent-456",
    createdAt: "8 minutes ago",
    lastActivity: "5 minutes ago",
  },
];

export function WorktreeStatus({ onSelectWorktree }: WorktreeStatusProps) {
  const [worktrees, setWorktrees] = useState<WorktreeInfo[]>(mockWorktrees);
  const [stats, setStats] = useState<WorktreeStats>({
    total: 2,
    active: 1,
    idle: 1,
    cleanup: 0,
    maxParallel: 8,
  });
  const [loading, setLoading] = useState(false);

  // In real implementation, would fetch from backend via Wails bindings
  useEffect(() => {
    // Simulate fetching worktree status
    const interval = setInterval(() => {
      // In real app: fetch from backend
      // const { GetWorktreeStats, ListWorktrees } = await import("./bindings/agent");
    }, 5000);

    return () => clearInterval(interval);
  }, []);

  const getStatusIcon = (status: WorktreeInfo["status"]) => {
    switch (status) {
      case "active":
        return <CheckCircle2 className="w-4 h-4 text-green-500" />;
      case "idle":
        return <Loader2 className="w-4 h-4 text-yellow-500" />;
      case "conflicted":
        return <AlertCircle className="w-4 h-4 text-red-500" />;
      case "merged":
        return <GitBranch className="w-4 h-4 text-blue-500" />;
    }
  };

  const getStatusText = (status: WorktreeInfo["status"]) => {
    switch (status) {
      case "active":
        return "Active";
      case "idle":
        return "Idle (pending cleanup)";
      case "conflicted":
        return "Conflict";
      case "merged":
        return "Merged";
    }
  };

  return (
    <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg p-4 min-w-[350px]">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <GitBranch className="w-5 h-5 text-purple-500" />
          <h3 className="font-semibold text-gray-900 dark:text-gray-100">Worktree Isolation</h3>
        </div>
        <span className="text-xs text-gray-500">
          {stats.active}/{stats.maxParallel} active
        </span>
      </div>

      {/* Stats bar */}
      <div className="flex gap-2 mb-4">
        <div className="flex-1 p-2 bg-green-50 dark:bg-green-900/20 rounded text-center">
          <div className="text-lg font-semibold text-green-600">{stats.active}</div>
          <div className="text-xs text-green-600/70">Active</div>
        </div>
        <div className="flex-1 p-2 bg-yellow-50 dark:bg-yellow-900/20 rounded text-center">
          <div className="text-lg font-semibold text-yellow-600">{stats.idle}</div>
          <div className="text-xs text-yellow-600/70">Idle</div>
        </div>
        <div className="flex-1 p-2 bg-red-50 dark:bg-red-900/20 rounded text-center">
          <div className="text-lg font-semibold text-red-600">{stats.cleanup}</div>
          <div className="text-xs text-red-600/70">Conflicts</div>
        </div>
      </div>

      {/* Worktree list */}
      <div className="space-y-2 max-h-[200px] overflow-y-auto">
        {worktrees.length === 0 ? (
          <div className="text-center py-4 text-gray-500">
            <GitBranch className="w-8 h-8 mx-auto mb-2 opacity-50" />
            <p className="text-sm">No active worktrees</p>
          </div>
        ) : (
          worktrees.map((wt) => (
            <div
              key={wt.id}
              className={`p-3 rounded-lg border ${
                wt.status === "active"
                  ? "border-green-200 dark:border-green-800 bg-green-50/50 dark:bg-green-900/10"
                  : wt.status === "conflicted"
                  ? "border-red-200 dark:border-red-800 bg-red-50/50 dark:bg-red-900/10"
                  : "border-gray-200 dark:border-gray-700 bg-gray-50/50 dark:bg-gray-700/10"
              } ${onSelectWorktree ? "cursor-pointer hover:shadow-md transition-shadow" : ""}`}
              onClick={() => onSelectWorktree?.(wt.id)}
            >
              <div className="flex items-center justify-between mb-1">
                <div className="flex items-center gap-2">
                  {getStatusIcon(wt.status)}
                  <span className="font-medium text-sm text-gray-900 dark:text-gray-100">
                    {wt.branch}
                  </span>
                </div>
                <span className={`text-xs px-2 py-0.5 rounded ${
                  wt.status === "active"
                    ? "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300"
                    : wt.status === "conflicted"
                    ? "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300"
                    : "bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300"
                }`}>
                  {getStatusText(wt.status)}
                </span>
              </div>
              <div className="text-xs text-gray-500 dark:text-gray-400 ml-6">
                <p>Path: {wt.path}</p>
                {wt.agentId && <p>Agent: {wt.agentId}</p>}
                <p>Created: {wt.createdAt}</p>
                {wt.lastActivity && <p>Last activity: {wt.lastActivity}</p>}
              </div>
            </div>
          ))
        )}
      </div>

      {/* Info text */}
      <div className="mt-4 p-3 bg-blue-50 dark:bg-blue-900/20 rounded-lg">
        <p className="text-xs text-blue-700 dark:text-blue-300">
          <strong>Worktree Isolation:</strong> Each sub-agent runs in an isolated Git worktree,
          preventing conflicts between parallel agents. Idle worktrees are automatically cleaned up
          after 10 minutes.
        </p>
      </div>
    </div>
  );
}
