# fleet 多仓在新架构下的行为

多仓从"单进程轮多仓"迁移到"ui 编排多容器、每容器单仓"。
遵循 A1(多仓语义改方向)、A3(后端可插拔/idle 销毁/snapshot/限额)、A4(ui 编排/发现/聚合)、现有 fleet.rs(参考语义)。

## 1. 编排策略:并发/串行/限额

**新模型**:每个接入的 repo = 一个容器(A1)。N 个 repo = N 个**潜在**容器,但**不是全部常驻**——靠 A3 的 idle 销毁,只有活跃仓占容器。

**限额分层**(ui 编排层强制):
- **全局最大活跃容器数** `max_active_containers`(ui 配置,默认如 8):同时 running 的 worker 容器上限。超出时新接入/唤醒排队,按"最久未活跃优先销毁/排队等待"调度。
- **每后端限额** `backend.<name>.max_containers`:docker/Podman/远端/microVM 各自的并发上限(保护宿主资源)。
- **资源配额**(每容器,沿用现有 `worker.cpus`/`worker.memory`,默认 4c/8g):ui 拉起时按后端能力下发。
- 全局/后端限额是**容器级**(多少仓并行),仓内的 run 并发仍由 core 的 `max_concurrent_runs_per_repository` 管(单仓容器内)。

## 2. fleet 配置迁移:fleet.toml → ui 仓库注册表

- **fleet.toml 退出**:新架构下"仓库列表"由 **ui 的仓库注册表**(A4,含 repository/provider/backend/credential_ref/idle_timeout/限额)取代,不再有独立 fleet.toml。
- **core 内 fleet.rs 的去留**:**编排职责上移到 ui**(发现/限额/退避/销毁),core 退化为**单仓 supervisor**(每容器只管自己那个仓)。
  - `FleetSupervisor` 的跨仓编排(统一并发、交错轮询、激活守卫)**不再需要**——那是"单进程管多仓"的产物,现在每容器单仓。
  - core 保留**单仓内**的并发限制与恢复(`max_concurrent_runs(_per_repository)`、recovery),这部分 fleet.rs 里 per-repo 的 clamp 逻辑(fleet.rs:464)融入 core 的单仓配置。
  - **fleet.rs 整体标记为遗留**,其核心不变量(每仓独立账本、原子认领、有界恢复)已在单仓 core 内自然成立(每容器一个 core 一个账本)。
- **迁移路径**:提供一次性导入——读旧 fleet.toml 的 `[[repository]]`(git URL/provider),批量填入 ui 仓库注册表并触发 B1 接入。

## 3. 跨仓统一视图与限额聚合

- **健康聚合**:每容器 core 发 `repo.health`(A2),ui 后端聚合(A4 §3)驱动 D1 总览卡片。`active_runs`/`queued_tasks`/`backoff_until` 直接来自 core 上报。
- **总并发槽的接管**:现在 `FleetSupervisor` 统一限总并发;新架构**总并发 = ui 的 `max_active_containers`**(容器级)+ 每容器内 core 的 run 并发(仓级)。两层限额替代原来的单层总并发,语义更清晰(ui 管"几个仓同时跑",core 管"一仓内几个 run")。
- 全局看板的"正在跑 N 个任务"= Σ 各容器 `repo.health.active_runs`,前端聚合(D2 store)。

## 4. 故障隔离与退避

- **某仓容器崩溃/轮询失败**:**ui 编排层实现退避**(上移自 fleet.rs `repository_backoff`,fleet.rs:117 的指数退避算法可复用到 ui)。
  - core 容器内轮询失败 → core 上报 `repo.health{status:"backoff", backoff_until}` → ui 展示;core 内部对该仓退避重试(单仓内逻辑,保留现有 daemon 的退避)。
  - 容器崩溃/失联 → ui 后端经 docker events + 心跳超时判定,标记 `offline`(A4 §5),按退避策略重建容器(指数退避,封顶),不影响其他仓容器。
- **隔离保证**:每容器独立(进程/网络/账本),一仓故障不波及其他仓——比原来"单进程多仓"的隔离更强(原来共享一个 daemon 进程)。

## 5. 批量操作

ui 支持对仓库注册表的批量操作(多选后一次下发):
- **批量接入**:导入 fleet.toml / 粘贴多个 git URL → 逐个走 B1 接入(受 `max_active_containers` 排队)。
- **批量暂停**:停 SSE + 暂停容器(保留账本),总览卡片置灰。
- **批量销毁**:触发各容器销毁(按 A3,销毁前默认 snapshot)。
- 批量操作经 ui 编排层单飞 + 逐项反馈(哪仓成功/失败),不阻塞单个失败影响整批。

## 6. 关键取舍

- **编排上移、core 退化**:fleet 的跨仓智能(发现/限额/退避/销毁)全部上移 ui,core 只做单仓 supervisor。这与形态②"core 保持简单、编排在外"一致,fleet.rs 作为"单进程多仓"时代的产物退出编排舞台。
- **限额两层化**:总并发从"单层总并发"拆成"ui 容器级 + core 仓级"两层,语义更贴新架构。
- **隔离升级**:从"单进程内多仓逻辑隔离"升级为"每仓一容器物理隔离",故障域更小,代价是编排复杂度在 ui——已由 A3/A4 承接。
- **fleet.toml 平滑迁移**:旧配置一次性导入 ui 注册表,老用户不丢仓库列表。
