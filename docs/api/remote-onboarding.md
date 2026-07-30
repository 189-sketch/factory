# factory 远程接入 UX(remote-add)

用户给一个 git 地址,系统自动 clone 进容器、init、开始工作的完整链路。
这是你最初提出的第 1 条需求的正式闭环,遵循 A1(形态②、`agent-entrypoint`)、A3(单容器 P2、后端可插拔、idle snapshot)、A4(ui 编排/发现/聚合)、C1(镜像)。

## 1. 入口形态:ui 的"添加仓库"对话框是唯一入口

接入是编排动作(拉起容器、注入凭据),所有权在 ui,故**唯一入口是 ui 的"添加仓库"对话框**(Electron 与 Web 同一份)。不保留独立 CLI 接入命令——CLI 已在 A1 决策全走 docker;若未来要 CLI,也只是转发到 ui 后端 `POST /ui/api/onboard` 的薄壳,本契约不展开。

**表单字段**:

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `git_url` | ✅ | 仓库地址。支持 `git@github.com:owner/repo.git`、`https://github.com/owner/repo(.git)`、`ssh://git@ssh.github.com/owner/repo.git`(复用 config.rs:851 `canonical_github_identity` 的归一化) |
| `provider` | ✅ | `codex` \| `claude`,决定镜像(`factory-core:codex` / `:claude`)与 agent 凭据 |
| `credential_ref` | ✅ | 指向 C2 凭据存储中已配置的凭据(agent 订阅 token + GitHub App) |
| `branch` | ⬜ | 默认远端默认分支(detached 后由 workflow 决定基线) |
| `trigger_labels` | ⬜ | 覆盖默认触发标签(默认 `factory:ready-for-spec` / `factory:ready-to-implement`,写进生成的 config.toml) |
| `idle_timeout` | ⬜ | 覆盖全局默认 10h(A3) |
| `backend` | ⬜ | 覆盖默认执行后端(docker / rootless Podman / 远端 / microVM,A3 可插拔) |

## 2. 接入时序(含失败回滚与中途反馈)

```
[ui 表单提交 git_url]
   │ ui 前端即时校验:URL 语法 + canonical_github_identity 归一化出 owner/repo
   ▼
[ui 后端 POST /ui/api/onboard]
   │ 1. 幂等检查:注册表查 owner/repo → 已存在且 running 则返回现有容器(见 §4)
   │ 2. 按 backend 拉起容器(镜像=factory-core:<provider>),
   │    注入 FACTORY_GIT_URL + FACTORY_API_TOKEN + 凭据 env(C2 链路)
   │ 3. 打 label(factory.managed/repository/provider/...),登记注册表(A4)
   ▼
[容器内 factory agent-entrypoint]
   │ 4. clone FACTORY_GIT_URL → /factory/work/<repo>(用注入的 git 凭据)
   │ 5. 若存在 snapshot 保留分支 → 恢复工作区(A3 idle snapshot)
   │ 6. factory init → 生成/校验 .factory/(幂等;已有配置则跳过生成)
   │ 7. factory serve(:7788,SSE+控制面)
   │ 8. factory run(常驻轮询)
   ▼
[ui 后端发现容器健康]
   │ 9. 连上 core /events,收到 repo.health{status:"ready"} → 卡片转绿
   │ 10. 接入完成,开始监听 issue
```

**中途反馈**:ui 把 1-9 步映射为对话框内的进度步骤(校验→拉容器→注入→克隆→初始化→就绪),每步成功/失败即时呈现(经 docker events + core SSE + 后端轮询)。

**失败回滚**(任一步失败):
- 容器已建但 agent-entrypoint 失败(clone 失败/凭据无效/init 报错)→ ui 销毁容器、清注册表、对话框报具体失败步与脱敏后的 stderr tail。
- 镜像拉取失败 → 报告后端/镜像错误,不建容器。
- 凭据校验失败 → 不建容器,提示去凭据管理配置。
- 回滚保证**不留半截容器/账本**;用户修正后可重试(幂等)。

## 3. clone 凭据与 agent 凭据分离

- **git clone 凭据**:用于容器内 `git clone` 私有仓库。
  - HTTPS:用 GitHub App installation token 作为 `http.extraheader` 或 askpass 注入(与 agent 干活的 GitHub 凭据**同一个** App,但 scope 只需 `contents:read` 用于 clone)。
  - SSH:若用户配 deploy key,经 C2 注入到容器 `~/.ssh`(agent UID home),core 侧 clone 用。
- **agent 干活凭据**:codex/claude 订阅 token(读 issue/写代码/开 PR)+ GitHub App token(gh/git push,需 `contents:write` + `pull_requests:write`,**绝不给 merge**,与 `HUMAN_MERGE_POLICY` 一致)。
- 三者经 C2 的统一注入链路进容器,按 UID/用途隔离;clone 凭据用即清。

## 4. 重复接入与恢复(幂等)

- **身份去重**:接入身份 = `canonical_github_identity` 归一化的 `owner/repo`(小写,与账本数据目录派生同源)。同一 `owner/repo` 重复提交:
  - 容器 running → 返回现有容器,不重建(幂等)。
  - 容器 idle 已销毁但有 snapshot → **重建容器并恢复**:clone 后从保留分支 checkout 工作区,账本数据卷按 `owner/repo` 身份复用(SHA-256 派生路径不变),core 重启 reconcile 中断任务(现有恢复语义,最多 2 次)。
  - 从未接入 → 全新接入。
- **snapshot 恢复**:A3 销毁前把工作区改动 commit+push 到保留分支 `factory/snapshot/<run>`;重接入时 agent-entrypoint 检测该分支并恢复,让 agent"先看现状再续作"(与现有恢复提示词一致)。

## 5. 首次 init 的交互(配置确认)

- 新 repo 无 `.factory/config.toml` 时,容器内 init 用默认模板生成(嵌入的三工作流 + github 适配器 + 默认触发标签)。
- **默认"先生成即可用"**:init 成功即接入完成、开始监听,不打断用户。
- 用户随后可在 ui 单仓视图查看/编辑生成的 `config.toml` 触发器(经控制面读写,或引导到仓库改文件),改了之后 core 下次轮询生效。不把"确认配置"设为接入的阻塞步骤,保持"给 URL 即开始工作"的顺滑。

## 6. 安全边界

- `git_url` 与表单字段在 ui 前端+后端双重校验;URL 归一化失败即拒,不进容器。
- 凭据只经 C2 链路注入,不经表单明文回传;对话框不显示完整 token。
- 容器出站受 A3 egress 防火墙白名单约束;clone/agent 流量都在白名单内。

## 7. 与现有 CLI 的关系

- `factory init` 语义不变(A1:容器内脚手架),`agent-entrypoint` 串联 clone→init→serve→run。
- 新增的是 **ui 侧编排**:把"拉容器+注入+发现+反馈"这层包在 `agent-entrypoint` 外面。core 内部逻辑(clone/init/serve/run)复用现有,只新增 `agent-entrypoint` 这个串联子命令与 serve。
