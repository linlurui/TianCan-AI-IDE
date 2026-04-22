// Re-export from wails3 auto-generated bindings
export type { FileStatus, RepoStatus, CommitInfo, CommitFileInfo } from "../../bindings/github.com/rocky233/tiancan-ai-ide/backend/git/models";
export {
  InitRepo,
  GetStatus,
  StageAll,
  Commit,
  GetLog,
  GetFileDiff,
  GetHeadFileContent,
  StageFile,
  UnstageFile,
  DiscardFile,
  RevertCommit,
  GetCommitFiles,
  GetCommitFileDiff,
  RestoreFileFromCommit,
} from "../../bindings/github.com/rocky233/tiancan-ai-ide/backend/git/service";
