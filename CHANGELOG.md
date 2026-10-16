# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/lang/zh-CN/).

## [0.3.0] - 2026-10-17

开源发版前的 agent 能力补齐（流 G · G5–G13）。每一项均带完整测试（单测 + 集成 + 反证）并以 `go test -race ./...` 全绿为门禁。

### Added
- 技能系统接入主循环（G5）：命中技能以 user 消息 pre-step 注入，正文不进 system prompt，保持 KV 缓存前缀有效。
- 目标驱动与计划模式（G12）：目标状态机（revision CAS + 轮次预算 + 竞态栅栏）；计划态经 system prompt 段落 + 批准 deny-all 独立执法，只读工具仍可探查。
- 权限预设（G10）：`{sandbox, approval}` 二元组 + 保留名 `auto`/`custom`，仅在旋钮真变时才持久化与通知运行时。
- 设置 schema + 密钥脱敏（G11）：`role('secret')` 字段在跨 UI 边界前结构化移除，`union` 保守合并，默认值一并脱敏。
- 浏览器工具（G13）：以纯 Go 的 go-rod（CDP）替换 playwright-go（移除 ~50MB Node 运行时）；页面 JS 合成输入被 isTrusted 硬墙拦截。

### Changed
- 工具守卫（G6）：每工具独立 deadline，区分「父 ctx 取消」与「自身超时」；重复/被拒调用重定向为提醒，打破死循环。
- 上下文压缩（G7）：压缩区间向前退到平衡切点，绝不切掉半个 tool-call/result 对；checkpoint 携带 `source.kind`。
- 用户批准（G8）：升级为四值闭集 `{allowed-once, rejected, cancelled, unavailable}`，`never` 策略先于任何 dispatch，`answerer` 抛错失败关闭。
- 意图澄清（G9）：显式 `open → continued/settled` 状态机；超时/无应答返回 `{pending:true}` 而非错误。

### Security
- 浏览器事件合成输入硬墙（G13）：`dispatchEvent`/`new XEvent`/`.click()` 等伪造派发一律拦截，仅真实 CDP 输入 `isTrusted=true` 放行。
- 密钥跨 UI 边界脱敏（G11）：`env.*` 等 secret 字段在发往前端前结构化移除。
- 高危命令默认 deny 规则（G4）：`rm -rf /`、`mkfs`、`dd if=`、远程脚本执行、`git push --force` 等默认拦截。

## [0.2.0] - 2026-10-16

开源合规与仓库规范化（G0 阶段）。

### Added
- 补充 `LICENSE`（Apache License 2.0），明确开源协议。
- 新增 `VERSION` 文件与 `CHANGELOG.md`，建立版本记录规范。

### Changed
- Go module 路径统一为 `github.com/linlurui/tiancan-ai-ide`（原 `github.com/rocky233/tiancan-ai-ide`，117 个文件 / 485 处替换，含 Wails 前端 bindings 目录迁移）。
- 自更新 Release API 地址改为 `repos/linlurui/tiancan-ai-ide`。
- README 克隆地址统一为小写仓库名。
- 应用当前版本号从 0.1.0 提升至 0.2.0。

### Removed
- 从版本控制中移除构建产物二进制 `tiancan-dev`（加入 `.gitignore`，不再 tracked）。

### Security
- 发版前敏感信息扫描通过：无私钥 / token / 内部域名泄露。

## [0.1.0]

首个内部可用版本：Go 1.25 + Wails3 桌面 AI IDE，含 agent 主循环、多推理源
（LM Studio / OpenAI 兼容 API / Web AI Provider / Auto Provider 路由 / Smart Router）、
MCP、沙箱设置、调试器集成等。
