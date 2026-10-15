// Rollback/Checkpoint API bindings for AI file change tracking
import {
  CreateCheckpoint,
  GetCheckpoints,
  DeleteCheckpoint,
  GetModifiedFiles,
  RollbackAll,
  RollbackFileToSnapshot,
  RollbackToCheckpoint,
  GetFileDiff,
  GetFileSnapshotContent,
  CreateSnapshot,
  GetSnapshotCount,
  ClearSnapshots,
} from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/rollbackbindings";

// Re-export types from generated bindings
export type {
  Checkpoint,
  ModifiedFileInfo,
  FileSnapshot,
  RollbackManager,
} from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/ralph/models";

/**
 * Rollback bindings API for frontend
 */
export const Rollback = {
  /**
   * Create a checkpoint with a name and description
   */
  CreateCheckpoint,

  /**
   * Get all checkpoints
   */
  GetCheckpoints,

  /**
   * Delete a checkpoint by ID
   */
  DeleteCheckpoint,

  /**
   * Get list of modified files
   */
  GetModifiedFiles,

  /**
   * Rollback all files to the last snapshot
   */
  RollbackAll,

  /**
   * Rollback a single file to a specific snapshot
   */
  RollbackFileToSnapshot,

  /**
   * Rollback all files to a specific checkpoint
   */
  RollbackToCheckpoint,

  /**
   * Get the diff between current file and a snapshot
   */
  GetFileDiff,

  /**
   * Get the content of a specific snapshot
   */
  GetFileSnapshotContent,

  /**
   * Create a manual snapshot for a file
   */
  CreateSnapshot,

  /**
   * Get the number of snapshots
   */
  GetSnapshotCount,

  /**
   * Clear all snapshots
   */
  ClearSnapshots,
};
