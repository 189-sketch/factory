# UI 与多容器的连接、发现与事件聚合

本契约定义 factory-ui 如何发现、连接、聚合 N 个容器内 core 的 SSE/HTTP。
遵循 A1(形态②,ui 管 docker)、A3(后端可插拔)、R2(SSE)、A2(G1 游标 + /api/v1)。

## 1. 连接拓扑：渲染层单连接，ui 后端聚合(推荐)

**渲染层(React)不直连任何容器**。所有到容器的连接都收敛到 ui 后端进程:

```
渲染层(浏览器/Electron renderer)
   │  1 条 SSE(GET /ui/events)+ 若干 HTTP(/ui/api/*)
   ▼
ui 后端(Express 5 / Electron 主进程内嵌 server)
   │  N 条 SSE(每容器 1 条 → core /events)+ N 组 HTTP(→ core /api/v1/*)
   ▼
容器 0..N 内的 factory-core(:7788)
```

- 渲染层只跟 ui 后端建**一条** SSE(`/ui/events`),后端把 N 个容器的事件聚合成一条总流。
- 渲染层的控制指令走 `/ui/api/*`,后端路由到对应容器的 core `/api/v1/*`。
- 后端 = AionUi `web-host` 模式的反向代理 + 聚合层,Electron 与 Web 双模式共用同一份后端代码(Electron 主进程内嵌 Express;web 模式独立 node 进程)。

**为什么不让渲染层直连容器**(否决方案):
- 直连要求每容器映射一个宿主端口,渲染层要维护 N 条 EventSource,N 变化时连接管理复杂,且暴露容器拓扑给前端。
- 聚合后渲染层永远单连接,容器增删对前端透明;断线补发、缓存、权限收口都在后端一处。

## 2. 容器发现与注册：docker label + 后端轮询

采用 **ui 主动发现**(而非 core 注册),因为 ui 本就是容器生命周期的所有者(A3):

- ui 拉起每个 worker 容器时打 label:
  - `factory.managed=true`
  - `factory.repository=<owner/repo>`
  - `factory.provider=codex|claude`
  - `factory.api_token_hash=<sha256(token) 前 8 位>`(便于后端匹配注入的令牌,不含明文)
- 后端周期性(`docker ps --filter label=factory.managed=true`,或订阅 docker events API)维护一张**容器注册表**:`{repository, container_id, backend, host, port, token, status, last_seen}`。
- 端口:每容器映射到宿主一个**随机空闲端口**(`docker run -P` 或 `-p 0:7788`),后端从 `docker inspect` 读实际端口。
- 远端后端(`DOCKER_HOST=tcp://...`):容器端口映射到远端宿主,后端用远端宿主地址 + 映射端口连接;发现机制不变(都走 docker API)。
- core **无需**反向注册——它不知道也不关心 ui 在哪(core 无感知原则)。

## 3. 事件聚合：后端扇入、渲染层单流、游标分仓

- 后端为注册表里的每个 `running` 容器维护一条到 core `/events` 的 SSE 连接(用该容器的 bearer 令牌)。
- 每条容器流断线时,后端按 R2/A2 语义用**该仓最后的 event_id** 做 `Last-Event-ID` 补发重连(指数退避)。
- 后端把多容器事件**归一化**后扇出给渲染层:
  - 信封已是全局设计(含 `repository`),后端原样转发,只在缺失时补 `repository` 与 ui 侧容器标识。
  - 渲染层收到的是多仓混合流,前端按 `repository` 分桶到各仓库视图。
- **游标是 per-repo 的**(每容器 core 各自维护 event_id 空间)。渲染层/后端补发时按 `repository` 分别记忆 `last_event_id`,不能跨仓混用——这点前端状态派生(D2)必须遵守。
- 后端对 `repo.health` 之外的高频 `run.activity` 做轻量合并(同 run 短窗口内批量下发),降低渲染层压力。

## 4. 控制面路由

- 渲染层 `POST /ui/api/{repository}/runs/{id}/cancel` → 后端查注册表得该仓容器地址+令牌 → 转发 `POST /api/v1/runs/{id}/cancel`(带容器令牌)→ 回传结果。
- `onboard`、`tasks/runs` 查询同理,全部以 `repository` 为路由键。
- 后端对写操作做**单飞**(single-flight)与幂等透传(`client_request_id`)。

## 5. 断线与降级

- 某容器 SSE 断开超过阈值(如 3 次重连失败/30s)→ 后端将该仓标记 `offline`,向渲染层发一个 ui 合成的 `repo.health{status:"offline"}` 事件(信封 `repository` 指向该仓),并在注册表标记。
- 容器心跳(`/api/v1/health`)恢复 → 后端重建 SSE,先发 `repo.health{status:"ready"}`。
- 容器被 idle 销毁(A3)→ docker events 通知后端,后端关闭对应连接、更新注册表为 `destroyed`,渲染层卡片置灰。
- 渲染层与后端之间那条 SSE 断开 → 前端 EventSource 自动重连,后端补发自该前端 `last_event_id`(后端为每个前端连接维护独立游标,或前端记录后回传)。

## 6. 与双模式的关系

- **Electron 模式**:后端 = 主进程内嵌 Express,渲染层经 `ipc` 或 `http://127.0.0.1:<port>` 访问;docker 访问用主进程的 dockerode。
- **Web 模式**:后端 = 独立 node 进程(AionUi `web-host` 同款),反向代理 + 静态托管 + 上述聚合;远程浏览器经同一端口访问。
- 两种模式共享同一 `packages/web-host` 后端代码与同一渲染层,仅"后端进程如何启动"不同 → 对应 A1 的 npm workspace 布局。
