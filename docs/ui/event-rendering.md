# 事件流的 ui 落地与渲染(缓存 / 重连 / 状态派生)

core SSE 事件在 ui 的订阅、缓存、断线补发与视图映射。
遵循 R2(SSE + Last-Event-ID)、A2(G1 全局游标 + 4 事件 + 信封)、A4(后端聚合 + 渲染层单连接 + 游标分仓)、D1(五视图 + Arco + SWR/Zustand + virtuoso)。

## 1. 客户端连接策略:渲染层单连接,经 ui 后端聚合

落定 A4 §1:渲染层**不直连容器**,只跟 ui 后端建**一条** SSE。

- 渲染层用原生 `EventSource` 连 `GET /ui/events`(ui 后端聚合 N 容器后的总流,A4 §3)。
- **React 集成**:封装 `useFactoryEvents` hook。在 `useEffect` 里 `new EventSource(...)`,cleanup 里 `close()`——StrictMode 双挂载下必须这样,避免重复连接(R2 警示)。
- 后端聚合流的事件信封已是全局设计(含 `repository`),渲染层按 `repository` 分桶到各仓视图。
- 控制指令走 `/ui/api/*`(SWR mutation),不经 SSE。

## 2. 断线重连与补发:游标分仓(A4 硬约束)

两层重连,游标都是 **per-repo**(每容器 core 的 event_id 空间独立):

**(a) 渲染层 ↔ ui 后端那条 SSE**:
- `EventSource` 自动重连(约 3s,可经 `retry:` 调),自动带 `Last-Event-ID`。
- 但渲染层游标是**后端聚合流**的序号,不是 core 的 event_id。后端为聚合流维护自己的单调序号(见 §3),渲染层断开重连由后端补发聚合流缺口。
- 渲染层额外在内存(可选 localStorage)记每仓 `last_event_id`,仅作展示层去重兜底,不作补发权威。

**(b) ui 后端 ↔ 各容器 core 的 N 条 SSE**:
- 后端为每仓记 `last_event_id[repository]`(持久化到后端 SQLite,跨后端重启保留)。
- 某仓容器 SSE 断开 → 后端按该仓 `last_event_id` 用 `Last-Event-ID` 补发重连(指数退避),**按 repository 分别记忆,不跨仓混用**(A4 §3 硬约束)。
- 容器 idle 销毁 → 该仓游标归档;重建容器恢复时,core 账本 events 表仍在(数据卷复用,B1 §4),后端从归档游标续补。

## 3. 本地缓存与状态派生:SWR(HTTP)+ Zustand(SSE 实时)

**分工**(D1 §6):
- **SWR**:一次性/低频 HTTP 数据——`/ui/api/*` 的 tasks、runs、status、credentials、backends。用于首屏与按需刷新。
- **Zustand**:SSE 驱动的**实时派生 store**。后端聚合流的事件进一个 `useRealtimeStore`,按事件类型增量更新。

**Zustand store 形状**(按仓分桶):

```ts
{
  repos: {
    [repository: string]: {
      health: RepoHealth,            // repo.health 事件覆盖
      tasks: Record<taskId, TaskState>,   // task.state 事件 upsert
      activities: Record<runId, Activity[]>, // run.activity 事件 append(环形上限)
      outcomes: Record<runId, RunOutcome>,   // run.outcome 事件 set
      lastEventId: number,           // 该仓游标(展示层)
    }
  }
}
```

**派生规则**:
- `repo.health` → 覆盖该仓 `health`(总览卡片 + 详情头)。
- `task.state` → upsert `tasks[task_id]`(左栏任务列表,按状态五色)。
- `run.activity` → append 到 `activities[run_id]`(中栏活动日志),**环形缓冲上限**(如每 run 保留最近 500 条,防内存膨胀;`truncated` 标记提示截断)。
- `run.outcome` → set `outcomes[run_id]`(右栏详情 + 关键节点时间线终态)。
- ui 合成的 `repo.health{offline}`(A4 §5)→ 该仓卡片/详情置灰。

**SWR 与 SSE 的衔接**:SSE 是实时主源;SWR 在**首屏**与**SSE 重连补发后**做一次对账(fetch 全量校正 store),防长时间运行后漂移。冲突时以 SSE 事件为准(它带全局游标,有序)。

## 4. 渲染映射:4 事件 → D1 视图组件

| 事件 | store 落点 | 驱动组件(D1) |
| --- | --- | --- |
| `repo.health` | `repos[r].health` | 总览卡片色点/计数;详情头状态;`offline` 置灰 |
| `task.state` | `repos[r].tasks` | 详情左栏任务列表(五色图标、选中态) |
| `run.activity` | `repos[r].activities` | 详情中栏实时活动日志(virtuoso)+ 关键节点提炼 + todo list |
| `run.outcome` | `repos[r].outcomes` | 详情右栏(outcome/PR/耗时)+ 中栏时间线终态 |

**高频 `run.activity` 的节流/虚拟化**:
- 后端已做短窗口批量下发(A4 §3)。
- 渲染层 `react-virtuoso` 虚拟滚动,只渲染可视行;新事件用 `requestAnimationFrame` 合批 flush 进 store(避免高频 setState 抖动)。
- 自动滚到底,用户上翻则锁定(virtuoso `followOutput`)。

## 5. todo list 与关键节点的呈现(核心监控对象)

**关键节点时间线**:从 `run.activity` 的 `activity` 文本 + `run.outcome` 提炼里程碑。core 的 `safe_activity_summary` 已把 Codex 活动提炼成人类可读摘要,前端按摘要类型/关键词映射为时间线节点(认领/工作区就绪/代码完成/reviewer/开 PR/终态)。这是**纯前端提炼,不改 core**。

**todo list 判定**:
- 若 agent 的待办/进展能从 `run.activity` 的结构化摘要(或 activity 文本的稳定模式)可靠提炼 → 前端解析展示,**不新增 core 事件**(保持 A2 的 4 事件最小集)。
- 若现有 `run.activity` 不足以结构化表达 todo(例如需要明确的"待办项新增/完成"信号)→ **additive 增补**一个 `run.todo` 事件类型(payload:`{items: [{id, text, status}]}`,additive-only,不动信封 v)。这符合 R2 的演进阶梯,是首选项之外的备选。
- **判定动作**:实现 D2 时先验证 `run.activity` 能否稳定提炼 todo;不能则按 additive 增补 `run.todo`,并同步在 `schema/events/` 加一份 schema。本契约不预设结论,把判定留给实现期对真实 Codex/Claude 活动流的验证。

## 6. 实现要点清单

- `useFactoryEvents` hook:EventSource 生命周期 + StrictMode cleanup + 错误分类。
- 后端聚合层:per-repo 游标持久化(SQLite)、补发重连、批量下发、聚合流自身序号。
- Zustand store:按仓分桶、环形活动缓冲、rAF 合批。
- SWR:首屏 + 补发后对账。
- 组件:任务列表 / 活动时间线 / 虚拟活动日志 / 详情面板(D1 已定布局)。
- schema 类型:`json-schema-to-typescript` 从 `schema/events/*.json` 生成 TS,运行时 zod 宽松校验(A2 §6)。
