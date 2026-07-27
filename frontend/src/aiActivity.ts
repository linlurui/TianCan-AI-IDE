export type ActivityKind = "thinking" | "tool" | "result" | "success" | "error" | "route";
export type ActivityStatus = "running" | "success" | "error";

export interface ActivityItem {
  id: number;
  kind: ActivityKind;
  text: string;
  detail?: string;
  output?: string;
  toolName?: string;
  files?: string[];
  status?: ActivityStatus;
  startedAt?: number;
  completedAt?: number;
}

type ToolInfo = { title: string; summary?: string; files: string[] };

const READ_TOOLS = new Set(["read_file", "list_directory", "get_directory_tree", "file_exists"]);
const WRITE_TOOLS = new Set(["write_file", "create_file", "edit_file", "file_write", "file_edit", "apply_patch"]);
const SEARCH_TOOLS = new Set(["grep", "grep_files", "glob", "search_files", "web_search"]);
const COMMAND_TOOLS = new Set(["bash", "shell", "execute_command", "run_command", "test_run"]);

function parseArgs(content?: string): Record<string, unknown> {
  if (!content) return {};
  try {
    const value = JSON.parse(content);
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  } catch {
    return {};
  }
}

function collectPaths(value: unknown, key = "", result: string[] = []): string[] {
  if (typeof value === "string" && /(^|_)(path|file|files|paths|filename)$/.test(key.toLowerCase())) {
    result.push(value);
  } else if (Array.isArray(value)) {
    value.forEach((entry) => collectPaths(entry, key, result));
  } else if (value && typeof value === "object") {
    Object.entries(value as Record<string, unknown>).forEach(([childKey, child]) => collectPaths(child, childKey, result));
  }
  return result;
}

export function describeTool(toolName: string, content?: string): ToolInfo {
  const name = toolName.toLowerCase();
  const args = parseArgs(content);
  const files = [...new Set(collectPaths(args))];
  const command = typeof args.command === "string" ? args.command : "";
  const pattern = typeof args.pattern === "string" ? args.pattern : typeof args.query === "string" ? args.query : "";

  if (READ_TOOLS.has(name)) return { title: files.length > 1 ? `批量读取文件 (${files.length})` : "读取文件", summary: files[0], files };
  if (WRITE_TOOLS.has(name)) return { title: files.length > 1 ? `修改文件 (${files.length})` : "修改文件", summary: files[0], files };
  if (SEARCH_TOOLS.has(name)) return { title: "搜索代码", summary: pattern, files };
  if (COMMAND_TOOLS.has(name)) return { title: "运行命令", summary: command, files };
  if (name.startsWith("git_")) return { title: "检查 Git", summary: name.slice(4).replace(/_/g, " "), files };
  return { title: `执行工具 ${toolName || "tool"}`, files };
}

export function appendActivity(items: ActivityItem[], item: ActivityItem): ActivityItem[] {
  const next = [...items, item];
  return next.length > 160 ? next.slice(next.length - 160) : next;
}

export function finishToolActivity(items: ActivityItem[], toolName: string, detail: string, failed = false): ActivityItem[] {
  let index = -1;
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i].toolName === toolName && items[i].status === "running") {
      index = i;
      break;
    }
  }
  if (index < 0) {
    if (items.some((item) => item.toolName === toolName && item.output === detail)) return items;
    return appendActivity(items, {
      id: Date.now() + Math.random(), kind: failed ? "error" : "result", toolName,
      text: failed ? `${toolName} 执行失败` : `${toolName} 执行完成`, output: detail,
      status: failed ? "error" : "success", startedAt: Date.now(), completedAt: Date.now(),
    });
  }
  const next = [...items];
  next[index] = {
    ...next[index], output: detail,
    kind: failed ? "error" : "result", status: failed ? "error" : "success", completedAt: Date.now(),
  };
  return next;
}

export function withConversationContext(
  text: string,
  messages: Array<{ role: string; content: string }>,
): string {
  const history = messages
    .filter((message) => (message.role === "user" || message.role === "assistant") && message.content.trim())
    .slice(-8)
    .map((message) => `${message.role === "user" ? "用户" : "助手"}: ${message.content.trim()}`)
    .join("\n\n");
  if (!history) return text;
  const limited = history.length > 6000 ? history.slice(history.length - 6000) : history;
  return `以下是同一项目中最近的连续对话，请结合上下文回答当前消息。\n\n${limited}\n\n当前用户消息: ${text}`;
}
