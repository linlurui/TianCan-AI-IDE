/**
 * AIChangesPanel - AI 修改任务面板
 *
 * 功能：
 * 1. 显示 AI Agent 的任务列表
 * 2. 点击文件显示 MonacoDiffViewer 对比
 * 3. 支持 Apply/Reject 操作
 * 4. 与 SubAgentProgress 共享状态 (via useAgentProgressStore)
 */
import React, { useState, useEffect, useCallback } from 'react';
import {
  ChevronRight,
  ChevronDown,
  Check,
  X,
  Clock,
  FileCode,
  Loader2,
  AlertCircle,
  GitCommitHorizontal,
  ChevronLast,
  ChevronFirst,
  FileSearch,
  Eye,
  Diff,
} from 'lucide-react';
import { AgentTask, TaskFile } from '../bindings/task';
import * as Task from '../bindings/task';
import { GetFileOriginalContent } from '../bindings/task';
import { useAgentProgressStore } from '../hooks/useAgentProgress';
import { useTranslation } from '../i18n';
import MonacoDiffViewer from './MonacoDiffViewer';
// Re-export types for external use
export type { AgentTask, TaskFile };

interface AIChangesPanelProps {
  tasks: AgentTask[];
  onApplyTask: (taskId: string) => void;
  onRejectTask: (taskId: string) => void;
  onApplyFile: (taskId: string, filePath: string) => void;
  onRejectFile: (taskId: string, filePath: string) => void;
  onRefresh: () => void;
}

interface TaskNodeProps {
  task: AgentTask;
  level: number;
  isSelected: boolean;
  onApplyTask: (taskId: string) => void;
  onRejectTask: (taskId: string) => void;
  onApplyFile: (taskId: string, filePath: string) => void;
  onRejectFile: (taskId: string, filePath: string) => void;
  onSelectTask: (taskId: string) => void;
  onViewDiff: (taskId: string, filePath: string, originalContent: string, currentContent: string) => void;
}

const TaskNode: React.FC<TaskNodeProps> = ({
  task,
  level,
  isSelected,
  onApplyTask,
  onRejectTask,
  onApplyFile,
  onRejectFile,
  onSelectTask,
  onViewDiff,
}) => {
  const [expanded, setExpanded] = useState(level === 0 || isSelected);

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'running':
        return 'var(--status-running, #3b82f6)';
      case 'completed':
      case 'applied':
        return 'var(--status-success, #22c55e)';
      case 'failed':
      case 'rejected':
        return 'var(--status-error, #ef4444)';
      case 'pending':
        return 'var(--status-pending, #f59e0b)';
      default:
        return 'var(--text-secondary, #9ca3af)';
    }
  };

  const getStatusIcon = (status: string) => {
    switch (status) {
      case 'running':
        return <Loader2 size={14} className="animate-spin" />;
      case 'completed':
      case 'applied':
        return <Check size={14} />;
      case 'failed':
      case 'rejected':
        return <X size={14} />;
      default:
        return <Clock size={14} />;
    }
  };

  const pendingCount = task.modifiedFiles?.filter(f => f.status === 'pending').length || 0;
  const hasModifiedFiles = task.modifiedFiles && task.modifiedFiles.length > 0;

  // 处理文件点击查看 Diff
  const handleFileClick = async (file: any) => {
    if (file.status !== 'pending') {
      // 非 pending 状态也可以查看
      onViewDiff(task.id, file.path, file.originalContent || '', file.currentContent || file.content || '');
      return;
    }

    // pending 状态：获取原始内容
    try {
      const originalContent = await GetFileOriginalContent(task.id, file.path);
      onViewDiff(task.id, file.path, originalContent || '', file.currentContent || file.content || '');
    } catch (e) {
      console.error('Failed to get original content:', e);
      // 回退：直接显示
      onViewDiff(task.id, file.path, file.originalContent || '', file.currentContent || file.content || '');
    }
  };

  return (
    <div className="task-node" style={{ marginLeft: level * 16 }}>
      {/* 任务头部 */}
      <div
        className={`task-header ${isSelected ? 'task-selected' : ''}`}
        style={{
          display: 'flex',
          alignItems: 'center',
          padding: '8px 12px',
          backgroundColor: isSelected
            ? 'var(--bg-selected, rgba(59, 130, 246, 0.15))'
            : level === 0
              ? 'var(--bg-secondary, #1e1e1e)'
              : 'transparent',
          borderRadius: level === 0 ? 6 : 0,
          marginBottom: 4,
          cursor: hasModifiedFiles ? 'pointer' : 'default',
          borderLeft: isSelected ? '3px solid var(--accent, #3b82f6)' : '3px solid transparent',
          transition: 'background-color 0.15s, border-color 0.15s',
        }}
        onClick={() => {
          if (hasModifiedFiles) {
            setExpanded(!expanded);
            onSelectTask(task.id);
          }
        }}
      >
        {hasModifiedFiles && (
          <span style={{ marginRight: 4, color: 'var(--text-secondary)' }}>
            {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          </span>
        )}
        {!hasModifiedFiles && <span style={{ width: 18 }} />}

        <span style={{ color: getStatusColor(task.status), marginRight: 8 }}>
          {getStatusIcon(task.status)}
        </span>

        <GitCommitHorizontal size={14} style={{ marginRight: 6, color: 'var(--text-secondary)' }} />

        <span style={{ flex: 1, fontWeight: level === 0 ? 500 : 400, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {task.name || 'Unnamed Task'}
        </span>

        {hasModifiedFiles && (
          <span
            style={{
              backgroundColor: 'var(--accent, #3b82f6)',
              color: '#fff',
              padding: '2px 8px',
              borderRadius: 10,
              fontSize: 11,
              fontWeight: 600,
              marginRight: 8,
              display: 'flex',
              alignItems: 'center',
              gap: 4,
            }}
          >
            <FileSearch size={10} />
            {task.modifiedFiles.length}
          </span>
        )}

        {pendingCount > 0 && (
          <span
            style={{
              backgroundColor: 'var(--status-pending, #f59e0b)',
              color: '#000',
              padding: '2px 8px',
              borderRadius: 10,
              fontSize: 11,
              fontWeight: 600,
            }}
          >
            {pendingCount} 待审
          </span>
        )}

        {task.status === 'completed' && pendingCount > 0 && (
          <div style={{ display: 'flex', gap: 4, marginLeft: 8 }}>
            <button
              onClick={(e) => {
                e.stopPropagation();
                onApplyTask(task.id);
              }}
              style={{
                padding: '4px 8px',
                backgroundColor: 'var(--status-success, #22c55e)',
                color: '#fff',
                border: 'none',
                borderRadius: 4,
                cursor: 'pointer',
                fontSize: 12,
                display: 'flex',
                alignItems: 'center',
                gap: 4,
              }}
            >
              <Check size={12} /> 全部应用
            </button>
            <button
              onClick={(e) => {
                e.stopPropagation();
                onRejectTask(task.id);
              }}
              style={{
                padding: '4px 8px',
                backgroundColor: 'var(--status-error, #ef4444)',
                color: '#fff',
                border: 'none',
                borderRadius: 4,
                cursor: 'pointer',
                fontSize: 12,
                display: 'flex',
                alignItems: 'center',
                gap: 4,
              }}
            >
              <X size={12} /> 全部拒绝
            </button>
          </div>
        )}
      </div>

      {/* 文件列表 */}
      {expanded && task.modifiedFiles && task.modifiedFiles.length > 0 && (
        <div className="task-files" style={{ marginLeft: 22, marginBottom: 8 }}>
          {task.modifiedFiles.map((file, idx) => (
            <div
              key={idx}
              style={{
                display: 'flex',
                alignItems: 'center',
                padding: '6px 10px',
                backgroundColor: 'var(--bg-tertiary, #252526)',
                borderRadius: 4,
                marginBottom: 4,
                borderLeft: `3px solid ${getStatusColor(file.status)}`,
                cursor: 'pointer',
                transition: 'background-color 0.15s',
              }}
              onClick={() => handleFileClick(file)}
              onMouseEnter={(e) => {
                e.currentTarget.style.backgroundColor = 'var(--bg-secondary, #2d2d2d)';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.backgroundColor = 'var(--bg-tertiary, #252526)';
              }}
            >
              <FileCode size={14} style={{ marginRight: 8, color: 'var(--text-secondary)' }} />
              <span style={{ flex: 1, fontFamily: 'monospace', fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {file.path}
              </span>

              {/* 查看 Diff 按钮 */}
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  handleFileClick(file);
                }}
                style={{
                  padding: '2px 6px',
                  backgroundColor: 'var(--bg-secondary, #333)',
                  color: 'var(--text-secondary)',
                  border: 'none',
                  borderRadius: 3,
                  cursor: 'pointer',
                  fontSize: 11,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 4,
                  marginRight: 4,
                }}
                title="查看修改对比"
              >
                <Eye size={10} />
                查看
              </button>

              {file.status === 'pending' && (
                <div style={{ display: 'flex', gap: 4 }}>
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      onApplyFile(task.id, file.path);
                    }}
                    style={{
                      padding: '2px 6px',
                      backgroundColor: 'var(--status-success, #22c55e)',
                      color: '#fff',
                      border: 'none',
                      borderRadius: 3,
                      cursor: 'pointer',
                      fontSize: 11,
                    }}
                  >
                    <Check size={10} />
                  </button>
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      onRejectFile(task.id, file.path);
                    }}
                    style={{
                      padding: '2px 6px',
                      backgroundColor: 'var(--status-error, #ef4444)',
                      color: '#fff',
                      border: 'none',
                      borderRadius: 3,
                      cursor: 'pointer',
                      fontSize: 11,
                    }}
                  >
                    <X size={10} />
                  </button>
                </div>
              )}

              {file.status === 'applied' && (
                <span style={{ color: 'var(--status-success)', fontSize: 11 }}>✓ 已应用</span>
              )}
              {file.status === 'rejected' && (
                <span style={{ color: 'var(--status-error)', fontSize: 11 }}>✗ 已拒绝</span>
              )}
            </div>
          ))}
        </div>
      )}

      {/* 子任务 */}
      {(task.children || []).map((child, idx) => (
        child && <TaskNode
          key={idx}
          task={child}
          level={level + 1}
          isSelected={false}
          onApplyTask={onApplyTask}
          onRejectTask={onRejectTask}
          onApplyFile={onApplyFile}
          onRejectFile={onRejectFile}
          onSelectTask={onSelectTask}
          onViewDiff={onViewDiff}
        />
      ))}
    </div>
  );
};

export const AIChangesPanel: React.FC<AIChangesPanelProps> = ({
  tasks,
  onApplyTask,
  onRejectTask,
  onApplyFile,
  onRejectFile,
  onRefresh,
}) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [localTasks, setLocalTasks] = useState<AgentTask[]>(tasks);
  const [error, setError] = useState<string | null>(null);

  // Diff 查看器状态
  const [diffFile, setDiffFile] = useState<{
    taskId: string;
    filePath: string;
    originalContent: string;
    currentContent: string;
  } | null>(null);

  // 使用共享状态
  const {
    selectedTaskId,
    setSelectedTask,
    selectedFilePath,
    fileModifications,
    currentModificationIndex,
    updateFileModifications,
    navigateToPrevModification,
    navigateToNextModification,
  } = useAgentProgressStore();

  // 同步 tasks 到共享状态
  useEffect(() => {
    setLocalTasks(tasks);
    updateFileModifications(tasks);

    // 自动选中最新任务
    if (tasks.length > 0) {
      const tasksWithMods = tasks.filter(t => t.modifiedFiles && t.modifiedFiles.length > 0);
      if (tasksWithMods.length > 0) {
        const latestTask = tasksWithMods[tasksWithMods.length - 1];
        if (!selectedTaskId || !tasksWithMods.find(t => t.id === selectedTaskId)) {
          setSelectedTask(latestTask.id, latestTask);
        }
      }
    }
  }, [tasks, selectedTaskId, setSelectedTask, updateFileModifications]);

  // 从后端刷新任务
  const handleRefresh = async () => {
    setLoading(true);
    setError(null);
    try {
      const backendTasks = await Task.GetAgentTasks();
      if (backendTasks && backendTasks.length > 0) {
        setLocalTasks(backendTasks);
        updateFileModifications(backendTasks);
      }
    } catch (err) {
      setError('从后端加载任务失败');
      console.error(err);
    } finally {
      setLoading(false);
      onRefresh?.();
    }
  };

  // 选中任务
  const handleSelectTask = (taskId: string) => {
    const task = localTasks.find(t => t.id === taskId);
    setSelectedTask(taskId, task || undefined);
  };

  // 应用任务
  const handleApplyTask = async (taskId: string) => {
    try {
      await Task.ApplyTask(taskId);
      handleRefresh();
      onApplyTask(taskId);
    } catch (err) {
      console.error('Failed to apply task:', err);
    }
  };

  // 拒绝任务
  const handleRejectTask = async (taskId: string) => {
    try {
      await Task.RejectTask(taskId);
      handleRefresh();
      onRejectTask(taskId);
    } catch (err) {
      console.error('Failed to reject task:', err);
    }
  };

  // 应用文件
  const handleApplyFile = async (taskId: string, filePath: string) => {
    try {
      await Task.ApplyFile(taskId, filePath);
      handleRefresh();
      onApplyFile(taskId, filePath);
    } catch (err) {
      console.error('Failed to apply file:', err);
    }
  };

  // 拒绝文件
  const handleRejectFile = async (taskId: string, filePath: string) => {
    try {
      await Task.RejectFile(taskId, filePath);
      handleRefresh();
      onRejectFile(taskId, filePath);
    } catch (err) {
      console.error('Failed to reject file:', err);
    }
  };

  // 查看 Diff
  const handleViewDiff = useCallback((
    taskId: string,
    filePath: string,
    originalContent: string,
    currentContent: string
  ) => {
    setDiffFile({ taskId, filePath, originalContent, currentContent });
  }, []);

  // 关闭 Diff 查看器
  const handleCloseDiff = useCallback(() => {
    setDiffFile(null);
  }, []);

  // Diff 查看器中的应用/拒绝
  const handleDiffApply = async () => {
    if (diffFile) {
      await handleApplyFile(diffFile.taskId, diffFile.filePath);
      setDiffFile(null);
    }
  };

  const handleDiffReject = async () => {
    if (diffFile) {
      await handleRejectFile(diffFile.taskId, diffFile.filePath);
      setDiffFile(null);
    }
  };

  const hasModifications = fileModifications.length > 0;

  return (
    <div
      style={{
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        backgroundColor: 'var(--bg-primary, #1e1e1e)',
        color: 'var(--text-primary, #ccc)',
      }}
    >
      {/* Header */}
      <div
        style={{
          padding: '12px 16px',
          borderBottom: '1px solid var(--border-color, #333)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          flexWrap: 'wrap',
          gap: 8,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Diff size={16} style={{ color: 'var(--accent, #3b82f6)' }} />
          <h3 style={{ margin: 0, fontSize: 14, fontWeight: 600 }}>AI Changes</h3>
        </div>

        {/* Navigation */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          {hasModifications && (
            <>
              <span style={{ fontSize: 11, color: 'var(--text-secondary)', marginRight: 4 }}>
                {currentModificationIndex + 1}/{fileModifications.length}
              </span>
              <button
                onClick={() => {
                  if (navigateToPrevModification()) {
                    handleRefresh();
                  }
                }}
                disabled={loading || !hasModifications}
                style={{
                  padding: '4px 8px',
                  backgroundColor: 'var(--bg-secondary)',
                  border: '1px solid var(--border-color)',
                  borderRadius: 4,
                  cursor: loading || !hasModifications ? 'not-allowed' : 'pointer',
                  color: 'var(--text-primary)',
                  fontSize: 12,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 2,
                  opacity: loading || !hasModifications ? 0.5 : 1,
                }}
              >
                <ChevronFirst size={14} />
              </button>
              <button
                onClick={() => {
                  if (navigateToNextModification()) {
                    handleRefresh();
                  }
                }}
                disabled={loading || !hasModifications}
                style={{
                  padding: '4px 8px',
                  backgroundColor: 'var(--bg-secondary)',
                  border: '1px solid var(--border-color)',
                  borderRadius: 4,
                  cursor: loading || !hasModifications ? 'not-allowed' : 'pointer',
                  color: 'var(--text-primary)',
                  fontSize: 12,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 2,
                  opacity: loading || !hasModifications ? 0.5 : 1,
                }}
              >
                <ChevronLast size={14} />
              </button>
              <div style={{ width: 1, height: 20, backgroundColor: 'var(--border-color)', margin: '0 4px' }} />
            </>
          )}
          <button
            onClick={handleRefresh}
            disabled={loading}
            style={{
              padding: '4px 8px',
              backgroundColor: 'var(--bg-secondary)',
              border: '1px solid var(--border-color)',
              borderRadius: 4,
              cursor: loading ? 'not-allowed' : 'pointer',
              color: 'var(--text-primary)',
              fontSize: 12,
              display: 'flex',
              alignItems: 'center',
              gap: 4,
              opacity: loading ? 0.6 : 1,
            }}
          >
            <Clock size={12} className={loading ? 'animate-spin' : ''} />
            刷新
          </button>
        </div>
      </div>

      {/* 当前任务指示器 */}
      {hasModifications && fileModifications.length > 0 && (
        <div
          style={{
            padding: '8px 16px',
            backgroundColor: 'var(--accent, #3b82f6)',
            color: '#fff',
            fontSize: 12,
            display: 'flex',
            alignItems: 'center',
            gap: 8,
          }}
        >
          <FileSearch size={14} />
          <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {fileModifications[currentModificationIndex]?.taskName || 'Task'}
          </span>
          <span style={{ opacity: 0.7, fontSize: 11 }}>
            {currentModificationIndex + 1} / {fileModifications.length}
          </span>
        </div>
      )}

      {/* Diff 查看器 */}
      {diffFile && (
        <div style={{ padding: 12, borderBottom: '1px solid var(--border-color, #333)' }}>
          <MonacoDiffViewer
            originalContent={diffFile.originalContent}
            modifiedContent={diffFile.currentContent}
            filePath={diffFile.filePath}
            onApply={handleDiffApply}
            onReject={handleDiffReject}
            onClose={handleCloseDiff}
            height={280}
          />
        </div>
      )}

      {/* 任务列表 */}
      <div style={{ flex: 1, overflow: 'auto', padding: 12 }}>
        {error && (
          <div
            style={{
              padding: 12,
              backgroundColor: 'rgba(239, 68, 68, 0.1)',
              borderRadius: 6,
              marginBottom: 12,
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              color: 'var(--status-error)',
            }}
          >
            <AlertCircle size={14} />
            {error}
          </div>
        )}

        {localTasks.length === 0 ? (
          <div
            style={{
              textAlign: 'center',
              padding: 40,
              color: 'var(--text-secondary)',
            }}
          >
            <GitCommitHorizontal size={32} style={{ marginBottom: 12, opacity: 0.5 }} />
            <p style={{ margin: 0, fontSize: 13 }}>
              暂无 AI 任务<br />
              <span style={{ fontSize: 12 }}>
                AI 修改将显示在此处供您审查
              </span>
            </p>
          </div>
        ) : (
          localTasks.map((task, idx) => (
            <TaskNode
              key={task.id || idx}
              task={task}
              level={0}
              isSelected={task.id === selectedTaskId}
              onApplyTask={handleApplyTask}
              onRejectTask={handleRejectTask}
              onApplyFile={handleApplyFile}
              onRejectFile={handleRejectFile}
              onSelectTask={handleSelectTask}
              onViewDiff={handleViewDiff}
            />
          ))
        )}
      </div>

      {/* Footer */}
      {localTasks.length > 0 && (
        <div
          style={{
            padding: '8px 16px',
            borderTop: '1px solid var(--border-color, #333)',
            fontSize: 11,
            color: 'var(--text-secondary)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <span>✓ 应用 · ✗ 拒绝 · 👁 查看对比</span>
          {hasModifications && (
            <span style={{ color: 'var(--accent)' }}>
              使用 ← → 导航任务
            </span>
          )}
        </div>
      )}
    </div>
  );
};
