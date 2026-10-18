# 浏览器测试（原 Playwright 脚本面板）重设计

## 1. 背景与问题

产品自身的自动化测试（`go test ./backend/...` + `tsc --noEmit` + `npm run build`）
从未使用 Playwright；且底层浏览器驱动已从 playwright-go 全面迁移到纯 Go 的
go-rod（CDP），理由是不打包 Node 运行时、不依赖驱动安装。

但 IDE 里的「测试 → Playwright 脚本」页面仍停留在旧路线：

- 用户必须先点 Install Playwright：npm 装包 + 下载 Chromium（数百 MB）
- 脚本只存后端内存 map，重启即全部丢失
- 裸 textarea 编辑，无行号/高亮/补全
- 与项目不集成：不能针对当前项目跑，输出不进任何可复用资产
- 与 sandbox 服务里的 playwright 安装检测重复

## 2. 方案

**「浏览器测试」**：步骤化（step-based）的浏览器自动化测试，驱动用项目内
共享的 go-rod 封装 `backend/browser`（`ResolveBin` + `Launch`），跑在本机
已安装的 Chrome/Chromium 上——零安装、零额外运行时。

### 2.1 数据模型（backend/autotest/script.go）

一个测试 = 一个 JSON 文件，落在 `<项目>/.tiancan/tests/<id>.json`（未打开
项目时落全局数据目录），可提交进 git，也可交给 agent 生成/修改。

步骤类型与字段：

| type | 字段 | 语义 |
| --- | --- | --- |
| open | url | 导航并等待主文档加载 |
| click | selector | 点击元素 |
| fill | selector + text | 填写输入框 |
| assertText | selector + text | 元素文本必须包含 text |
| assertTitle | text | 页面标题必须包含 text |
| screenshot | name? | 整页截图存 screenshots/ |
| wait | ms 或 selector | 固定等待或等待元素出现 |

每步可设 `timeoutMs`（默认 15s）。保存与运行前都过 `Validate`。

### 2.2 执行器（backend/autotest/runner.go + cdp.go + browser_launch.go）

- 浏览器启动用 rod 的 launcher（只借它挑 Chromium flags 和 exec 进程），
  **必须带 `--no-sandbox --disable-gpu --use-mock-keychain`**：缺了这三个，
  Chrome 会在打印 DevTools 行之后立刻崩溃退出（sandbox initialization
  failed / GPU process isn't usable），客户端拿到的是秒断连接——症状是
  `Target.createTarget` 都正常、下一步 EOF，极具误导性。
- 协议层是**自建的最小 CDP 客户端**（cdp.go，coder/websocket）：browser 级
  WebSocket + JSON-RPC + flat session（sessionId 路由到标签页）。
  为什么不用 rod 高层：rod v0.116.2（2024 年，已停更）与 2026 年的 Chrome
  不兼容（`PageFromTarget` 内部路径会让 Chrome 静默关闭 WebSocket），
  而协议本身完全正常（Python websockets 逐条验证过）。
- 每步绑定 ctx 与超时：ctx 取消（StopRun）即杀浏览器，超时不泄漏到后续步骤
- DOM 操作全部走 `Runtime.evaluate`（等待元素、点击、填写、断言文本/标题），
  截图走 `Page.captureScreenshot`；打开页面后轮询 `document.readyState`
- 失败即停，返回带步骤号的错误；进度以 `autotest:output` 事件流式推给前端
  （step/log/error/done，含每步耗时与截图文件名）
- 截图文件名经 `safeName` 过滤（只留 `[A-Za-z0-9_-]`），防路径穿越
- ID 校验 `^[A-Za-z0-9_-]{1,64}$`，脚本文件名不可构造穿越

### 2.3 界面（frontend/src/components/BrowserTestPanel.tsx）

- 左栏：测试列表（落盘的真实文件），可新建/删除
- 主区工具栏：名称、存储位置提示（.tiancan/tests/）、保存、运行/停止、
  步骤模式 ↔ JSON 源模式（Monaco）切换
- 步骤模式：每步一行（类型下拉 + 按类型动态渲染的字段），上移/下移/删除
- 输出区：逐步结果、耗时、截图文件名；失败步红色高亮
- 本机无浏览器时顶部警告条（提示装 Chrome 或设 `TIANCAN_BROWSER_BIN`），
  运行按钮禁用
- 运行前自动保存（后端从磁盘加载，必须先落盘）

### 2.4 清理

- 删除 `backend/playwright`、`PlaywrightPanel.tsx` 及其绑定与样式
- `sandbox.EnvStatus.PlaywrightReady` → `BrowserReady`（`browser.Available()`），
  删除沙箱内的 npm playwright 安装逻辑（无调用方）
- webai/ai 里残留的 "Playwright" 过时注释改为浏览器/go-rod 实际措辞

## 3. 边界

- 无头模式：需要看浏览器画面的场景（如手动登录）不适用，那是 webai 登录流的职责
- 登录态：测试跑在全新 profile 上，不带用户 cookie；需要登录的流程应在
  测试内完成（后续可加 cookie 注入步骤）
- 每步 15s 默认超时；慢站点在步骤上单独设 timeoutMs
- 坏 JSON 文件不阻塞测试列表（跳过并继续展示其它测试）
