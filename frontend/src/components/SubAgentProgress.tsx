/**
 * SubAgentProgress - 子Agent进度面板
 *
 * 功能：
 * 1. 显示子Agent执行进度
 * 2. 显示当前任务名称
 * 3. 点击任务可跳转到 AIChangesPanel 对应位置
 * 4. 与 AIChangesPanel 共享状态 (via useAgentProgressStore)
 */
import React, { useEffect, useState, useRef, useCallback } from "react";
import { Events } from "@wailsio/runtime";
import {
  Wrench, CheckCircle2, XCircle, Loader2, ChevronDown, ChevronRight,
  Target, ArrowRight, Clock, AlertCircle
} from "lucide-react";
import { useAgentProgressStore } from "../hooks/useAgentProgress";
import { AgentTask } from "../bindings/task";

interface SubAgentStep {
  agentId: string;
  agentType: string;
  iteration: number;
  toolName?: string;
  toolArgs?: Record<string, unknown>;
  output?: string;
  isDone: boolean;
  error?: string;
  taskId?: string;        // 关联的任务ID
  taskName?: string;      // 当前任务名称
  progress?: number;      // 进度百分比 0-100
}

interface AgentRun {
  agentId: string;
  agentType: string;
  taskId?: string;
  taskName?: string;
  steps: SubAgentStep[];
  isDone: boolean;
  error?: string;
  startTime: Date;
  endTime?: Date;
}

interface SubAgentProgressPanelProps {
  onNavigateToTask?: (taskId: string) => void;
}

export default function SubAgentProgressPanel({ onNavigateToTask }: SubAgentProgressPanelProps) {
  const [runs, setRuns] = useState<AgentRun[]>([]);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const panelRef = useRef<HTMLDivElement>(null);

  const {
    currentSubAgentTask,
    setCurrentSubAgentTask,
    selectedTaskId,
    navigateToTask,
  } = useAgentProgressStore();

  // 监听后端事件
  useEffect(() => {
    const off = Events.On("subagent:progress", (evt: any) => {
      const data = evt?.data ?? evt;
      if (!data || !data.agentId) return;

      setRuns((prev) => {
        const existing = prev.findIndex((r) => r.agentId === data.agentId);
        if (existing >= 0) {
          // 更新已有运行
          const updated = [...prev];
          const run = { ...updated[existing], steps: [...updated[existing].steps] };

          // 查找或添加步骤
          const stepIdx = run.steps.findIndex((s) => s.iteration === data.iteration);
          if (stepIdx >= 0) {
            run.steps[stepIdx] = { ...run.steps[stepIdx], ...data };
          } else {
            run.steps.push(data as SubAgentStep);
          }

          // 更新任务信息
          if (data.taskName) {
            run.taskName = data.taskName;
            setCurrentSubAgentTask(data.taskName);
          }
          if (data.taskId) {
            run.taskId = data.taskId;
          }

          if (data.isDone) {
            run.isDone = true;
            run.error = data.error;
            run.endTime = new Date();
          }

          updated[existing] = run;
          return updated;
        } else {
          // 新增运行
          const newRun: AgentRun = {
            agentId: data.agentId,
            agentType: data.agentType || "general-purpose",
            taskId: data.taskId,
            taskName: data.taskName,
            steps: [data as SubAgentStep],
            isDone: data.isDone || false,
            error: data.error,
            startTime: new Date(),
            endTime: data.isDone ? new Date() : undefined,
          };

          if (data.taskName) {
            setCurrentSubAgentTask(data.taskName);
          }

          return [...prev, newRun];
        }
      });

      // 自动展开新运行
      setExpanded((prev) => ({
        ...prev,
        [data.agentId]: prev[data.agentId] ?? true,
      }));
    });

    return () => { off(); };
  }, [setCurrentSubAgentTask]);

  // 自动滚动到底部
  useEffect(() => {
    if (panelRef.current) {
      panelRef.current.scrollTop = panelRef.current.scrollHeight;
    }
  }, [runs]);

  const toggleExpand = (id: string) => {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  // 计算进度
  const calculateProgress = (run: AgentRun): number => {
    if (run.isDone) return 100;
    const totalSteps = run.steps.length;
    if (totalSteps === 0) return 0;
    // 基于步骤数和是否完成来估算
    return Math.min(95, (totalSteps / 10) * 100);
  };

  // 格式化时间
  const formatDuration = (start: Date, end?: Date): string => {
    const duration = (end || new Date()).getTime() - start.getTime();
    const seconds = Math.floor(duration / 1000);
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}m ${remainingSeconds}s`;
  };

  // 获取当前活动步骤
  const getCurrentStep = (run: AgentRun): SubAgentStep | undefined => {
    return run.steps[run.steps.length - 1];
  };

  // 处理点击跳转到任务
  const handleNavigateToTask = useCallback(async (run: AgentRun) => {
    if (run.taskId) {
      // 尝试从后端获取任务列表来定位
      try {
        const tasks = await (window as any).go.agent.TaskBindings.GetAgentTasks() || [];
        navigateToTask(run.taskId, tasks);
        onNavigateToTask?.(run.taskId);
      } catch (e) {
        console.error("Failed to get tasks for navigation:", e);
        // 仍然触发回调
        onNavigateToTask?.(run.taskId);
      }
    }
  }, [navigateToTask, onNavigateToTask]);

  // 清除所有运行
  const handleClear = () => {
    setRuns([]);
    setExpanded({});
    setCurrentSubAgentTask(null);
  };

  if (runs.length === 0) return null;

  // 统计信息
  const totalSteps = runs.reduce((sum, run) => sum + run.steps.length, 0);
  const completedRuns = runs.filter(r => r.isDone).length;
  const activeRuns = runs.filter(r => !r.isDone).length;

  return (
    <div className="subagent-progress-panel" ref={panelRef}>
      {/* 头部 */}
      <div className="subagent-header">
        <div style={styles.headerLeft}>
          <Target size={12} />
          <span>子 Agent 进度</span>
          {activeRuns > 0 && (
            <span style={styles.badge}>{activeRuns} 活跃</span>
          )}
        </div>
        <button
          className="subagent-clear-btn"
          onClick={handleClear}
          title="清除"
        >
          清除
        </button>
      </div>

      {/* 当前任务指示器 */}
      {currentSubAgentTask && (
        <div style={styles.currentTask}>
          <Clock size={12} />
          <span style={styles.currentTaskText}>{currentSubAgentTask}</span>
        </div>
      )}

      {/* 统计栏 */}
      <div style={styles.statsBar}>
        <span style={styles.statItem}>
          <Wrench size={10} /> {totalSteps} 步
        </span>
        <span style={styles.statItem}>
          <CheckCircle2 size={10} className="subagent-done-icon" /> {completedRuns}
        </span>
        {activeRuns > 0 && (
          <span style={styles.statItem}>
            <Loader2 size={10} className="spin" /> {activeRuns}
          </span>
        )}
      </div>

      {/* 运行列表 */}
      {runs.map((run) => {
        const currentStep = getCurrentStep(run);
        const progress = calculateProgress(run);
        const isExpanded = expanded[run.agentId];
        const isSelected = run.taskId === selectedTaskId;
        const duration = formatDuration(run.startTime, run.endTime);

        return (
          <div
            key={run.agentId}
            className={`subagent-run ${isSelected ? "subagent-run-selected" : ""}`}
            style={isSelected ? styles.selectedRun : undefined}
          >
            {/* 运行头部 */}
            <div
              className="subagent-run-header"
              style={styles.runHeader}
              onClick={() => toggleExpand(run.agentId)}
            >
              <span style={styles.expandIcon}>
                {isExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
              </span>

              <span style={styles.agentType}>{run.agentType}</span>

              {/* 任务名称 */}
              {run.taskName && (
                <>
                  <ArrowRight size={10} style={styles.arrowIcon} />
                  <span style={styles.taskName} title={run.taskName}>
                    {run.taskName.length > 20 ? run.taskName.slice(0, 20) + "..." : run.taskName}
                  </span>
                </>
              )}

              {/* 进度条 */}
              {!run.isDone && (
                <div style={styles.progressBarContainer}>
                  <div style={{ ...styles.progressBar, width: `${progress}%` }} />
                </div>
              )}

              {/* 状态图标 */}
              <span style={styles.statusIcon}>
                {run.isDone ? (
                  run.error ? (
                    <XCircle size={12} className="subagent-error-icon" />
                  ) : (
                    <CheckCircle2 size={12} className="subagent-done-icon" />
                  )
                ) : (
                  <Loader2 size={12} className="spin subagent-running-icon" />
                )}
              </span>

              {/* 步数和时间 */}
              <span style={styles.stepCount}>{run.steps.length} 步</span>
              <span style={styles.duration}>{duration}</span>
            </div>

            {/* 可展开的步骤详情 */}
            {isExpanded && (
              <div className="subagent-steps" style={styles.steps}>
                {/* 当前步骤指示器 */}
                {currentStep && !run.isDone && currentStep.toolName && (
                  <div style={styles.currentStepIndicator}>
                    <AlertCircle size={10} />
                    <span>正在执行: {currentStep.toolName}</span>
                  </div>
                )}

                {/* 步骤列表 */}
                {run.steps.map((step, i) => (
                  <div
                    key={i}
                    style={{
                      ...styles.step,
                      opacity: i === run.steps.length - 1 && !run.isDone ? 1 : 0.8,
                    }}
                  >
                    <div style={styles.stepHeader}>
                      <span style={styles.stepIteration}>#{step.iteration}</span>
                      {step.toolName && (
                        <span style={styles.stepToolName}>
                          <Wrench size={10} /> {step.toolName}
                        </span>
                      )}
                      {step.isDone && !step.error && (
                        <CheckCircle2 size={10} className="subagent-done-icon" />
                      )}
                      {step.error && (
                        <XCircle size={10} className="subagent-error-icon" />
                      )}
                    </div>

                    {/* 输出预览 */}
                    {step.output && (
                      <div style={styles.stepOutput}>
                        {step.output.slice(0, 150)}
                        {step.output.length > 150 && "..."}
                      </div>
                    )}

                    {/* 错误信息 */}
                    {step.error && (
                      <div style={styles.stepError}>{step.error}</div>
                    )}
                  </div>
                ))}

                {/* 跳转到任务按钮 */}
                {run.taskId && (
                  <button
                    style={styles.navigateButton}
                    onClick={(e) => {
                      e.stopPropagation();
                      handleNavigateToTask(run);
                    }}
                  >
                    <ArrowRight size={12} />
                    跳转到任务详情
                  </button>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  headerLeft: {
    display: "flex",
    alignItems: "center",
    gap: 6,
  },
  badge: {
    fontSize: 10,
    padding: "1px 5px",
    borderRadius: 8,
    backgroundColor: "var(--accent, #3b82f6)",
    color: "#fff",
  },
  currentTask: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    padding: "6px 12px",
    backgroundColor: "var(--bg-tertiary, #2d2d2d)",
    borderBottom: "1px solid var(--border-color, #333)",
    fontSize: 11,
    color: "var(--text-secondary, #9ca3af)",
  },
  currentTaskText: {
    flex: 1,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  statsBar: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "4px 12px",
    backgroundColor: "var(--bg-secondary, #252526)",
    borderBottom: "1px solid var(--border-color, #333)",
    fontSize: 10,
    color: "var(--text-muted, #6b7280)",
  },
  statItem: {
    display: "flex",
    alignItems: "center",
    gap: 4,
  },
  runHeader: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    padding: "8px 12px",
    cursor: "pointer",
    borderBottom: "1px solid var(--border-color, #333)",
    transition: "background-color 0.15s",
  },
  selectedRun: {
    backgroundColor: "var(--bg-selected, rgba(59, 130, 246, 0.1))",
    borderLeft: "2px solid var(--accent, #3b82f6)",
  },
  expandIcon: {
    color: "var(--text-muted, #6b7280)",
  },
  agentType: {
    fontSize: 12,
    fontWeight: 500,
    color: "var(--text-primary, #ccc)",
  },
  arrowIcon: {
    color: "var(--text-muted, #6b7280)",
  },
  taskName: {
    fontSize: 11,
    color: "var(--accent, #3b82f6)",
    maxWidth: 120,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  progressBarContainer: {
    flex: 1,
    height: 3,
    backgroundColor: "var(--bg-tertiary, #2d2d2d)",
    borderRadius: 2,
    marginLeft: 8,
    maxWidth: 60,
    overflow: "hidden",
  },
  progressBar: {
    height: "100%",
    backgroundColor: "var(--accent, #3b82f6)",
    borderRadius: 2,
    transition: "width 0.3s ease",
  },
  statusIcon: {
    marginLeft: "auto",
  },
  stepCount: {
    fontSize: 10,
    color: "var(--text-muted, #6b7280)",
    padding: "1px 5px",
    backgroundColor: "var(--bg-tertiary, #2d2d2d)",
    borderRadius: 4,
  },
  duration: {
    fontSize: 10,
    color: "var(--text-muted, #6b7280)",
  },
  steps: {
    padding: "8px 12px",
    backgroundColor: "var(--bg-primary, #1e1e1e)",
  },
  currentStepIndicator: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    padding: "4px 8px",
    backgroundColor: "var(--accent, #3b82f6)",
    color: "#fff",
    borderRadius: 4,
    fontSize: 11,
    marginBottom: 8,
  },
  step: {
    padding: "6px 8px",
    backgroundColor: "var(--bg-secondary, #252526)",
    borderRadius: 4,
    marginBottom: 4,
    borderLeft: "2px solid var(--border-color, #333)",
  },
  stepHeader: {
    display: "flex",
    alignItems: "center",
    gap: 8,
  },
  stepIteration: {
    fontSize: 10,
    color: "var(--text-muted, #6b7280)",
    fontWeight: 500,
  },
  stepToolName: {
    fontSize: 11,
    color: "var(--text-primary, #ccc)",
    display: "flex",
    alignItems: "center",
    gap: 4,
  },
  stepOutput: {
    marginTop: 4,
    fontSize: 10,
    color: "var(--text-muted, #6b7280)",
    fontFamily: "monospace",
    lineHeight: 1.4,
    wordBreak: "break-all",
  },
  stepError: {
    marginTop: 4,
    fontSize: 10,
    color: "var(--status-error, #ef4444)",
    fontFamily: "monospace",
  },
  navigateButton: {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    gap: 6,
    width: "100%",
    marginTop: 8,
    padding: "6px 12px",
    backgroundColor: "var(--accent, #3b82f6)",
    color: "#fff",
    border: "none",
    borderRadius: 4,
    cursor: "pointer",
    fontSize: 11,
    fontWeight: 500,
    transition: "opacity 0.15s",
  },
};
