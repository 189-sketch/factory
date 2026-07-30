# 运行时镜像内容定义

定义 `factory-core:codex` 与 `factory-core:claude` 两个运行时镜像。
遵循 R1(rootless/非 root 固定 UID/凭据不落明文/嵌套沙箱取舍)、A1(二进制 `factory`、docker/ 目录)、A3(单容器 P2、core 直接 spawn 本地 agent)。

## 1. 镜像分层与变体策略

三镜像,共享 base + provider 层差异(不是两套独立 Dockerfile):

```
factory-core:base        (Dockerfile.base)      — core 二进制 + 双 UID + 运行时依赖
factory-core:codex       (Dockerfile.codex)     — base + Codex CLI
factory-core:claude      (Dockerfile.claude)    — base + Claude Code CLI
```

- **共享 base**:core 编译、双 UID setup、git/gh/jq/tini、数据目录、禁 core dump,全在 base 一次定义,两 provider 镜像只差 agent CLI 安装层。
- 优点:core 升级只重建 base;provider 各自独立锁版本;新增 provider 只加一个薄 Dockerfile。

## 2. base image 选择

- **build stage**:`rust:<toolchain>-slim-bookworm`(edition 2024,toolchain 锁 `1.96.0`,与现有 sandbox-template 一致)。rusqlite `bundled` 自带 sqlite,故只需 `build-essential + pkg-config`,不依赖系统 sqlite。
- **runtime stage**:`debian:bookworm-slim`。
  - 不选 distroless:容器内要跑 `git`/`gh`/`jq` + agent CLI(codex/claude 安装脚本需要 glibc 与 shell),distroless 无 shell/package manager 装不动。
  - 多阶段构建把编译期工具链(Rust)挡在运行时镜像外,运行时镜像只含 `factory` 二进制 + 运行依赖。

## 3. 运行时安装与版本锁定

| agent | 安装方式 | 理由 |
| --- | --- | --- |
| Codex | 官方独立脚本 `https://chatgpt.com/codex/install.sh`(无需 Node) | npm `@openai/codex` 虽支持但拖入 Node 运行时;独立脚本更干净 |
| Claude Code | 官方原生安装器 `https://claude.ai/install.sh` | npm `@anthropic-ai/claude-code` 已弃用;原生安装器免 Node |

- **安装到 agent 用户 home**(`USER factory-agent` 下安装),core 不持有 agent 运行时,呼应 R1 的 UID 边界。
- **版本锁定**:安装脚本默认拉最新;生产构建应以 build arg 固定版本(如 `CODEX_VERSION`/`CLAUDE_VERSION` 传具体 tag,安装脚本/下载链接按版本取),保证镜像可复现。默认 `latest` 仅用于开发。
- ⚠️ npm 的**无作用域** `codex` 包是无关项目,务必用官方脚本或 `@openai/codex`。

## 4. entrypoint 与进程模型

- **PID1 = tini**:base `ENTRYPOINT ["/usr/bin/tini","--","/usr/local/bin/factory"]`,默认 `CMD ["agent-entrypoint"]`。tini 收割僵尸进程、转发信号,配合 core 的优雅停机。
- **启动串联**(`factory agent-entrypoint`,A1 定义):读 env(git URL + 凭据)→ clone → `init`(生成 .factory/)→ `serve`(HTTP/SSE,:7788)→ `run`(常驻轮询)。
- **单容器双进程身份**:core 以 `factory-core`(UID 1000)运行,spawn agent 时降权到 `factory-agent`(UID 1001)(R1 §一:同容器不同 UID)。
- **网络**:core 在容器内绑 `0.0.0.0:7788`(A2 契约)。

## 5. 双 UID 与凭据最小化(R1 落地)

- `factory-core`(UID 1000):持有账本(`/factory/data`,700)、git 工作区、注入的凭据 env。
- `factory-agent`(UID 1001):跑不可信 agent 代码;无权读 `/factory/data` 与 core 的 `/proc/<pid>/environ`。
- **procfs hidepid**:ui 拉起容器时挂 `--mount` procfs 带 `hidepid=2`(或 compose `pid` 选项),使 agent 看不到 core 进程 environ。这在编排层(A3/C2)落地,镜像内备好双 UID 即可。
- **禁 core dump**:`RUST_BACKTRACE=0` + 编排层 `ulimit -c 0`(R1 §三)。
- **凭据**:一律运行时 `docker run -e` 注入 core(C2 链路),core 用即清 + spawn agent 时 `env_remove` 敏感键;**不进镜像、不写文件**。

## 6. 嵌套沙箱取舍(R1 开放风险 #2)

默认**不放开** `SYS_ADMIN`/seccomp/AppArmor 等危险能力:
- 外层 rootless 容器即主隔离边界;agent 自带的嵌套沙箱(codex bubblewrap / claude sandbox)降级为"依赖外层容器"的纵深,而非必须。
- codex 现有命令含 `--sandbox danger-full-access`(sandbox.rs:29),容器内模式下由 core 的 runtime 配置改为不请求特权沙箱,靠外层容器隔离。
- 是否放开内层沙箱能力做成**编排层可配置**(`enable_nested_sandbox: bool`,默认 false),写清取舍,不模棱两可。

## 7. 健康探针

- 镜像提供 `HEALTHCHECK`(ui 编排/容器发现用):`curl -fsS http://127.0.0.1:7788/api/v1/health || exit 1`(A2 探活端点,容器内自查)。

## 8. 构建

```sh
docker build -f docker/Dockerfile.base   -t factory-core:base   .
docker build -f docker/Dockerfile.codex  -t factory-core:codex  .
docker build -f docker/Dockerfile.claude -t factory-core:claude .
```

构建上下文为仓库根(base 的 build stage 需 `factory-core/` 源码与根 `Cargo.toml`/`Cargo.lock`)。
