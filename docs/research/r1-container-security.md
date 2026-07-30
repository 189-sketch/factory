# R1 容器安全模型研究

本报告解决工单 R1(https://github.com/189-sketch/factory/issues/3)。
目标是把 Factory 从"宿主机编排 + 可选 Docker Sandbox(microVM)"升级为"core 跑在容器内、每个接入的 git repo 一个容器、凭据经环境变量注入"的容器化架构。
本报告只做安全决策建议，不写实现代码。

阅读前置:现有架构见 [../analysis.md](../analysis.md)(特别是第六节隔离模型)，microVM 现状实现见 [../../src/sandbox.rs](../../src/sandbox.rs)。

## 一、容器模型建议

结论:用 rootless 容器(优先 Podman，其次 rootless Docker)承载每个 repo 的 core + agent 运行时，容器内以非 root 固定 UID 运行;不要让 agent 进程拿到容器内 root，更不要挂载宿主 docker.sock。

理由:

- 威胁主体是"agent 生成的不可信代码 + 被提示词注入诱导的 agent 行为"。容器共享宿主内核，一旦 agent 容器以 root 运行且发生内核级容器逃逸，后果是整个宿主失守。
- rootless 模式下，即使容器内进程拿到"容器内 root"，它在宿主上仍映射为一个无权限的普通 UID(user namespace 隔离)，逃逸后的爆炸半径被显著压缩。这是社区运行 Claude Code 的共识:openclaw-plugin-claude-code 明确推荐 Podman over Docker，理由是"Docker 默认 root daemon，容器逃逸即宿主 root;rootless 容器逃逸落在无权的 user namespace"。
- 嵌套沙箱是关键约束。Codex 在 Linux 上用 bubblewrap(user namespace)，Claude Code 也有自带沙箱。要在外层容器里再跑这层嵌套沙箱，外层容器必须放行 `unshare`/`mount`/`pivot_root` 等系统调用，代价是要么加 `--cap-add SYS_ADMIN`、放开 seccomp/AppArmor，要么(对 Claude Code)开 `enableWeakerNestedSandbox: true`。
  - 建议:外层容器默认不放开这些危险能力，而是让 agent 的嵌套沙箱降级为"依赖外层容器做隔离"的模式。外层 rootless 容器本身就是隔离边界，内层 agent 沙箱作为纵深，二者取其一提供主边界即可。
  - 绝对禁止把 `/var/run/docker.sock` 挂进 agent 容器:这等于给了逃逸到宿主并起特权容器的通道。
- 内核前置条件:宿主必须允许非特权 user namespace(`kernel.unprivileged_userns_clone`)。注意 Ubuntu 24.04+ 默认通过 AppArmor 限制非特权 userns，需要显式放行;rootless 还会退回 fuse-overlayfs 与 slirp4netns/pasta 网络，带来一定性能开销。这是为隔离性付的确定成本，符合"质量与安全优先于开发成本"的项目准则。

对 Factory 的具体含义:core(Factory daemon)与 agent(codex / claude-code)是否同容器是第二层决策。
建议 core 与 agent 同容器但不同 UID:core 用一个 UID 持有账本与源码，agent 子进程用另一个 UID。这样同容器内靠 UID 边界让 agent 无法读 core 的环境与账本(见第三节)。若后续要更强隔离，再把 agent 单独拆成"每 run 一个短命容器"，core 跨容器编排。

## 二、网络模型建议

结论:默认拒绝(deny-by-default)出站，只放行一个按 provider 最小化的域名白名单;白名单在容器网络层强制(专用 egress 代理 / 防火墙)，而不是靠 agent 自觉。所有流量仅 HTTPS/443。

必需域名(按 provider 区分，只放行用到的那个):

- Anthropic(Claude Code，必需):`api.anthropic.com`、`claude.ai`、`platform.claude.com`、`downloads.claude.ai`。OAuth token 的换取/刷新/吊销走 `platform.claude.com`，所以即便只用订阅认证也必须放行。
- OpenAI(Codex，必需):`api.openai.com`。
- GitHub(agent 用 gh/git 干活必需):`github.com`、`api.github.com`、`raw.githubusercontent.com`，以及 git over https 所需的对应主机。对象存储类(如 release/LFS 可能落到 `*.githubusercontent.com`)按需补。

可关掉的非必需流量(收缩暴露面):

- Claude Code 设 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` 可同时关掉两个 Datadog 遥测/报错域名;`ENABLE_CLAUDEAI_MCP_SERVERS=false` 关掉 `mcp-proxy.anthropic.com`;`DISABLE_TELEMETRY`/`DISABLE_ERROR_REPORTING`/`DO_NOT_TRACK` 进一步减面。
- 用 npm 或自管二进制安装时，`downloads.claude.ai` 与 `storage.googleapis.com` 的安装/自更新用途不需要，但 npm 安装仍需 `registry.npmjs.org`(或内部镜像)。
- 注意一个坑:即使走 LLM 网关(`ANTHROPIC_BASE_URL`)，Claude Code 的 fast-mode 可用性检查和 WebFetch 域安全检查仍会回头打 `api.anthropic.com`，白名单要留这一条或显式 `skipWebFetchPreflight: true`。

代理注入凭据 vs 环境变量直接注入的取舍(这是网络模型里最值钱的一条):

- 直接环境变量注入，agent 进程持有明文 key，提示词注入一句 `print(os.environ)` 或读 `/proc/*/environ` 就能外带。`docker inspect` 也会暴露全部 env。
- 代理注入(参考 gh-aw-firewall 的 api-proxy-sidecar 三容器模式):agent 容器只拿到占位 token(如 `ghu_aaaa...`)和 `*_BASE_URL` 重定向，真实 key 留在独立的 sidecar 容器里，sidecar 在转发前剥离客户端 auth 头并注入真 key。这样"agent 内存里根本没有可真外带的东西"。
- 建议:凭据能走代理就走强代理(见第三节)。代价是 gh-aw-firewall 自身也踩过的坑:api-proxy sidecar 没有按调用方鉴权，sandbox 网络内任何进程摸到 10000/10001 端口就能用组织的 key 调 API。Factory 若采用代理模式，必须补上"按调用方/按 run 的一次性凭证 + 网络命名空间级隔离"，不能让"网络可达"成为唯一访问控制。

## 三、凭据注入模型建议

结论:优先"代理/sidecar 注入、容器内不落明文";若必须环境变量注入，则按"独立 UID + procfs hidepid + 短命 + 最小权限"四件套把暴露面压到最小。Codex 订阅 token 与 claude 凭据一视同仁按高敏处理。

环境变量注入在容器内的真实暴露范围(三条都已验证):

1. `/proc/<pid>/environ`:同 UID 的兄弟进程可直接读到彼此的环境变量。gh-aw-firewall 多份 issue(#1762/#1771/#1786/#1802/#1830/#1834)反复确认:同 UID 进程能跨进程边界探测到 token 键的存在与长度。container 里 core 与 agent 若同 UID，agent 就能读 core 持有的所有 env。
2. 子进程继承:env 默认沿 fork/exec 传下去，agent spawn 的测试、服务、子 shell 全部继承，任何一环被注入即外泄。
3. core dump / crash:进程崩溃转储会把内存(含 env)落盘;`docker inspect`、k8s describe 同样回显 env。

最小化手段(按收益排序):

- 首选，不落明文:用 sidecar/代理注入(见第二节)，真实 key 不进 agent 进程地址空间，从根本上消除上述全部三条。这是 gh-aw-firewall、Vault Agent sidecar(短命凭据写内存卷而非 env)、Aembit(请求时动态注入)共同的演进方向。
- 次选，环境变量注入时收敛:
  - core 与 agent 用不同 UID(见第一节),procfs 挂 `hidepid=2`(再配 `gid=` 例外),让 agent 看不到 core 进程的 `/proc/<pid>/environ`。
  - 注入后立即"用即清":core 读完凭据建立会话/写入受保护文件后，从自身 env 抹掉，缩短存活窗口;子进程默认 `env_remove` 敏感键(Factory 现有 `preserve_workspace` 的 host git 调用已经这么做:`env_remove(github_token_env)/GH_TOKEN/GITHUB_TOKEN`，这个习惯要保留并推广到 agent spawn 路径)。
  - 用短命、窄权限凭据:GitHub 用 fine-grained PAT 或 GitHub App installation token(小时级过期、只读必要仓库、绝不给 merge 权限),配合分支保护从制度上保证 agent 无法绕过人类合并(与 Factory 现有 `HUMAN_MERGE_POLICY` 一致)。
  - 禁 core dump(`ulimit -c 0` / `SYS_PTRACE` 不加)、关掉 ptrace，缩小崩溃与调试面。

## 四、microVM 去留结论

结论:新架构在"可信代码、但 agent 行为不可完全预测"的主场景下，可以用"rootless 容器 + 网络白名单 + 代理注入凭据"达到与 microVM 相当的实用安全水平，microVM 那层可以不再是默认;但要把 microVM 保留为"高敏仓库/真正不可信代码"的可选增强档，而不是彻底删除。

理由(信任边界差异):

- 核心差异就一句话:容器共享宿主内核，microVM 有独立内核 + hypervisor 硬件边界。容器逃逸(借内核漏洞/syscall bug 打破 namespace)是一个真实存在的漏洞类别，且 seccomp/AppArmor 加固不改变这个底层模型——"有内核 exploit 的攻击者不在乎你的 seccomp profile"。microVM 里即使打穿 guest 内核，还要再破 hypervisor 才能摸到宿主，难得多。
- 但 Factory 的实际威胁模型要分层看:
  - 跑的是"你自己的仓库 + agent 生成但会经人类评审才合并的代码"，且有分支保护、窄权限凭据、网络白名单、rootless UID 边界这几道纵深。这不是"多租户跑任意用户提交代码"的场景。
  - 在这个模型下，rootless 容器 + deny-by-default 网络 + 不落明文凭据，已经把"逃逸到宿主"和"外带凭据"两条主要杀伤链都压住了。microVM 提供的额外内核边界，边际收益下降，却带来启动慢、内存开销高、快照+fetch 移交复杂(现有 `preserve_workspace` 那套)的确定成本。
- 保留 microVM 的理由是它仍是"真正不可信代码"场景的唯一正确答案:当你要跑的仓库本身不可信、或未来开放成多租户/托管 worker 池(analysis.md 第十三节列为扩展点)时，共享内核就不够了，那时 microVM 的独立内核是必须的。因此把 microVM 降级为可选档而非删除，是兼顾当下成本与未来扩展的稳妥决策。

一句话:容器是默认信任边界，microVM 是高敏/多租户档的升级路径，二者用同一套网络白名单与凭据代理策略，只是隔离强度不同。

## 五、开放风险清单

按优先级列出本报告未完全闭环、需要在实现期处理的风险:

1. 代理注入的按调用方鉴权(高):gh-aw-firewall #3022 证明"网络可达即可用 key"会被同 sandbox 任意进程滥用。Factory 若走代理模式，必须设计一次性/按 run 凭证与网络命名空间隔离，这是实现期第一优先级。
2. 嵌套沙箱能力放行(高):为跑 codex bubblewrap / claude sandbox 而放开 `SYS_ADMIN`、seccomp、AppArmor，会显著削弱外层容器边界。需要在"放开能力换内层沙箱"与"收紧能力靠外层容器"之间做明确取舍并写成配置，不能模棱两可。
3. 宿主内核 userns 前置(中):Ubuntu 24.04+ 默认 AppArmor 限制非特权 userns,rootless 可能装不上或要额外放行;安装/校验流程(`factory validate` 对应物)需要检测并给出明确指引。
4. OAuth 刷新域不可省(中):claude 订阅的 token 刷新走 `platform.claude.com`,fast-mode/WebFetch 检查回头打 `api.anthropic.com`,白名单漏配会导致间歇性鉴权失败而非显式报错，排障成本高。
5. core 与 agent 同容器的 UID 隔离(中):若同容器，必须落实不同 UID + procfs `hidepid=2`,否则 `/proc/environ` 让凭据最小化手段失效。这需要容器编排层支持，纳入镜像/编排设计。
6. rootless 性能开销(低):fuse-overlayfs 与 slirp4netns/pasta 有 IO 与网络开销，对长时编译/测试型 agent 任务可能有感，需实测是否可接受。
7. GitHub 对象存储域(低):release 资产 / LFS / Actions 构件可能落到 `*.githubusercontent.com` 等额外主机，白名单需按真实流量观察(可用 rano 这类工具)补齐，避免误伤正常 git 操作。

## 主要参考来源

- Claude Code 官方网络配置(必需域名、可关遥测、代理/CA/mTLS):https://code.claude.com/docs/en/network-config
- gh-aw-firewall api-proxy-sidecar(代理注入凭据三容器模式):https://github.com/github/gh-aw-firewall/blob/main/docs/api-proxy-sidecar.md
- gh-aw-firewall 同 UID /proc/environ 暴露 issue(#1771 等):https://github.com/github/gh-aw-firewall/issues/1771
- gh-aw-firewall sidecar 无按调用方鉴权 issue #3022:https://github.com/github/gh-aw-firewall/issues/3022
- microVM vs 容器信任边界(Fly.io):https://fly.io/learn/microvm-vs-container/
- openclaw-plugin-claude-code(Podman rootless 优先):https://github.com/13rac1/openclaw-plugin-claude-code
- Claude Code 沙箱嵌套配置与容器内运行(moksaweb / FluidFramework DEV.md / Qovery):https://moksaweb.com/claude-code-sandboxing/
- rootless Docker 隐性权衡(Ken Muse):https://www.kenmuse.com/blog/rootless-docker-and-its-hidden-security-trade-offs/
