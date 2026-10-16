# 天蚕 · TianCan AI IDE — Release v0.3.0

> 发布日期：2026-10-17 ｜ 协议：Apache-2.0 ｜ Module：`github.com/linlurui/tiancan-ai-ide`

## 概述

v0.3.0 是面向**开源发版**的一次能力补齐版本。在 v0.2.0（开源合规与仓库规范化）基础之上，本轮（流 G · G5–G13）把 agent 内核里此前缺失或不可验证的能力逐项补齐，并**为每一阶段建立完整测试**（单测 + 集成 + 反证），以 `go test -race ./...` 全绿作为进入下一阶段的门禁。

定位不变：**纯本地、数据零上传**的 AI 编程 IDE。本轮新增能力均在此承诺范围内，不引入任何云端依赖。

## 新增能力

### 技能系统接入主循环（G5）
- 命中的技能以 `user` 消息（pre-step）注入到真实用户请求**之前**；技能正文**不进 system prompt**，保持 provider KV 缓存前缀有效。
- 注册表 + 文件系统 Provider（`ProjectDataDir/skills` 与 `rootPath/skills` 加载 `.skill.yaml`）。

### 工具守卫（G6）
- 每工具独立 deadline，区分「父 ctx 取消」（整轮中止）与「工具自身超时」（到点中止）。
- 重复 / 被拒调用（如反复 `bash rm -rf /`）重定向为「重复调用提醒」，把模型导向换策略，打破死循环。

### 上下文压缩（G7）
- 压缩区间**向前退到平衡切点**，绝不切掉半个 tool-call/result 对。
- checkpoint 携带 `source.kind`（auto / manual / partial），下游可区分压缩来源。

### 用户批准四值闭集（G8，fail-closed）
- 批准结果收敛为恰好四值：`allowed-once` / `rejected` / `cancelled` / `unavailable`。
- `never` 策略在**任何工具 dispatch 之前**决定；交互式 answerer 抛错强制收口为 `unavailable`（失败关闭）。

### 意图澄清状态机（G9）
- 显式 `open → continued/settled` 状态机；澄清一旦 settled 不再被 reopen。
- 超时 / 无应答返回 `{pending:true}`，**不是错误**；答案经 `user` 消息回灌主循环。

### 权限预设（G10）
- 预设 = `{sandbox, approval}` 可比较二元组；保留名 `auto`（系统托管默认）与 `custom`（偏离哨兵）。
- 持久化与运行时通知**仅在旋钮真变时**发生，重复同元组不写盘。

### 设置 schema + 密钥脱敏（G11）
- 声明式 schema：`role('secret')` 字段在**跨 UI 边界前**被结构化移除。
- `union` 保守合并（任一分支声明 secret 即删）；默认值一并脱敏。
- 原语：`RedactJSON` / `RedactedForUI` / `PublicSchema`。

### 目标驱动与计划模式（G12）
- 目标状态机：`revision` CAS（防并发丢失更新）+ 轮次预算 + 可复用竞态栅栏。
- 计划态靠 **system prompt 段落 + 批准 deny-all 独立执法**（非工具层拦截）；只读工具在计划态仍可执行。

### 浏览器工具（G13）
- 以纯 Go 的 **go-rod（CDP）** 替换 playwright-go，**移除 ~50MB Node 运行时**，契合「Go 单栈 / 零外部运行时」。
- **isTrusted 硬墙**：`dispatchEvent` / `new XEvent` / `.click()` 等页面 JS 合成输入一律拦截，仅真实 CDP 输入（`isTrusted=true`）放行。

## 安全加固

- 浏览器事件合成输入硬墙（G13）。
- 密钥跨 UI 边界脱敏（G11），`env.*` 等 secret 字段在发往前端口径前移除。
- 高危命令默认 deny 规则（G4）：`rm -rf /`、`mkfs`、`dd if=`、远程脚本执行、`git push --force`、`npm publish`、`chmod -R 777 /`、覆写 `/dev/sd*` 等默认拦截。
- 进程级资源护栏（G4）：`bash -c` 前插 `ulimit` 前奏（CPU / 地址空间 / 单文件 / 进程数），`TIANCAN_RLIMIT_*` 可调。

## 构建与测试

### 从源码构建
```bash
# 环境：Go >= 1.25，Node.js >= 20 LTS，Wails CLI v3 (alpha.74+)
git clone https://github.com/linlurui/tiancan-ai-ide.git
cd TianCan-AI-IDE
go mod tidy
cd frontend && npm install && cd ..
~/go/bin/wails3 generate bindings -ts -b -names ./...
go build -tags production -o tiancan-ai-ide .
```

### 测试
- 全仓 `go test -race ./...` 通过（EXIT=0）。
- v0.3.0 当前 **27 个**含测试包全 OK（较 v0.2.0 的基线显著增加，主要增量来自流 G 各阶段新增叶子包）。
- 真机 / 外部依赖测试一律 **opt-in + 优雅 SKIP**（如浏览器真机测试需 `TIANCAN_LIVE_BROWSER=1`），绝不阻塞 `go test ./...`。

## 已知限制 / 后续

- **发布到 GitHub / Gitee**：本地 tag `v0.3.0` 已打；实际推送到 `github.com/linlurui/tiancan-ai-ide`（仓库重命名 + Gitee 较新内容整体重推）需在可联网环境确认账号与仓库状态后执行（G0 遗留）。
- **浏览器自动化**依赖本机已安装的 Chrome / Chromium；`TIANCAN_BROWSER_BIN` 可显式指定，缺失时快速失败而非无限下载。
- **交互式 answerer / clarifier（UI 弹窗）**：批准与澄清的失败关闭语义已在内核建立，UI 接线随桌面端（Wails）集成落地。
- **路线 A（事件日志 + 主循环 turn/step 改造，G14/G15）**：按计划为可选，默认**未做**；本轮采用路线 B（增量补齐）直接发版。若后续需要重放 / fork / 崩溃恢复能力再启 G14/G15。

## License

Apache License 2.0 — 见仓库根目录 [LICENSE](../LICENSE)。
