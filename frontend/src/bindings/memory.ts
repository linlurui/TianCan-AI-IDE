import { Call } from "@wailsio/runtime";

export interface ProjectCacheResult {
  project: string;
  exists: boolean;
  initialized: boolean;
  memoryCount: number;
}

export function EnsureProjectMemory(projectRoot: string): Promise<ProjectCacheResult> {
  return Call.ByName(
    "github.com/linlurui/tiancan-ai-ide/backend/memory.Service.EnsureProjectMemory",
    projectRoot,
  ) as Promise<ProjectCacheResult>;
}
