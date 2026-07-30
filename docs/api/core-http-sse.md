# core HTTP/SSE 接口契约

本契约定义 factory-core(容器内)对 factory-ui 暴露的事件流(SSE)与控制面(HTTP)。
传输与演进规则遵循 R2 决策(issue #4),游标语义为本契约新增的 G1 决策。
schema 唯一事实源在 `schema/events/*.json`,ui 用 `json-schema-to-typescript` 生成 TS 类型,core 用 `jsonschema` crate 校验出站。

## 0. 总则

- core 在容器内绑 `0.0.0.0`(不是 `127.0.0.1`),端口固定 `PORT`(默认 `7788`,可被 env `FACTORY_PORT` 覆盖)。
- 所有外发文本(尤其 `run.activity`、`run.outcome` 的 result/error/message)必须经 `sanitize_for_storage`(inspection.rs:604)脱敏 + 有界截断,与账本落库同一口径,杜绝事件流成为凭据外带旁路。
- 全部响应 `Cache-Control: no-cache`。
- ui 必须容忍未知字段与未知事件类型(additive-only)。

## 1. 游标语义(G1)

- core 账本新增 `events` 表:`event_id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, ts TEXT, repository TEXT, task_id INTEGER, run_id INTEGER, payload TEXT`。
- 所有事件 append 时分配**全局单调 `event_id`**,写入信封 `seq` 与 SSE `id:`。
- 补发游标只用全局 `event_id`;`run.activity` 的 per-run `sequence` 仅在 payload 内供 run 内排序,不参与补发。
- 持久化:容器重启后 ui 仍可用 `Last-Event-ID` 补断线缺口(契合 idle snapshot/恢复语义)。

## 2. 事件端点(SSE)

`GET /events`

- 响应:`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`。
- 每条事件:
  - `id: <event_id>`(全局游标)
  - `event: <type>`(如 `task.state`)
  - `data: <envelope JSON>`(符合 `schema/events/envelope.json`)
  - `retry: <ms>`(建议 3000,指导 EventSource 重连间隔)
- 心跳:无事件时每 15-30s 发一行注释 `:keep-alive`,防中间设备掐断空闲连接。
- 断点补发:
  - 请求头 `Last-Event-ID: N` 或查询参数 `?last_id=N` → core 先按 `event_id > N` 顺序补发缺口,再转实时流。
  - 查询参数 `?v=<信封版本>` → 能力握手:ui 版本低于 core 且 core 已升 v 时,core 在流开头先发一个 `error`/`unsupported` 事件告知升级,而非静默发不可解析结构。

## 3. 首批事件类型

| type | payload schema | 源 | 频率 |
| --- | --- | --- | --- |
| `task.state` | `schema/events/task-state.json` | 账本 `TaskState` 原子跃迁 | 低 |
| `run.activity` | `schema/events/run-activity.json` | `RuntimeObservation` watch channel | 中-高 |
| `run.outcome` | `schema/events/run-outcome.json` | 账本 `RunOutcome` 终态 | 低 |
| `repo.health` | `schema/events/repo-health.json` | core 仓库健康聚合 | 周期 + 变更 |

`run.activity` 挂载点:`spawn_run_monitor`(daemon.rs:3395)消费 `observations.changed()` 处,新增一个 SSE 订阅者扇出(复用现有 watch channel,不引入新事件总线)。

## 4. 控制面端点(HTTP,ui → core)

统一前缀 `/api/v1`。除探活外均需鉴权(见 §5)。写操作幂等:重复提交同 client_request_id 不重复生效。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/health` | 探活,返回 `{status, version, repository}`;ui idle 计时与容器发现用,可匿名 |
| GET | `/api/v1/tasks` | 列任务(支持 `?state=` 过滤) |
| GET | `/api/v1/tasks/{id}` | 任务详情 |
| GET | `/api/v1/runs` | 列运行(支持 `?task_id=` 过滤) |
| GET | `/api/v1/runs/{id}` | 运行详情(含容器/沙箱元数据) |
| POST | `/api/v1/runs/{id}/cancel` | 取消运行中 run,体 `{client_request_id}`;已终结则幂等返回现状 |
| POST | `/api/v1/onboard` | 触发工单接入评估(跑一次 source 调度),体 `{client_request_id}` |
| GET | `/api/v1/status` | 仓库概览(等价 `repo.health` 当前快照) |

响应错误统一 `{error: {code, message}}`,`message` 同样脱敏。

## 5. 鉴权

- 每容器一个**接入令牌**(bearer):ui 拉起容器时经 env `FACTORY_API_TOKEN` 注入(同 C2 凭据注入链路),ui 调 core 时带 `Authorization: Bearer <token>`。
- `/api/v1/health` 与 `/events` 可用同一令牌;`/events` 支持 `?token=`(EventSource 不能自定义头)。
- 令牌容器级、随容器销毁失效,不跨容器复用 → 呼应 R1"按调用方鉴权",避免网络可达即可用。

## 6. schema 校验挂载点

- core 出站:在 SSE 扇出前,用 `jsonschema` crate 按信封 + 对应 payload schema 校验;校验失败记日志并丢弃该事件(不中断流),防止坏事件打到 ui。
- ui 入站:`json-schema-to-typescript` 生成类型 + 运行时 `zod` 宽松校验(容忍未知字段)。

## 7. 演进

遵循 R2 §4.3-4.4:additive-only 为主;破坏性变更走"新增字段 → 新事件类型 → 信封 v+1 并行期"阶梯,绝不原地破坏。
