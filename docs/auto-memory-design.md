# Auto Memory（Markdown 体系）设计

代码位置：`backend/agent/memory/memory.go`（系统本体）、`backend/agent/agent.go`（接线）。

## 1. 它是什么

Claude Code 风格的文件化记忆：`~/.tiancan/memory/`（`TIANCAN_MEMORY_DIR` 可改观）

```
memory/
├── MEMORY.md              # 蒸馏索引（人工可读的总目录）
├── SESSION_MEMORY.md      # 结构化会话笔记（自动提取的目标）
├── logs/YYYY/MM/YYYY-MM-DD.md   # append-only 每日日志
├── config/template.md     # 可选：自定义 SESSION_MEMORY 模板
└── .extraction-state.json # 提取游标（talk to disk）
```

系统提示词里注入了这份说明（`LoadMemoryPrompt`，agent.go 的 `buildSystemPrompt`），
模型因此知道可以**自己**往当日日志追加条目。这一直与另一套结构化记忆
（`memory_expanded.go` 的 Semantic Memory Store）并存，职责有重叠，待收敛。

## 2. 记忆流水线（回合结束时）

`Agent.flushMemory(messages, chatFn)` 是唯一入口，两个阶段必须按此顺序：

1. **`ExtractMemories`（轻量，同步，绝不跳过）**
   只扫 User 消息里的显式记忆请求（remember / 记住 / don't forget / 别忘了），
   append 到当日日志。
   **增量**：只处理 `lastSummarizedMsgUUID` 之后的消息。历史实现是全量扫，
   于是同一句"记住 X"会在每个回合结束被反复追加（已修）。
2. **`ExtractSessionMemory`（重量，阈值触发，异步）**
   `ShouldExtractMemory` 判断：1 万 token 初始化 → 之后每 5000 token 增长
   （或工具调用达阈值）触发；末轮无工具调用才认为安全。
   命中后 **goroutine** 里 fork 一次对话让模型重写 SESSION_MEMORY.md。
   异步是因为它要几秒，不能堵住用户的下一句。

顺序不可交换：阶段 2 一旦move cursor，阶段 1 的增量窗口就会被“后来的”
游标吃掉一部分。

## 3. 为什么要有状态持久化

`Service.AgentRunAdvanced` **每条用户消息都 `agent.NewAgent`**，MemorySystem
随之重建。没有持久化的话 `tokensAtLastExtraction` 每次归零，“每 5000 token 触发”
永远不会成立 → 自动提取彻底失灵。

`.extraction-state.json` 存 `initialized / tokensAtLastExtraction /
lastSummarizedMsgUUID / consecutiveFailures`，构造时恢复。
`saveStateLocked` 会自建目录（否则静默降级成"每回合都触发"）。

**消息必须有 UUID**：主循环给 user/assistant/tool/clarify 消息都赋了
`uuid.NewString()`，增量窗口依赖它。

## 4. 提取为什么由我们来写文件

`BuildSessionMemoryUpdatePrompt` 原先让模型"用 file_edit 工具更新文件"，
但接入点的 `chatFn` 是纯聊天函数（无工具），而 `ExtractSessionMemory` 又把返回值
丢弃（`_, err = chatFn(...)`）——接上了也是空转。

现在改为：prompt 要求输出单个 `<updated_notes>` 块 → `writeSessionMemoryFromOutput`
提取并写盘，写前 `checkNotesStructure` 校验模板骨架（章节标题）是否完整，
不完整就拒绝写入（保留旧文件）。避免模型一次抖手把笔记结构毁掉。

## 5. compact 整合

`compact.CompactService` 早就预留了 `CompactConversationWithSessionMemory`。
agent 的自动压缩点（Run / RunStream）现在走 `runCompact`：

- 先看 `SessionMemoryForCompact()` 有没有真内容（未篡改过的模板不算）
- 有 → `CompactConversationWithSessionMemory`；没有 → 普通 `CompactConversation`

⚠️ 注意别误用 `GetSessionMemoryForCompact`：它的第二个返回值是
**wasTruncated（是否被截断）**，不是“有没有内容”。当笔记健康但未截断时它返回
false，用它当 ok 判断会恰好在最该注入时跳过注入（踩过）。

## 6. 手动入口

`Service.ExtractSessionMemoryNow(rootPath, mode)` + 前端输入框「整理记忆」按钮。
它忽略阈值，强制跑一次 `ManuallyExtractSessionMemory`（内部先
`WaitForExtraction` 防并发重写）。Service 侧缓存每个项目最后一个 agent
（`lastAgents`，上限 8）以拿到当前会话消息。

## 7. 已知取舍 / 待办

- MemorySystem 按 homeDir 单例：**多项目共用一份 SESSION_MEMORY.md**，
  并行开两个项目会互相污染。按项目隔离需要把 MemorySystem 的 key 改成
  rootPath，牵动每日日志与 MEMORY.md 的路径。
- Markdown 记忆树与 `memory_expanded.go` 的 SQLite/sqlite 结构化记忆并存。
- 提取失败有退避（`consecutiveFailures >= 3` 后不再重试），但失败不回滚游标，
  意味着一批失败的消息不会被重试 —— 与上游行为一致，取舍的是“再多议一次 LLM”。
