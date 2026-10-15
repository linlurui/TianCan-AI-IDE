# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/lang/zh-CN/).

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
