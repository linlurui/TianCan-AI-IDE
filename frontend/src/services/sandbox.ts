// Re-export from bindings with proper types
import {
  GetProviderStatus as _GetProviderStatus,
  GetEnvStatus as _GetEnvStatus,
  ListSandboxes as _ListSandboxes,
  SetProvider as _SetProvider,
  GetCurrentProvider as _GetCurrentProvider,
  CreateSandbox as _CreateSandbox,
  StopSandbox as _StopSandbox,
  ExecInSandbox as _ExecInSandbox,
  InstallProvider as _InstallProvider,
  GetManager as _GetManager,
} from "@bindings/sandbox";
import type {
  ProviderStatus,
  EnvStatus,
  SandboxInfo,
  SandboxConfig,
  SandboxState,
  ExecResult,
} from "@bindings/sandbox";

// Re-export functions
export const GetProviderStatus = _GetProviderStatus;
export const GetEnvStatus = _GetEnvStatus;
export const ListSandboxes = _ListSandboxes;
export const SetProvider = _SetProvider;
export const GetCurrentProvider = _GetCurrentProvider;
export const CreateSandbox = _CreateSandbox;
export const StopSandbox = _StopSandbox;
export const ExecInSandbox = _ExecInSandbox;
export const InstallProvider = _InstallProvider;
export const GetManager = _GetManager;

// Re-export types
export type {
  ProviderStatus,
  EnvStatus,
  SandboxInfo,
  SandboxConfig,
  SandboxState,
  ExecResult,
};

// Helper function to get provider status with proper typing
export async function getSandboxProviders(): Promise<ProviderStatus[]> {
  const status = await _GetProviderStatus();
  return status.map((p: ProviderStatus) => ({
    name: p.name,
    installed: p.installed,
    running: p.running,
    configured: p.configured,
    version: p.version,
    installPath: p.installPath,
  }));
}
