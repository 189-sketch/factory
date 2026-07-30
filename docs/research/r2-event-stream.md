# R2 事件流选型研究:SSE vs WebSocket vs 长轮询

本报告解决工单 R2(https://github.com/189-sketch/factory/issues/4)。
目标是为 Factory core(跑在容器内)向 factory-ui(Electron + Web 双模式看板)暴露事件流，选定传输协议，并给出事件 schema 的版本演进策略。
本报告只做选型与契约建议，不写实现代码。

阅读前置:core 现有架构见 [../analysis.md](../analysis.md)(第四节执行链路、第五节持久化、第九节提示词分界)，账本模型见 [../../src/storage.rs](../../src/storage.rs)，活动观测模型见 [../../src/runtime.rs](../../src/runtime.rs)。

## 一、场景约束(先把约束钉死，再谈选型)

Factory 的事件流场景有一组非常具体、且对选型起决定性作用的约束。这些约束直接来自代码与设计，而不是泛泛而谈。

### 1.1 数据面单向，控制面走 HTTP(已定决策)

工单与团队已明确:控制面(UI 下发指令)走普通 HTTP，事件流是主消费模式。
这意味着事件通道只需要 server → client 单向推送。
双向通信不是需求，因此 WebSocket 最核心的卖点(全双工)在本场景用不上。
这一条几乎单独就把选型收敛到 SSE 或长轮询。

### 1.2 core 已有"观测 + 有界活动"模型，事件源是内生状态而非外部总线

core 不是从零造事件流，它已经有一套内生的观测机制，这套机制天然就是 SSE 事件源:

- `RuntimeObservation`(runtime.rs:60)是一个带 `sequence: u64` 递增序号的状态快照，通过 `tokio::sync::watch` channel 在 daemon 内部广播(runtime.rs:71 `observation_channel`)。
- 每个 run 有一个后台监控任务 `spawn_run_monitor`(daemon.rs:3395)，它消费 `observations.changed()`，把观测持久化到账本 `Ledger::observe_run`(storage.rs:2630)，并在控制台打印 worker 进度(daemon.rs:3422)。
- 活动内容是 Codex 的 JSON 活动协议，经 `capture_activity_line`(runtime.rs:766)解析、经 `safe_activity_summary`(runtime.rs:812)提炼成人类可读摘要、经 `sanitize_for_storage`(inspection.rs:604)脱敏，再被有界截断(`MAX_OBSERVED_ACTIVITY_BYTES = 64 KiB`，runtime.rs:28)。

关键认知:core 已经在用 `watch::Sender<RuntimeObservation>` 做"状态变更 → 多消费者扇出"。
新增一个 SSE 推送层，本质是给这套现成的 watch channel 再加一个订阅者(把 `RuntimeObservation` 序列化成 SSE event)，而不是引入一套新的事件总线。
WebSocket 需要额外维护一套连接会话状态机与心跳协议，收益为零，纯属过度工程。

### 1.3 事件量中等、以状态变更与日志为主

要推的事件类别就是工单里列的:活动日志、任务状态变更、todo list 进展、关键节点。
这些正好对应 core 现有的两类状态跃迁:

- 任务/运行状态机:`TaskState`(queued/running/succeeded/failed/cancelled，storage.rs:282)与 `RunOutcome`(storage.rs:558)，由原子事务在账本内跃迁(如 `claim_and_start_run` storage.rs:1995、`finish_run_and_task_with_recovery` storage.rs:2342)。
- 活动观测流:`RuntimeObservation.activity` 增量，频率受限于 Codex 事件产出，且有 `sequence` 单调递增可用于去重与断点续传。

这是"中等频率、单条不大、需要有序、需要可补发"的典型 SSE 负载，不是高频行情/游戏帧同步那种必须 WebSocket 的负载。

### 1.4 跨 docker 端口映射与反向代理

core 跑在容器内，UI(Electron 或浏览器)在容器外，事件流要穿越 docker `-p hostPort:containerPort` 端口映射，将来可能再叠一层反向代理。
这带来两个硬要求:

- 协议必须能被普通 HTTP 代理/端口映射无损转发，不能依赖 `Upgrade` 头或 hop-by-hop 协议升级。
- 连接可能长时间空闲(没事件时)，需要应用层心跳防止中间设备把"看起来空闲"的连接掐掉。

SSE 就是普通 HTTP 长响应(`Content-Type: text/event-stream`)，端口映射与代理天然兼容，只需禁掉响应缓冲(见 2.3)。
WebSocket 是 hop-by-hop 的 `Upgrade: websocket` 握手，穿越企业代理/透明代理/TLS 终止设备时经常被剥头或拒绝升级，这是它有案可查的真实故障类别。

### 1.5 断线必须能补发，且事件可去重

看板类 UI 对"断线期间的事件不能丢"有硬需求。
core 的 `RuntimeObservation.sequence` 与账本里 `runs`/`tasks` 的自增主键、状态机时间戳(`last_activity_at`、`updated_at`)，天然提供了一套全局单调游标，正好映射到 SSE 的 `Last-Event-ID` / `id:` 字段。
长轮询也能做游标，但客户端要手写整套"请求-超时-重发-去重"循环;WebSocket 则完全没有内建的断点续传语义，得自己在应用层造一套(等价于把 SSE 的 `Last-Event-ID` 重新发明一遍)。

## 二、三方案对比表

下表按工单要求的维度对比，并额外加入对本场景最关键的两列(docker/代理兼容性、与 core 现有观测模型的契合度)。

| 维度 | SSE (Server-Sent Events) | WebSocket | HTTP 长轮询 |
| --- | --- | --- | --- |
| 协议本质 | 普通 HTTP 长响应，`text/event-stream`，单向 server→client | HTTP `Upgrade` 握手后切到独立双向帧协议 | 客户端反复发 HTTP 请求，服务端挂起直到有数据或超时 |
| axum 服务端实现复杂度 | 极低。axum 原生 `Sse<Stream>`(axum::response::sse)，把 `Stream<Item = Result<Event, Infallible>>` 包进 `Sse::new(stream).keep_alive(...)` 即可。与 core 现有 `watch::channel` 直连:订阅端 `BroadcastStream`/`watch::Receiver` 转 stream，几十行 | 高。`axum::extract::ws` 要手写握手、帧编解码(tokio-tungstenite)、消息分片、ping/pong 心跳、关闭帧处理，还要为双向通道设计消息协议。本场景双向用不上，全是沉没成本 | 低-中。端点逻辑简单，但要手动实现挂起队列、超时返回、客户端侧的重试与去重，边角情况多 |
| React 客户端实现 | 极低。浏览器/Electron 原生 `EventSource`，自动重连(~3s，可用 `retry:` 字段调)、自动带 `Last-Event-ID`。封装成 `useSSE` hook(注意 StrictMode 下要在 `useEffect` cleanup 里 `close()`，避免重复连接) | 中。原生 `WebSocket` API 或库(socket.io 等)，重连、退避、心跳、断点续传全要自己写 | 高。客户端要手写"发请求→等→处理→立刻再发"循环，加指数退避、错误分类、去重游标，没有原生支持 |
| 断线重连 | 内建。`EventSource` 自动重连，服务端用 `retry:` 控制间隔 | 需自实现(指数退避 + 抖动)，库可帮一部分 | 天然每次新请求即"重连"，但客户端循环逻辑复杂 |
| 事件补发 / 断点续传 | 内建语义。客户端重连自动带 `Last-Event-ID` 头，服务端按 `id:`(单调游标)补发缺口后再发新事件。与 core 的 `sequence`/自增主键一一对应 | 无内建语义。要自己设计 session token + 服务端缓冲 + 重放(等于重造 `Last-Event-ID`) | 可实现(游标参数)，但全靠手写，且"挂起期间丢事件 vs 超时窗口"要仔细设计 |
| 穿越 docker 端口映射 | 无损。就是一条 HTTP 响应，`-p 8080:8080` 即可，注意 core 在容器内要绑 `0.0.0.0` 而非 `127.0.0.1` | 可穿越，但握手是 `Upgrade` hop-by-hop，对中间设备更敏感 | 无损，普通请求-响应 |
| 穿越反向代理 / 负载均衡 | 好。标准 HTTP，只需禁响应缓冲(nginx `X-Accel-Buffering: no` / `proxy_buffering off`)并设长读超时 | 差-中。代理必须正确转发 `Upgrade`/`Connection` 头，企业代理/透明代理/TLS 终止设备常剥头或拒升级;长连接还易被"空闲超时"掐 | 好，但每个挂起请求占用一个连接/线程，代理排队与超时配置要留意 |
| 空闲连接保活 | 内建。`KeepAlive` 周期发注释行(`:keep-alive`)防中间设备掐线 | 需应用层 ping/pong | 不需要(每次请求短) |
| HTTP/1.1 连接数限制 | 每域 ~6 个 `EventSource` 连接上限;HTTP/2 下消除(多路复用)。本场景单看板连接数少，非瓶颈 | 不受 HTTP/1.1 6 连接限制 | 挂起请求同样占连接 |
| 双向通信 | 不支持(本场景不需要) | 原生支持(本场景不需要) | 变相双向(每次新请求可带客户端数据，但低效) |
| 与 core 现有观测模型契合 | 完美。`watch::channel` + `sequence` 就是 SSE 的事件源与游标 | 一般。要把 `watch` 扇出再套一层 ws 会话管理 | 一般。轮询语义与"持久化后读最新"匹配，但失去 push 实时性 |

## 三、推荐与理由

### 3.1 明确推荐:SSE(Server-Sent Events)

给 factory-ui 的事件流用 SSE，控制面继续走普通 HTTP(既定决策不变)。
core 侧用 axum 的 `Sse` 响应，把每个 run 的 `watch::Receiver<RuntimeObservation>`(以及账本状态机跃迁)扇出为 SSE 事件。

一句话:SSE 把本场景真正需要的三件事(单向 push、断线补发、穿代理)做成了协议内建能力，而 WebSocket 把本场景用不上的双向能力做成了必须自己背的实现成本。

### 3.2 理由(按重要性排序)

1. 单向已足够，双向是负资产。
   控制面走 HTTP 后，事件通道没有任何 client→server 的实时回传需求。
   WebSocket 的全部复杂度(帧协议、心跳、会话管理、双向消息协议)都只为一个用不上的能力买单。
   SSE 恰好只做"服务端往客户端推"，不多不少。

2. 断线补发是协议内建，不是自造轮子。
   看板丢事件是不可接受的。
   SSE 的 `Last-Event-ID` 是客户端自动回传、服务端按游标补发的标准语义;core 的 `RuntimeObservation.sequence` 和账本自增主键/时间戳正好充当这个游标。
   WebSocket 要做到同等效果，必须在应用层重新发明 session token + 服务端缓冲 + 重放协议，工作量与出错面都大得多。

3. 穿 docker / 代理最省心。
   SSE 是普通 HTTP 长响应，docker `-p` 端口映射无损转发，反向代理只需禁缓冲(`X-Accel-Buffering: no`)。
   WebSocket 的 `Upgrade` hop-by-hop 握手在企业代理/透明代理/TLS 终止设备前有真实、反复的失败案例(剥 `Upgrade` 头、拒 101、掐"空闲"长连接)。
   本场景 core 在容器里、UI 在外面，将来还可能叠代理，选兼容性上限更高的那个。

4. 与 core 现有模型零阻抗。
   core 已经在用 `tokio::sync::watch` 做"状态变更扇出给多消费者"(`observation_channel`,runtime.rs:71)，后台监控任务已经在消费这套 channel 写账本、打日志。
   加 SSE 就是再挂一个订阅者，把 `RuntimeObservation` 序列化成 `Event`，几乎不动编排心脏。
   这符合 Factory"机制克制、边界清晰"的工程风格。

5. 两端实现成本都是三者最低。
   axum 原生 `Sse` + `KeepAlive` 几十行;React 侧原生 `EventSource` 自动重连、自动带 `Last-Event-ID`，包一个带 cleanup 的 `useSSE` hook 即可。
   把省下的复杂度预算留给真正难的部分(见第四节的 schema 演进)。

### 3.3 落地时的三个工程要点(选型之外的必修课)

- 心跳必须开。没事件时连接空闲，docker NAT 与代理会把"看起来死了"的连接掐掉。
  用 `KeepAlive::new().interval(...)` 周期发注释行(`:ka`)，间隔取 15-30s(小于常见代理/NAT 的空闲超时)。
- 缓冲必须关。SSE 流在 nginx 等代理后若被缓冲，"实时"会变"成批延迟"。
  core 响应头加 `Cache-Control: no-cache` 与 `X-Accel-Buffering: no`;若将来上代理，代理侧 `proxy_buffering off`。
- 绑定地址要在容器内绑 `0.0.0.0`。core 在容器内若绑 `127.0.0.1`，docker `-p` 映射后外部仍连不上;这是 SSE/Web 服务在容器里最常见的连通性坑，与协议无关但必须写进接口契约(A2 票)。

### 3.4 长轮询与 WebSocket 的准确定位(不是"选错"，是"不合适")

- 长轮询:作为 SSE 的降级兜底有其价值(极端受限网络连 SSE 都不放行时)，但它把重连、去重、游标全推给客户端手写，实时性又不如 push。本场景不主推，可作为未来的可选 fallback，不进入主契约。
- WebSocket:当且仅当未来出现"UI 要往 core 高频、低延迟回传交互事件"(如看板上的实时协同标注、流式干预指令)时再引入。届时它与 SSE 并存(ws 走双向控制面增强，SSE 仍走单向事件流)，而不是替换。当前门票范围内不引入。

## 四、事件 schema 版本演进策略

core 与 ui 独立发布，事件 schema 必须能在两端版本错位时不崩、可协商、可平滑升级。
以下策略借鉴事件驱动架构的成熟实践，并贴合 Factory"机制克制、只增不破"的风格。

### 4.1 总原则:additive-only(只增)，reader 容忍未知

这是整个演进策略的地基，两条铁律:

- 生产者(core)对既有事件只做加法:新增字段、新增事件类型，永不改既有字段的含义/类型，永不删字段，永不重命名。
- 消费者(ui)对未知一律容忍:解析事件时忽略不认识的字段、不认识的 `event:` 类型，绝不因多出字段而报错(即 JSON 反序列化必须"宽容"，serde 侧不加 `deny_unknown_fields`，TS 侧类型标注允许额外字段)。

只要这两条守住，core 发新版加字段、ui 不升级也照常工作;ui 升级认新字段、core 不升级也只是少几个字段可选渲染。两端发布顺序彻底解耦。

注意一个张力:Factory 的配置层(TOML `Config`)刻意用了 `deny_unknown_fields` 做严格校验(analysis.md 第三节)，这是"配置拥有机制"的正确姿态。
但事件流 schema 是跨进程、跨发布单元的对外契约，姿态必须相反:对外契约宽容，对内配置严格。
不要把 config 的严格习惯误搬到事件 schema 上。

### 4.2 事件信封(envelope):统一元信息 + 自由 payload

每个 SSE 事件用统一信封，把"路由/演进所需的元信息"与"具体业务负载"分开:

```jsonc
// event: <type>            <- SSE 的 event: 字段，粗粒度类型，用于路由
// id: <monotonic-cursor>   <- SSE 的 id: 字段，全局单调游标，用于 Last-Event-ID 补发
// data: {                  <- SSE 的 data: 字段，JSON 信封
  "v": 1,                    // 信封/事件结构版本(int)，见 4.3
  "type": "run.activity",    // 逻辑事件类型，与 SSE event: 一致或更细
  "seq": 12345,              // 与 id 相同的单调序号(冗余于 id，便于 ui 内部去重排序)
  "ts": 1753814400000,       // 事件产生时刻(epoch millis)
  "repository": "owner/repo",// 多仓库(fleet)下的归属维度
  "task_id": 56,             // 关联维度(可空，视事件类型)
  "run_id": 123,             // 关联维度(可空)
  "payload": { /* 该事件类型专属字段，additive-only */ }
// }
```

设计要点:

- `id:` 用全局单调游标(core 可组合 `RuntimeObservation.sequence` 与账本自增 id/时间戳生成，保证跨 run、跨重启唯一且递增)。
  ui 断线重连自动带 `Last-Event-ID`，core 据此补发缺口。
- `event:`(SSE 层)用粗粒度、稳定的类型名做路由(如 `task.state` / `run.activity` / `run.outcome` / `repo.health`);细粒度信息放 `payload`，便于不动路由就扩展。
- 事件类型名用点分层命名(`<域>.<动作>`)，新增类型就是新增一个名字，天然 additive。

### 4.3 版本协商:信封内 `v` 字段 + 能力握手，双轨但克制

不引入沉重的 schema registry,用两层轻量机制:

1. 信封级 `v` 字段(必带)。
   标记该事件结构的版本。
   演进规则:`v` 只在"不得不做破坏性变更"时 +1(见 4.4);additive 变更不动 `v`。
   绝大多数时候 `v` 恒为 1。

2. 连接建立时的能力握手(可选但推荐)。
   ui 建立 SSE 连接时在查询参数带上它支持的最高信封版本，如 `GET /events?v=1&last_id=...`。
   core 据此决定:
   - ui 版本 ≥ core:core 按自己版本发，ui 靠"容忍未知"正常工作。
   - ui 版本 < core 且 core 已升 `v`:core 要么降级发到 ui 的版本(若可无损降级)，要么在流开头先发一个 `error`/`unsupported` 事件告知 ui 升级，而不是静默发出 ui 解析不了的结构。

这样版本协商是"建立连接时一次性对齐",而不是每条事件都谈，开销为零，逻辑清晰。

### 4.4 破坏性变更的处理:能不加就不加，非加不可就并行期

additive-only 能覆盖 90% 的演进。
剩下 10% 真正的破坏性变更(必须改字段含义/类型、必须删字段)，按下面的梯子处理，优先用靠上的:

1. 首选 - 用新增替代修改:不改旧字段，新增一个语义正确的字段，旧字段标记 deprecated 但继续发。
   ui 逐步迁到新字段。
   这实际上仍是 additive。
2. 次选 - 新事件类型:若变更大到字段级修补不够，引入新 `event:` 类型(如 `run.activity.v2`)，旧类型并行发送一段时间。
   旧 ui 听旧类型，新 ui 听新类型。
3. 最后 - 信封 `v` +1 并行期:只有动到信封结构本身时才升 `v`，并给两代版本一个并行窗口(core 同时能按 `v=1` 和 `v=2` 发，靠 4.3 的握手对齐),ui 全部迁走后 core 才停发旧 `v`。

无论走哪一级，都不要原地破坏(in-place breaking):不让"同一个 `event:` + 同一个 `v`"在不同 core 版本里结构不同。
这是独立发布两端不互相打爆的根本保证。

### 4.5 对 A2 票(core HTTP/SSE 接口契约)的具体输入

把下面几条直接写进接口契约，作为 core 侧必须兑现的承诺:

- 传输:SSE，`GET /events`,`Content-Type: text/event-stream`,`Cache-Control: no-cache`,`X-Accel-Buffering: no`,core 容器内绑 `0.0.0.0`。
- 心跳:`KeepAlive` 注释行，间隔 15-30s。
- 游标:每条事件带 `id:`(全局单调);支持 `Last-Event-ID` 请求头与 `last_id` 查询参数做断点补发;补发完缺口再发实时事件。
- 信封:统一信封含 `v` / `type` / `seq` / `ts` / `repository` / 可空 `task_id`、`run_id` / `payload`;payload additive-only。
- 事件类型:首批至少 `task.state`(任务状态机跃迁)、`run.activity`(活动观测增量，对应 `RuntimeObservation.activity`)、`run.outcome`(运行终态，对应 `RunOutcome`)、`repo.health`(fleet 仓库健康)。
- 兼容:core 对所有事件 additive-only;ui 必须容忍未知字段与未知事件类型。
- 脱敏:所有经事件流外发的文本(尤其 `run.activity`、`run.outcome` 的 result/error)必须复用 core 现有 `sanitize_for_storage`(inspection.rs:604)与有界截断常量(`MAX_ACTIVITY_BYTES` 等),与账本落库同一套脱敏口径，避免事件流成为凭据外带的旁路。

## 主要参考来源

- axum 官方 SSE 示例(`Sse::new` + `KeepAlive` + tokio-stream):https://www.cnblogs.com/soarowl/p/18320061
- axum broadcast 扇出 + `BroadcastStream`(多客户端 SSE):https://github.com/tokio-rs/axum/issues/2150
- SSE 自动重连、`retry:` 字段与 `Last-Event-ID` 语义:https://tigerabrodi.blog/server-sent-events-a-practical-guide-for-the-real-world 与 https://tutorials.dodatech.com/apis/sse/sse-retry-mechanism/
- React `useSSE` hook 与 cleanup/StrictMode 注意点:https://oneuptime.com/blog/post/2026-01-15-server-sent-events-sse-react/view
- SSE vs WebSocket 对比(单向 push 场景优先 SSE):https://websocket.org/comparisons/sse/ 与 https://www.svix.com/resources/faq/websocket-vs-sse/
- WebSocket `Upgrade` 在企业代理/防火墙下的兼容性失败案例:https://ably.com/blog/websocket-compatibility 与 https://blog.postman.com/websocket-connection-failed/
- SSE 在代理后的缓冲问题与 `X-Accel-Buffering: no`:https://blog.stackademic.com/net-10-sse-in-production-the-proxy-buffering-default-that-turns-real-time-into-batches-cbe49c45c3ad
- Docker 端口映射与容器内绑 `0.0.0.0`(SSE 端点连通性):https://docs.docker.com/engine/network/port-publishing/
- 事件 schema 版本演进(additive / 并行版本 / 破坏性变更阶梯):https://theburningmonk.com/2025/04/event-versioning-strategies-for-event-driven-architectures/ 与 https://docs.confluent.io/platform/current/schema-registry/fundamentals/schema-evolution.html
