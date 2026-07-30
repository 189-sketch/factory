# factory-ui 看板信息架构

视图清单与每视图展示什么,面向 Electron + Web 双模式。
遵循 R2(事件类型)、A2(信封 + 4 事件 + /api/v1 控制面)、A4(ui 后端聚合、repository 路由)、A3(后端/idle 生命周期)、B1(接入)、C2(凭据)。

## 1. 视图清单(五个)

```
┌────────────────────────────────────────────────────┐
│ 顶栏:factory logo · 后端状态(已连容器数) · 设置        │
├──────────┬─────────────────────────────────────────┤
│ 侧栏导航  │  主区(随路由切换)                         │
│ ▸ 总览    │                                         │
│ ▸ 仓库    │                                         │
│ ▸ 后端    │                                         │
│ ▸ 凭据    │                                         │
│ ▸ 设置    │                                         │
└──────────┴─────────────────────────────────────────┘
```

| 视图 | 路由 | 承载 |
| --- | --- | --- |
| 总览 | `/` | fleet 多仓卡片网格,全局健康一览 |
| 仓库(单仓详情) | `/repos/:owner/:repo` | 单仓的任务流 + 事件流 + todo list + 控制 |
| 后端 | `/backends` | 执行后端(docker/Podman/远端/microVM)与容器实例管理 |
| 凭据 | `/credentials` | 凭据的增删改查(C2 credentials 表) |
| 设置 | `/settings` | 全局默认值(idle 超时、默认后端、默认触发标签、主题/语言) |

**核心监控对象**(todo list、关键节点、任务状态机、活动日志)全部落在**单仓视图**,总览只做聚合健康。

## 2. 总览(`/`)— fleet 多仓卡片

每个 repo 一张卡片(Arco `Card`),数据源 `repo.health` 事件 + `/api/v1/status`:

```
┌──────────────────────────────┐
│ ● acme/webapp        [codex] │  ●=status 色点(ready绿/running蓝/idle灰/backoff黄/error红/offline暗)
│ running · 2 active · 3 queued│  status / active_runs / queued_tasks
│ 最近: run #128 开 PR #45      │  最近一条 run.outcome/task.state 摘要
│ 最后活动 2m 前               │  last_activity_at 相对时间
│ [进入] [暂停] [销毁]          │  控制入口(见 §5)
└──────────────────────────────┘
```

- 顶部全局条:容器总数、running 数、错误数。
- 右上「+ 添加仓库」按钮 → 弹出 B1 接入对话框。
- 卡片按 status 排序(error/running 在前),空态引导接入第一个仓库。

## 3. 仓库详情(`/repos/:owner/:repo`)— 核心监控视图

三栏布局(Arco `Layout`),这是看板的心脏:

```
┌───────────────────────────────────────────────────────────┐
│ acme/webapp [codex]  ●running   [触发接入] [暂停] [销毁]     │ 头部:身份+状态+仓级控制
├──────────────┬──────────────────────────────┬─────────────┤
│ 任务列表      │  活动流 / todo list           │  详情面板    │
│ (task.state) │  (run.activity)              │ (选中项)    │
│              │                              │             │
│ #56 实现登录  │  ▸ 关键节点时间线             │ run #128    │
│  ●running    │   ● 认领任务                  │ outcome: —  │
│ #55 修导航   │   ● 工作区就绪                │ PR #45      │
│  ✓succeeded  │   ● 代码完成                  │ 耗时 38m    │
│ #54 找bug    │   ● reviewer 通过             │ [取消]      │
│  ✗failed     │   ● PR #45 已开 ✓            │             │
│              │  ─────────────                │             │
│              │  ▸ 实时活动日志(虚拟滚动)      │             │
└──────────────┴──────────────────────────────┴─────────────┘
```

- **左栏·任务列表**:该仓所有 task(`task.state` 驱动),按更新时间倒序;状态图标(queued/running/succeeded/failed/cancelled 五色)。选中某 task → 中右栏聚焦它。
- **中栏·活动流 / todo list**(选中 task 的最新 run):
  - 上半:**关键节点时间线**——从 `run.activity`/`run.outcome` 提炼的里程碑(认领→工作区就绪→代码完成→reviewer→开 PR→终态),Arco `Timeline`。
  - 下半:**实时活动日志**——`run.activity` 增量,`react-virtuoso` 虚拟滚动(高频),新事件自动滚到底(可锁定)。
  - **todo list 呈现**:agent 的待办/进展作为活动流的结构化部分提炼展示(见 D2;若现有 `run.activity` 不足以结构化,可能需 core 增补 todo 事件——D2 判定)。
- **右栏·详情面板**:选中 task/run 的元数据(outcome、PR 链接、耗时、attempt、错误摘要),+ run 级控制(取消)。

## 4. 后端(`/backends`)与凭据(`/credentials`)

- **后端视图**:执行后端列表(类型/地址/状态),每后端下的容器实例(容器 id、repo、状态、端口、启动时长),容器级操作(查看日志/重启/销毁)。数据源 docker API(A3/A4)。
- **凭据视图**:credentials 表列表(kind 图标 + label + 掩码 + scope_hint),新增/编辑/删除(删除前检查被引用);表单按 kind 分(codex/claude/GitHub App)。永不回显明文(C2)。

## 5. 控制入口落点(呼应 A2 控制面)

| 操作 | 落点 | 后端路由 |
| --- | --- | --- |
| 添加仓库(接入) | 总览「+ 添加仓库」 | `POST /ui/api/onboard` |
| 取消 run | 仓库详情右栏 / 任务列表项 | `POST /ui/api/{repo}/runs/{id}/cancel` |
| 触发接入评估 | 仓库详情头「触发接入」 | `POST /ui/api/{repo}/onboard` |
| 暂停/恢复仓 | 总览卡片 / 详情头 | ui 编排层(停 SSE+暂停容器) |
| 销毁容器 | 总览卡片 / 后端视图 | ui 编排层(A3 idle/手动销毁) |
| 凭据 CRUD | 凭据视图 | ui 后端 credentials 存储 |

仓级与容器级操作经 ui 编排层(A3/A4),run 级操作经 core `/api/v1`(A2),权限分层清晰。

## 6. 技术映射(React 19 + Arco,双模式共享)

- **组件库**:Arco Design(`Layout/Card/Timeline/Table/Form/Modal/Tag`),图标 `@icon-park/react`。
- **路由**:react-router-dom 7,五视图对应五路由。
- **状态**:SWR(HTTP 数据:tasks/runs/status/credentials)+ Zustand(SSE 事件派生的实时 store),分工见 D2。
- **虚拟滚动**:react-virtuoso(活动日志、长任务列表)。
- **双模式共享**:渲染层一份代码;Electron 模式经 `ipc`/`127.0.0.1` 连内嵌 Express,Web 模式连独立后端(A4 §6)。构建照 AionUi:`packages/desktop`(electron-vite)+ `packages/web-host`(Express)。
- **i18n/主题**:i18next(中/英),Arco 明暗主题,设置视图切换。

## 7. 关键设计取舍

- **单仓视图是唯一"重"视图**:把 todo list/关键节点/活动日志集中在它,总览保持轻(纯健康聚合),避免总览信息过载。
- **关键节点从现有事件提炼,不新增 core 事件类型**(除 D2 判定 todo 需增补外),保持 A2 的 4 事件最小集与 additive-only。
- **仓级操作走 ui 编排、run 级走 core API**:这条分界与"ui 管 docker 生命周期、core 管容器内执行"的形态②一致,权限不越界。
