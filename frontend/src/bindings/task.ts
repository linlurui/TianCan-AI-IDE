// Task bindings for AI agent task management
import {
  GetAgentTasks,
  GetTask,
  ApplyTask,
  RejectTask,
  ApplyFile,
  RejectFile,
  GetFileOriginalContent,
  CleanupOldTasks,
} from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/taskbindings";

export {
  GetAgentTasks,
  GetTask,
  ApplyTask,
  RejectTask,
  ApplyFile,
  RejectFile,
  GetFileOriginalContent,
  CleanupOldTasks,
};

// Re-export types from generated bindings
export type { AgentTask, TaskFile } from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/ralph/models";

/**
 * Task bindings API for frontend
 */
export const Task = {
  /**
   * Get all agent tasks
   */
  GetAgentTasks,

  /**
   * Get a single task by ID
   */
  GetTask,

  /**
   * Apply all changes from a task
   */
  ApplyTask,

  /**
   * Reject all changes from a task
   */
  RejectTask,

  /**
   * Apply a single file from a task
   */
  ApplyFile,

  /**
   * Reject a single file from a task
   */
  RejectFile,

  /**
   * Get the original content of a file for diff viewing
   */
  GetFileOriginalContent,

  /**
   * Clean up old tasks (older than 24 hours)
   */
  CleanupOldTasks,
};
