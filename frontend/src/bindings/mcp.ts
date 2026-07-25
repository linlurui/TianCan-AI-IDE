/**
 * MCP Bindings - Model Context Protocol
 *
 * Auto-generated - matches backend Go structs with exported fields
 */

export interface MCPConfig {
  Type: string;
  Command?: string;
  Args?: string[];
  Url?: string;
  Env?: Record<string, string>;
  Disabled?: boolean;
}

export interface MCPServer {
  Name: string;
  Type: string;
  Config: MCPConfig;
  Tools: MCPToolInfo[];
  Error?: string;
}

export interface MCPToolInfo {
  Name: string;
  Description: string;
  ServerName?: string;
  InputSchema?: Record<string, unknown>;
  ReadOnly?: boolean;
}

/**
 * 获取所有 MCP 服务器状态
 */
export async function GetServers(): Promise<MCPServer[]> {
  // TODO: 绑定到后端
  return [];
}

/**
 * 连接 MCP 服务器
 */
export async function ConnectServer(name: string, config: MCPConfig): Promise<boolean> {
  // TODO: 绑定到后端
  return false;
}

/**
 * 断开 MCP 服务器连接
 */
export async function DisconnectServer(name: string): Promise<boolean> {
  // TODO: 绑定到后端
  return false;
}

/**
 * 切换服务器启用状态
 */
export async function ToggleServer(name: string, enabled: boolean): Promise<boolean> {
  // TODO: 绑定到后端
  return false;
}

/**
 * 保存 MCP 配置
 */
export async function SaveConfig(name: string, config: MCPConfig): Promise<boolean> {
  // TODO: 绑定到后端
  return false;
}

/**
 * 删除 MCP 配置
 */
export async function DeleteConfig(name: string): Promise<boolean> {
  // TODO: 绑定到后端
  return false;
}

/**
 * 列出服务器可用工具
 */
export async function ListTools(name: string): Promise<MCPToolInfo[]> {
  // TODO: 绑定到后端
  return [];
}

/**
 * 获取所有配置
 */
export async function GetConfigs(): Promise<Record<string, MCPConfig>> {
  // TODO: 绑定到后端
  return {};
}
