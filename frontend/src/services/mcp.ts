/**
 * MCP Service - wraps the Wails generated bindings
 *
 * Auto-generated - uses Wails runtime bindings
 */

// Import types from the Wails-generated bindings
import type {
  MCPConfig,
  MCPServerState,
  MCPToolInfo,
  MCPServerConnType,
} from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/types/models";

// Import functions from the Wails-generated service bindings
import * as mcpService from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/mcp/service";

// Re-export types for convenience
export type {
  MCPConfig,
  MCPServerState,
  MCPToolInfo,
};

// Re-export enums
export { MCPServerConnType } from "../../bindings/github.com/linlurui/tiancan-ai-ide/backend/agent/types/models";

/**
 * Alias for MCPServerState - represents a connected MCP server
 */
export interface MCPServer extends MCPServerState {}

/**
 * 获取所有 MCP 服务器状态
 */
export async function GetServers(): Promise<MCPServerState[]> {
  return mcpService.GetServers() as unknown as MCPServerState[];
}

/**
 * 连接到 MCP 服务器
 * @param name 服务器名称
 * @param config 服务器配置
 */
export async function ConnectServer(name: string, config: MCPConfig): Promise<string> {
  return mcpService.ConnectServer(name, config) as unknown as string;
}

/**
 * 断开 MCP 服务器连接
 */
export async function DisconnectServer(name: string): Promise<void> {
  return mcpService.DisconnectServer(name) as unknown as void;
}

/**
 * 启用/禁用 MCP 服务器
 */
export async function ToggleServer(name: string, enabled: boolean): Promise<void> {
  return mcpService.ToggleServer(name, enabled) as unknown as void;
}

/**
 * 保存 MCP 配置
 * @param name 服务器名称
 * @param config 服务器配置
 */
export async function SaveConfig(name: string, config: MCPConfig): Promise<void> {
  return mcpService.SaveConfig(name, config) as unknown as void;
}

/**
 * 删除 MCP 配置
 */
export async function DeleteConfig(name: string): Promise<void> {
  return mcpService.DeleteConfig(name) as unknown as void;
}

/**
 * 获取所有可用的 MCP 工具（来自所有已连接服务器）
 */
export async function ListTools(): Promise<MCPToolInfo[]> {
  return mcpService.ListTools() as unknown as MCPToolInfo[];
}

/**
 * 获取所有可用的 MCP 资源（来自所有已连接服务器）
 */
export async function ListResources(): Promise<unknown[]> {
  return mcpService.ListResources() as unknown as unknown[];
}

/**
 * 获取所有可用的 MCP 提示词（来自所有已连接服务器）
 */
export async function ListPrompts(): Promise<unknown[]> {
  return mcpService.ListPrompts() as unknown as unknown[];
}

/**
 * 获取保存的 MCP 配置映射 (name -> config)
 */
export async function GetConfigs(): Promise<Record<string, MCPConfig>> {
  return mcpService.GetConfigs() as unknown as Record<string, MCPConfig>;
}

/**
 * 调用 MCP 工具
 * @param toolName 工具名称 (格式: serverName/toolName)
 * @param args 工具参数
 */
export async function CallTool(toolName: string, args: Record<string, unknown>): Promise<[string, boolean]> {
  return mcpService.CallTool(toolName, args) as unknown as [string, boolean];
}

/**
 * 检查工具是否允许调用
 */
export async function IsToolAllowed(serverName: string, toolName: string): Promise<boolean> {
  return mcpService.IsToolAllowed(serverName, toolName) as unknown as boolean;
}

/**
 * 刷新工具列表
 */
export async function RefreshTools(): Promise<void> {
  return mcpService.RefreshTools() as unknown as void;
}

/**
 * 获取工具描述（用于系统提示词）
 */
export async function GetToolDescriptions(): Promise<string> {
  return mcpService.GetToolDescriptions() as unknown as string;
}
