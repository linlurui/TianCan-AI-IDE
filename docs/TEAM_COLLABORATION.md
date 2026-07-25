# 春蚕 AI IDE 团队协作系统设计

> 设计版本: v1.0  
> 创建时间: 2026-07-25  
> 状态: 设计中

---

## 1. 设计目标

在 AI 时代，团队协作的本质从「人与人协作」转变为「AI 与 AI 协作」。传统 Git PR 审核流程对 AI 来说是不必要的开销——AI 完成的功能应该直接提交到 Git。

本系统旨在打造**轻量级、去中心化的 AI 原生协作网络**，让不同开发者的 AI Agent 能够：

- 共享项目上下文和记忆
- 自动发现彼此的能力
- 高效协同完成任务

---

## 2. 核心功能模块

### 2.1 账号体系 - 春蚕账号打通

**目标**: 与 ccait.cn 账号体系统一，实现跨平台身份认证。

#### 功能点

| 功能 | 优先级 | 说明 |
|------|--------|------|
| OAuth2 登录 | P0 | 通过 ccait.cn API 实现第三方登录 |
| Token 管理 | P0 | 登录 Token 存储与自动刷新 |
| 用户信息同步 | P1 | 昵称、头像、项目列表同步 |
| 多账号切换 | P2 | 支持多个春蚕账号快速切换 |

#### 技术方案

```
天蚕IDE ←→ ccait.cn API
     ↓
  OAuth2 / JWT Token
     ↓
  用户信息缓存本地
```

#### API 接口（待确认）

```
POST /api/auth/login          - 登录
POST /api/auth/refresh        - 刷新 Token
GET  /api/user/profile        - 获取用户信息
POST /api/auth/logout         - 登出
```

---

### 2.2 项目邀请系统

**目标**: 简化团队加入流程，一键邀请成员。

#### 功能点

| 功能 | 优先级 | 说明 |
|------|--------|------|
| 生成邀请链接 | P0 | 生成带唯一邀请码的 URL |
| 邀请码解析 | P0 | 支持 `tiancan://join?project=xxx&code=abc123` Deep Link |
| 项目成员管理 | P0 | Owner / Member 角色区分 |
| 成员权限控制 | P1 | Owner 可踢人、设置权限 |
| 项目列表同步 | P1 | 跨设备同步用户参与的项目 |

#### 邀请链接格式

```
tiancan://join?project={project_id}&code={invite_code}
```

#### 邀请码生成规则

- 6 位字母数字组合
- 24 小时有效期
- 单次使用后失效

---

### 2.3 代码级记忆工程

**目标**: 基于 agentmemory 项目的设计，实现项目级上下文在团队成员间共享。

#### 功能点

| 功能 | 优先级 | 说明 |
|------|--------|------|
| 项目记忆标记 | P0 | AI 可以标记"这条记忆需要共享给团队" |
| 记忆同步 | P0 | 自动将标记的记忆同步到云端 |
| 记忆获取 | P0 | 从云端获取其他成员的共享记忆 |
| 上下文融合 | P1 | 自动将团队记忆融入当前 Agent 上下文 |
| 选择性同步 | P2 | 按项目/标签选择性同步记忆 |

#### 记忆类型

| 类型 | 说明 | 示例 |
|------|------|------|
| `architecture` | 架构决策 | "我们选择 Postgres 而不是 MySQL 因为..." |
| `solution` | 技术方案 | "这个 bug 用 XX 方案解决的" |
| `pattern` | 代码模式 | "我们的 API 响应格式统一用 Result<T>" |
| `task` | 任务状态 | "登录模块由 @Alice 实现，预计周五完成" |
| `observation` | 观察记录 | "发现某 API 在高并发下有性能问题" |

#### 技术方案（参考 agentmemory mesh 模块）

```
┌─────────┐         ┌─────────────┐         ┌─────────┐
│ IDE A   │ ←──────→│ 春蚕云端    │ ←──────→│ IDE B   │
│ Agent   │  同步    │ 记忆库      │  同步    │ Agent   │
└─────────┘         └─────────────┘         └─────────┘
                          ↑
                    项目级上下文共享
```

#### 核心 API

```
POST /api/memory/share          - 分享记忆到项目
GET  /api/memory/project/{id}   - 获取项目共享记忆
POST /api/memory/sync           - 双向同步记忆
GET  /api/memory/search         - 搜索团队记忆
```

#### LWW (Last-Write-Wins) 合并策略

当多个 Agent 同时修改同一记忆时，采用时间戳合并：
- 保留最新时间戳的版本
- 旧版本作为历史记录保留

---

### 2.4 AI 能力发现网络

**目标**: 构建团队 Agent 能力注册表，让任务能自动路由到最合适的 AI。

#### 功能点

| 功能 | 优先级 | 说明 |
|------|--------|------|
| 能力注册 | P0 | AI 启动时自动注册自己擅长的领域 |
| 能力发现 | P0 | 根据任务需求查找匹配的 AI |
| 任务委托 | P1 | 将子任务委托给其他 AI |
| 状态追踪 | P1 | 实时知道每个 AI 在做什么 |
| 能力学习 | P2 | AI 从完成的任务中学习新能力 |

#### Agent 能力注册表

```json
{
  "agentId": "user-a-claude-code",
  "owner": "user-a",
  "name": "用户A 的 Claude Code",
  "model": "claude-sonnet-4",
  "capabilities": [
    {
      "domain": "architecture",
      "level": 5,
      "description": "擅长复杂系统架构设计"
    },
    {
      "domain": "python",
      "level": 5,
      "description": "Python 全栈开发"
    },
    {
      "domain": "refactor",
      "level": 4,
      "description": "代码重构与优化"
    }
  ],
  "status": "busy",  // idle / busy / offline
  "currentTask": "实现用户认证模块",
  "updatedAt": "2026-07-25T10:00:00Z"
}
```

#### 能力级别定义

| 级别 | 说明 |
|------|------|
| 1 | 了解基本概念 |
| 2 | 能完成简单任务 |
| 3 | 能独立完成任务 |
| 4 | 熟练，能处理复杂情况 |
| 5 | 专家级，能指导他人 |

#### 核心 API

```
POST /api/agent/register        - 注册 Agent 能力
POST /api/agent/heartbeat       - 定期心跳，更新状态
GET  /api/agent/find            - 查找匹配的 Agent
POST /api/agent/delegate        - 委托任务给其他 Agent
GET  /api/agent/list            - 列出团队所有 Agent
```

---

## 3. 数据模型

### 3.1 用户 (User)

```go
type User struct {
    ID        string    `json:"id"`
    Nickname  string    `json:"nickname"`
    Avatar    string    `json:"avatar"`
    Email     string    `json:"email"`
    CreatedAt time.Time `json:"createdAt"`
}
```

### 3.2 项目 (Project)

```go
type Project struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    Description string    `json:"description"`
    OwnerID     string    `json:"ownerId"`
    Members     []Member  `json:"members"`
    InviteCode  string    `json:"inviteCode,omitempty"`
    CreatedAt   time.Time `json:"createdAt"`
}

type Member struct {
    UserID    string    `json:"userId"`
    Role      string    `json:"role"` // owner, admin, member
    JoinedAt  time.Time `json:"joinedAt"`
}
```

### 3.3 共享记忆 (SharedMemory)

```go
type SharedMemory struct {
    ID        string      `json:"id"`
    ProjectID string      `json:"projectId"`
    Type      string      `json:"type"` // architecture, solution, pattern, task, observation
    Title     string      `json:"title"`
    Content   string      `json:"content"`
    Tags      []string    `json:"tags"`
    AuthorID  string      `json:"authorId"`
    AuthorAI  string      `json:"authorAi"` // Agent 标识
    CreatedAt time.Time   `json:"createdAt"`
    UpdatedAt time.Time   `json:"updatedAt"`
}
```

### 3.4 Agent 能力 (AgentCapability)

```go
type AgentCapability struct {
    AgentID      string           `json:"agentId"`
    OwnerID      string           `json:"ownerId"`
    Name         string           `json:"name"`
    Model        string           `json:"model"`
    Capabilities []CapabilityItem `json:"capabilities"`
    Status       string           `json:"status"` // idle, busy, offline
    CurrentTask  string           `json:"currentTask,omitempty"`
    UpdatedAt    time.Time       `json:"updatedAt"`
}

type CapabilityItem struct {
    Domain      string `json:"domain"`
    Level       int    `json:"level"` // 1-5
    Description string `json:"description"`
}
```

---

## 4. 技术架构

### 4.1 系统架构图

```
┌─────────────────────────────────────────────────────────────────────┐
│                            天蚕 IDE                                  │
├──────────────┬──────────────┬───────────────┬──────────────────────┤
│   前端 UI    │   AI Agent   │   本地记忆    │     协作模块         │
│   (React)   │   (Go/TAOR)  │  (已有)       │   (新增)             │
├──────────────┴──────────────┴───────────────┴──────────────────────┤
│                          春蚕云服务                                  │
├──────────────┬──────────────┬───────────────┬──────────────────────┤
│   账号系统   │   项目管理   │   记忆同步    │    Agent 发现        │
│  (ccait.cn)  │  + 邀请码   │   (新)        │     (新)             │
└──────────────┴──────────────┴───────────────┴──────────────────────┘
```

### 4.2 模块依赖

```
协作模块 (backend/collaboration/)
├── auth/           - 账号认证 (依赖 ccait.cn API)
├── project/        - 项目管理 (依赖 auth)
├── memory/         - 记忆同步 (依赖 project)
└── discovery/      - Agent 发现 (依赖 auth)
```

### 4.3 前端页面

```
frontend/src/
├── pages/
│   └── Team/                    - 新增团队页面
│       ├── Login.tsx            - 登录页面
│       ├── Projects.tsx         - 项目列表
│       ├── ProjectDetail.tsx    - 项目详情
│       ├── Members.tsx          - 成员管理
│       └── AgentRegistry.tsx    - Agent 能力注册表
├── components/
│   ├── TeamBadge.tsx            - 团队状态徽章
│   └── AgentCard.tsx            - Agent 能力卡片
└── hooks/
    └── useCollaboration.ts       - 协作状态 Hook
```

---

## 5. 实现计划

### Phase 1: 基础架构 (P0)

- [ ] 春蚕账号登录/登出
- [ ] 项目创建与邀请链接
- [ ] Deep Link 解析 (tiancan://join)
- [ ] 基础 API 服务端点

### Phase 2: 记忆同步 (P0)

- [ ] 记忆标记与分享
- [ ] 云端记忆存储
- [ ] 记忆同步客户端
- [ ] 上下文融合

### Phase 3: Agent 发现 (P1)

- [ ] Agent 能力注册
- [ ] 能力匹配算法
- [ ] 任务委托机制
- [ ] 状态追踪

### Phase 4: 高级功能 (P2)

- [ ] 选择性记忆同步
- [ ] AI 能力学习
- [ ] 离线支持
- [ ] 冲突解决优化

---

## 6. 参考项目

- [agentmemory](https://github.com/rohitg00/agentmemory) - AI Agent 持久化记忆系统
  - 参考其 `team` 和 `mesh` 模块设计
  - 采用类似的 LWW 合并策略

---

## 7. 待确认事项

1. ccait.cn API 接口规范是否已确定？
2. 是否需要支持自建春蚕服务（不依赖 ccait.cn）？
3. 记忆存储是否使用 ccait.cn 的现有服务还是独立存储？
4. 是否需要支持 GitHub / GitLab 账号登录？

---

## 8. 设计变更记录

| 日期 | 版本 | 变更内容 |
|------|------|----------|
| 2026-07-25 | v1.0 | 初始版本 |
