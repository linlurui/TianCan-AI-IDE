// Sandbox bindings - types and stubs
// These will be generated from Go backend

export interface ProviderStatus {
  name: string;
  available: boolean;
  statusMessage: string;
  installRequired: boolean;
  installCommand: string;
  running: boolean;
  configured: boolean;
  installed: boolean;
  version?: string;
  installPath?: string;
}

export interface SandboxInfo {
  id: string;
  name: string;
  state: string;
  provider: string;
  createdAt: string;
}

export interface SandboxConfig {
  provider: string;
  timeout: number;
  autoRemove: boolean;
}

export interface SandboxState {
  status: string;
  startedAt?: string;
  expiresAt?: string;
}

export interface ExecResult {
  exitCode: number;
  stdout: string;
  stderr: string;
}

export interface EnvStatus {
  available: boolean;
  installedProviders: string[];
  defaultProvider: string;
}

export interface Manager {
  provider: string;
  isConnected: boolean;
}

// Stubs - these need to be connected to actual backend
export async function GetProviderStatus(): Promise<ProviderStatus[]> {
  console.warn("Sandbox bindings not generated - using stub");
  return [
    { name: "process", available: true, statusMessage: "Built-in process isolation", installRequired: false, installCommand: "", running: true, configured: true, installed: true },
    { name: "docker", available: false, statusMessage: "Docker not found", installRequired: true, installCommand: "brew install docker", running: false, configured: false, installed: false },
    { name: "daytona", available: false, statusMessage: "Daytona not installed", installRequired: true, installCommand: "curl -sf https://download.daytona.io/daytona/install.sh | sh", running: false, configured: false, installed: false },
  ];
}

export async function ListSandboxes(): Promise<SandboxInfo[]> {
  return [];
}

export async function SetProvider(name: string): Promise<void> {
  console.warn("Sandbox SetProvider not implemented:", name);
}

export async function StopSandbox(id: string): Promise<void> {
  console.warn("Sandbox StopSandbox not implemented:", id);
}

export async function InstallProvider(name: string): Promise<void> {
  console.warn("Sandbox InstallProvider not implemented:", name);
}

export async function CreateSandbox(config: SandboxConfig): Promise<SandboxInfo> {
  console.warn("Sandbox CreateSandbox not implemented");
  return { id: "", name: "", state: "", provider: "", createdAt: "" };
}

export async function ExecInSandbox(id: string, cmd: string): Promise<ExecResult> {
  console.warn("Sandbox ExecInSandbox not implemented");
  return { exitCode: 1, stdout: "", stderr: "Not implemented" };
}

export async function GetCurrentProvider(): Promise<string> {
  return "process";
}

export async function GetEnvStatus(): Promise<EnvStatus> {
  return { available: true, installedProviders: ["process", "docker"], defaultProvider: "process" };
}

export async function GetManager(): Promise<Manager> {
  return { provider: "process", isConnected: true };
}
