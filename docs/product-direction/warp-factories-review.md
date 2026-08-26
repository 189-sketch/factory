# Warp Factories product review

> **Review date:** 2026-08-26

This review compares Factory with the public Warp Factories product and examples. It is
a direction-setting document, not a claim that Factory should copy Warp feature for
feature. Factory is a local, personal system. Warp is a hosted team platform in early
access.

## The main lesson

Warp's strongest idea is not any single agent or integration. It is the separation of five
things that often get mixed together:

1. A work item keeps one identity from intake to human handoff.
2. Agents own durable roles such as triage, specification, implementation, and review.
3. Skills package reusable procedures and domain knowledge.
4. Runtime configuration grants repositories, tools, secrets, models, and compute.
5. Scorers and metrics measure the result without silently changing production policy.

Factory already has a sound execution core and the same control-plane versus execution-
plane split. Its next useful step is to make agent knowledge composable with skills. It
should not make prompts responsible for permissions, work-item identity, or evaluation.

## Product comparison

| Area | Warp Factories | Factory today | Direction |
| --- | --- | --- | --- |
| Unit of work | A work item keeps source context and history across agent runs and human follow-ups. | A job stores one input prompt and one agent or ordered pipeline. | Add durable work-item identity only after the local execution core and skills are stable. |
| Coordination | One foreman chooses the shortest useful path through specialist roles and can revisit existing conversations. | The default foreman coordinates native subagents inside one executor run. Configured pipelines are fixed and sequential. | Keep dynamic policy in the foreman. Do not turn every role into a mandatory pipeline stage. |
| Agent roles | Foreman, triage, spec, implement, and review have distinct responsibilities. | Foreman and audit ship by default. User-defined agents can be added. | Add specialist agents when they create a real quality or permission boundary. Start with review and test knowledge as skills. |
| Definitions as code | A versioned tree owns factory, agent, automation, runner, scorer, and skill definitions. | TOML and Markdown files own agents, pipelines, executors, and repositories. | Keep files as the source of truth. Add schema validation and deterministic hashes as the format grows. |
| Skills | Shared skills apply to all agents. Agent-scoped skills apply to one role. Each skill is a `SKILL.md` with metadata and optional support files. | Reusable procedures must be copied into full agent prompts or discovered independently by the chosen harness. | Add shared and agent-scoped skills to Factory's shared definition. Compile them into the final prompt so every executor behaves consistently. |
| Models and harnesses | Each agent may choose a model, harness, runner, and host. | Each agent chooses an executor. A run may request a model alias; workers own model mappings. | Preserve worker-owned model resolution. Consider an agent default model only if it remains a logical alias, not a provider credential or raw worker command. |
| Execution | Hosted runners or self-hosted workers provide isolated compute. | Workers execute against pre-existing local worktrees with their local credentials. | Local-first is a feature. Add sandbox profiles only when Factory can enforce them across executors. |
| Intake | Slack, GitHub, GitLab, Linear, Jira, API, MCP, direct runs, and schedules can create work. | CLI and the local web form submit jobs. Foreman tasks point at GitHub issues. | Add one intake source at a time. Preserve source identity and deduplicate deliveries before supporting broad event triggers. |
| Access control | Integrations, repositories, agent secrets, MCP servers, and runtime identity define access. Filters only route work. | Worker configuration and its process environment define access. Prompt text guides behavior. | Keep this boundary explicit. Skills and intake filters must never imply more authority than the worker has. |
| Observability | Work-item stages, runs, PR links, autonomy, cycle time, cost, and scorer results are visible. | Jobs, runs, workers, duration, optional token usage, results, and event logs are visible. | Add outcome and PR identity before adding more aggregate charts. Duration and tokens without outcome quality are incomplete. |
| Evaluation | Focused scorers classify completed conversations. Benchmarks compare configurations. Self-improvement proposes reviewed changes. | A Python eval checks the default foreman's GitHub label lifecycle. | Keep deterministic evals as the first line of proof. Add narrow scorers only for facts not cheaply proved from state. Never auto-apply prompt or skill changes. |
| Human gates | Humans approve ambiguous specs and decide merges. Permissions enforce merge policy. | The foreman stops for missing decisions and never merges. | Keep the merge boundary. Treat prompt policy as convention and repository protection as enforcement. |

## What Factory should adopt

### 1. Shared and agent-scoped skills

This is the highest-leverage near-term addition. The current prompts are already careful,
but reusable procedures such as repository conventions, testing standards, GitHub review
handling, and release checks will otherwise be repeated across agents. The proposed
format and lifecycle are in [`../worker-skills/design.md`](../worker-skills/design.md).

### 2. One durable identity above jobs

Warp's work item survives triage, implementation, review, follow-ups, and local handoff.
Factory's job is currently an execution record, which is simpler and correct for V1. If
Factory later accepts events or follow-up messages, it should add a work item above jobs
instead of stretching one job row into both a conversation and an execution attempt.

The identity should bind the source type and source ID, such as a GitHub issue URL, to a
stable Factory ID. Repeated deliveries should continue the same item or be rejected as
duplicates. This decision needs its own design before implementation.

### 3. Outcome-aware measurement

The control plane measures duration and executor-reported token usage. These describe
effort, not quality. The next metric should be a durable outcome that can be tied to a
pull request and its verification state. Useful first measures are:

- jobs that reached a review-ready pull request;
- jobs that stopped for a human decision;
- jobs blocked by infrastructure or expired waiting;
- repair attempts before handoff;
- time from admission to review-ready handoff.

Only after these are reliable should Factory derive autonomy or cost-per-PR views.

### 4. Reviewed self-improvement

Warp uses scorer failures to propose changes for review rather than silently rewriting
its factory. Factory should keep the same human gate. Evals may identify a weak prompt or
skill and open a proposed change, but production definitions should change only through a
normal reviewed commit or an explicit local file edit.

### 5. Source-aware intake, later

Broad integrations add more than webhook parsing. They require authorization, event
deduplication, source-thread continuity, reply routing, and clear trigger versus access
semantics. Factory should add integrations only after work-item identity exists. A small
GitHub issue intake path is more useful than a generic automation language with weak
delivery guarantees.

## What Factory should not copy yet

- Hosted multi-tenant execution. The current goal is a trusted local factory.
- A large automation filter schema. There is no event intake layer to route yet.
- Provider-managed secrets or MCP catalogs. The worker already owns credentials and
  executor environment.
- LLM scorers for deterministic facts such as exit status, label state, or test results.
- Automatic skill discovery from arbitrary repository directories. That would make a
  checked-out task repository able to alter the factory's durable operating policy.
- A mandatory five-stage pipeline. Small, clear work should keep taking the shortest safe
  path.

## Current readiness assessment

Factory is structurally ready to replace a personal, local older factory after the known
release gates below are cleared. Its process supervision, bounded artifacts, durable
managed state, lease recovery, and worker trust boundary have focused tests and passed
the race-enabled suite during this review. The documentation and clean-checkout build fix
from pull request #21 is merged, and there are no open pull requests or issues at the time
of this review.

Release gates:

1. Rebuild and reinstall Factory with Go 1.26.6 or newer. Go binaries include the standard
   library used at build time, so upgrading Go without rebuilding the binary does not
   clear this gate. `govulncheck` found six reachable standard-library vulnerabilities in
   the review machine's Go 1.26.4 toolchain, all reported fixed in 1.26.6.
2. Add a required CI workflow before treating pull-request review checks as merge proof.
   The repository currently has local `just check` coverage but no checked-in GitHub
   Actions workflow.
3. Back up `~/.factory/config.toml`, `worker.toml`, agent prompts, the worker token, and
   the SQLite database before replacing the older installation. The installer preserves
   existing files and therefore is not a migration tool.

These are operational gates, not reasons to redesign the execution core.

## Recommended order

1. Clear the three readiness gates and use the current factory for real work.
2. Implement shared and agent-scoped skills from the proposed design.
3. Collect several real runs and refine the skills from observed failures.
4. Design durable work items and follow-ups if CLI and web submission become limiting.
5. Add outcome metrics and narrow evaluation around the workflow that actually emerges.
6. Add one source-aware integration only when it removes repeated manual intake work.

## Sources reviewed

- [Warp Factories overview](https://docs.warp.dev/factories/)
- [How Warp Factories work](https://docs.warp.dev/factories/how-factories-work/)
- [Factory agents](https://docs.warp.dev/factories/factory-agents/)
- [Factory definition syntax](https://docs.warp.dev/factories/factory-as-code/)
- [Infrastructure and security](https://docs.warp.dev/factories/infrastructure-and-security/)
- [Connect your factory](https://docs.warp.dev/factories/connect-your-factory/)
- [Automation filters](https://docs.warp.dev/factories/automation-filters/)
- [Factory API](https://docs.warp.dev/factories/factory-api/)
- [Factory MCP](https://docs.warp.dev/factories/factory-mcp/)
- [Factory dashboard](https://docs.warp.dev/factories/factory-dashboard/)
- [Measure and improve](https://docs.warp.dev/factories/measure-and-improve/)
- [Troubleshooting](https://docs.warp.dev/factories/troubleshooting/)
- [Skills for agents](https://docs.warp.dev/agents/capabilities/skills/)
- [Warp Factory examples](https://github.com/warpdotdev/warp-factory-examples)

The Slack, GitHub, GitLab, Linear, and Jira factory integration guides were also reviewed
for their intake, identity, permission, and trigger behavior.
