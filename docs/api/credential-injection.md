# 凭据注入实现(ui 存储 → 容器注入 → 最小化暴露)

落实 P2 的凭据生命周期:凭据从用户手里,经 ui 存储、注入容器、到容器内 agent 的最小化暴露。
遵循 R1(安全模型 §三)、A3(P2 简化版:env 注入+用即清+App token;core 无感知,ui 侧差异化)、B1(clone/agent 凭据分离、credential_ref)。

## 1. ui 端凭据存储

**Electron 模式**:用 `safeStorage`(系统 keychain 加密:macOS Keychain / Windows DPAPI / Linux libsecret)加密凭据明文,密文落 ui 本地 SQLite(`better-sqlite3`,与 AionUi 同驱动)的 `credentials` 表。

**Web 模式**(无 safeStorage):凭据经 TLS 提交到 ui 后端,后端用**用户口令派生的密钥**(或部署方配置的 KMS/环境密钥)做 AES-GCM 加密,同样落后端 SQLite。口令不存储,仅在加密/解密时经前端临时提供(或会话内存持有)。

**`credentials` 表**(逻辑模型):

| 列 | 说明 |
| --- | --- |
| `id` | 凭据引用(B1 表单 `credential_ref` 指它) |
| `kind` | `codex_subscription` \| `claude_credential` \| `github_app` |
| `label` | 用户可读的名称(如"我的 Codex Pro") |
| `secret_enc` | 加密后的凭据明文(token / claude 凭据 / GitHub App 私钥+app_id+installation_id) |
| `scope_hint` | 适用范围提示(哪些仓/组织可用) |
| `created_at` / `updated_at` | |

- **凭据与仓库的绑定**:不硬绑定。接入(B1)时表单 `credential_ref` 选定一条凭据;同一凭据可服务多仓。绑定关系记在 ui 的仓库注册表(`repository → credential_ref`),凭据删除前检查是否被引用。
- 对话框/列表**永不回显完整明文**,只显示 `label` + 掩码(如 `ghp_…abcd`)。

## 2. 注入链路:ui → 容器

**目标**:凭据到容器内 core,但**不出现在 `docker inspect`**(R1 警示 env 会被 inspect 回显)。

- **首选:启动时经 stdin / 临时 fd 传入,而非 `docker run -e`。**
  - ui 后端拉起容器时,把凭据作为 `agent-entrypoint` 的**一次性启动载荷**,经容器 stdin(或挂载的 tmpfs 卷内一个 `0600`、core UID 拥有的文件,core 读后即删)传入。
  - core 读到后立即建立会话/写入 core UID 拥有的受保护文件(`/factory/data`,700),然后**从载荷来源清除**(关 stdin / 删 tmpfs 文件)。
  - 这样凭据不进 `docker inspect` 的 Env,也不进进程初始 environ。
- **次选(简化起步):`docker run -e` 注入**,但接受 `docker inspect` 可见的代价,并用 §3 的"用即清"压缩窗口。开发期可用,生产应升到首选方案。
- **FACTORY_API_TOKEN**(A2 控制面令牌)与**业务凭据**分开:API token 走 env 注入(它本就是容器级、低敏),业务凭据走上述 stdin/tmpfs 高敏链路。

**GitHub App token 获取**(§4 细化):ui 持有 App 私钥,**按需**在拉起容器前生成小时级 installation token,经高敏链路注入;core 不持有 App 私钥,只拿短命 installation token。

## 3. 容器内最小化暴露(R1 四件套落地)

1. **不同 UID**:core(1000)持凭据与 `/factory/data`;agent(1001)跑不可信代码。镜像已备好双 UID(C1)。
2. **procfs hidepid=2**:ui 拉起容器挂 `--mount type=proc,dst=/proc,proc-opt=hidepid=2`(或 rootless 等价),agent 看不到 core 进程 `/proc/<pid>/environ`。
3. **用即清**:core 读凭据建立会话后,从自身 environ `env_remove` 敏感键;spawn agent 时对 `FACTORY_GITHUB_TOKEN`/`GH_TOKEN`/`*_API_KEY` 等一律 `env_remove`(现有 `preserve_workspace`/`run_with_token` 已这么做,推广到 agent spawn 全路径)。agent 需要的 GitHub 访问经 core 的窄通道(见 §4),不直接继承凭据。
4. **短命窄权限**:GitHub 用 App installation token(小时级,scope 最小,**绝不给 merge**);禁 core dump(`ulimit -c 0` + `RUST_BACKTRACE=0`,C1 已落)。

## 4. GitHub App token 签发/刷新

- **ui 持有 App 私钥**(`kind=github_app` 那条凭据,加密存);安装时记录 `app_id` + `installation_id`。
- **签发**:ui 用私钥签 JWT → 换 installation token(scope:`contents:read/write` + `pull_requests:write` + `issues:read/write`,按需;不给 `merge`/admin)。
- **注入**:installation token 经高敏链路给 core;core 设置 `github_token_env`(默认 `FACTORY_GITHUB_TOKEN`,config.rs:624)指向它,`gh`/`git` 调用用它(github.rs:1425、clone.rs)。
- **刷新**:installation token 约 1h 过期,可能短于 run 生命周期(默认 2h/4h)。core **在 token 临期时经控制面向 ui 请求刷新**(ui 用私钥重签一个新 installation token 经控制面推给 core,或 core 拉取)。core 不持有私钥,只能拿 ui 签发的短命 token。
- **clone 凭据**(B1 §3):用同一 App 的 `contents:read` installation token(HTTPS `http.extraheader`)或独立 deploy key(SSH)。

## 5. 多 provider 并存

- 凭据按 `kind` 命名空间隔离:`codex_subscription` / `claude_credential` / `github_app` 各自独立条目。
- 同一 ui 可管理 codex 仓(注入 `codex_subscription`)与 claude 仓(注入 `claude_credential`);GitHub App 可两 provider 共用一条(它是仓库访问凭据,与 LLM provider 无关)。
- 容器只注入**该仓 provider 对应**的那条 LLM 凭据 + GitHub 凭据,不注入无关凭据(最小暴露)。

## 6. 安全边界与不做的事

- 凭据**不写进镜像**(C1)、**不写进 git**、**不明文落日志/事件流**(经 `sanitize_for_storage` 脱敏,A2)。
- ui 列表/对话框不回显明文;导出/备份凭据需二次确认并加密。
- 注入失败(凭据无效/过期)→ 不建容器(B1 回滚),报脱敏原因。
- 不支持把 App 私钥直接给 core;core 只拿短命 installation token。

## 7. 与现有代码的接点

- core 已支持 `worker.github_token_env`(config.rs:624)从 env 读 token,`gh`(github.rs:1425)与 clone(clone.rs)用它 → 注入链路只要把 installation token 放进该 env 变量即可复用。
- `env_remove` 脱敏习惯已存在(github.rs:1423 `env_remove("GH_REPO")`、preserve_workspace 的 `env_remove(github_token_env)`),推广到 agent spawn。
- 新增:core 的"启动载荷读入 + 用即清 + 临期刷新请求"逻辑,落在 `agent-entrypoint` 与 runtime spawn 路径。
