import { FileNode } from "./bindings/filesystem";
import { RepoStatus } from "./bindings/git";

export type ProjectType = "java" | "frontend" | "golang" | "python" | "rust" | "other";

export interface Project {
  id: string;
  rootPath: string;
  name: string;
  type?: ProjectType;
  tree: FileNode | null;
  gitStatus: RepoStatus | null;
}
