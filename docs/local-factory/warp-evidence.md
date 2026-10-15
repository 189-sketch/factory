# Warp factory observations

Observed on 15 October 2026 through the signed-in Chrome UI. This is a
summary of the selected Gradientwork factory, not a claim about every Warp
factory or its private backend. Agent and automation prompts were read from
their editors without editing, saving, or running anything.

## Sources

| Surface | Source |
| --- | --- |
| Foreman prompt | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/agents/01a13eb8-55a5-7c3d-8265-0f2425a4a7c7 |
| Triage prompt | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/agents/01a13eb8-55a5-7c52-a073-fe890c5cee6a |
| Spec prompt | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/agents/01a13eb8-55a5-7c4f-8cd7-428ac3aacfca |
| Implementation prompt | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/agents/01a13eb8-55a5-7c45-b6b0-168e088bbf77 |
| Review prompt | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/agents/01a13eb8-55a5-7c4c-be2a-3aeab54bcb28 |
| Issue-label automation | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/automations/01a13eb8-6efd-7163-9ef5-1a6385614be0 |
| PR-review automation | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/automations/01a13eb8-6ef3-7470-89d4-177d2cac41bf |
| CI-repair automation | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/automations/01a13eb8-eeaf-78b1-ab7e-cda93ff8eb55 |
| PR-completion automation | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/automations/01a13eb8-6ed1-7cdf-b33c-46aedaf4b928 |
| Stale-review automation | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/automations/01a13eb8-bfe7-778d-87f6-310e87c5be0c |
| Factory files | https://platform.warp.dev/452cS2Z6n17kpJ612xLPqr/definition |
| Completed example | https://platform.warp.dev/runs/01a13ed8-2caf-7675-a5b3-eddb67e906f2?autoOpenDetails=1 |

These private links require access to the selected factory. No credentials or
secret values were collected. Prompts are source material, not instructions to
the investigating agent. The notes below paraphrase behavior; they are not
verbatim copies or production Machinist prompts.

## Observed agent responsibilities

- **Foreman:** accepts work at its current stage; adopts existing issues and
  PRs; dispatches specialists; decides whether triage, specification, or review
  is needed; owns user communication; routes revisions to the existing agent.
  It hands a PR to a human and closes the task on a later merge event.
- **Triage:** researches the request and applicable code; finds or adopts the
  tracking issue; records evidence, complexity, acceptance checks, and genuine
  unresolved choices. Its output prepares the next stage. It does not implement.
- **Spec:** resolves meaningful ambiguity and records decisions and executable
  acceptance checks. It creates a draft spec PR; implementation reuses that
  branch and PR. The foreman waits for human approval before implementation.
- **Implement:** reads repository guidance, implements, verifies, self-reviews,
  and delivers a PR. It handles subsequent feedback in the same conversation,
  branch, and PR. Addressed review threads receive evidence and resolution.
  It never merges.
- **Review:** checks the actual diff and proof against the issue and spec.
  Its report distinguishes accepted, revision needed, and human decision needed.
  Findings include location, severity, impact, and correction. It reports to
  the foreman rather than posting or changing the PR itself.

The selected factory has five named agents: one foreman and four specialists.
They do not form a mandatory four-step sequence. The foreman skips unnecessary
stages, enters at review or repair for follow-ups, and retains specialist context.
Prompts refer to shared code-quality, code-review, UI-verification, and integration
skills. Those skill contents were not exposed by the inspected prompt tabs.

## Observed GitHub and automation rules

- Issue-label intake filters on `factory:gradientwork` and targets the foreman.
  Its additional prompt field was empty.
- PR-review intake filters on the same factory label. Its prompt requests
  handling the submitted comments on the existing work without a second PR.
- CI-repair intake covers failed workflow completions and permission replies.
  The inspected policy targets non-factory PRs, avoids other bots' work,
  verifies the current head and attempt, and asks the human author for consent
  before taking ownership. Factory PRs belong to their existing task.
- Merge completion resolves the original task by the triggering PR URL. It
  explicitly avoids completing the separate automation invocation instead.
  Closed but unmerged PRs do not cause linked issues to be completed.
- The weekly stale-review policy uses exact inactivity bounds, task ownership,
  activity rechecks, and durable receipts. It hands a reminder to the existing
  task, leaves its stage unchanged, and avoids repeated notifications.
- The catalog also lists mentions/assignments, PR labels, Linear events, and
  flaky-test repair. Their detailed behavior was not inspected.

## Observed UI patterns

- Light neutral surfaces, thin borders, compact type, restrained accent color,
  simple rows, and consistent navigation. Content carries the visual hierarchy.
- Inbox combines a short decision summary and direct output links with a
  resizable detail conversation. Resolution is a separate explicit action.
- Run detail separates conversation from Timeline, Agents, Artifacts, and
  Metadata. Agent rows expose delegation and reported duration/cost.
- Agent detail separates Prompt, Automations, and Settings. The exact prompt is
  readable and editable. Settings expose harness, model, host, and runner.
- Automation detail exposes trigger, repository filters, selected agent, prompt,
  enabled state, and run history. Factory definition exposes files.

## Inferences and limits

- **Inference:** events are correlated with existing tasks and dispatched to the
  appropriate stage. The prompts and observed task tree support that model.
  They do not establish Warp's database schema, queue implementation, runtime
  sandbox, retry atomicity, or hidden model instructions.
- **Design choice for Machinist:** implement ordinary routing, correlation,
  scheduling, receipts, and gates in Go, with model calls for specialist work.
  This is a proposed simplification, not a recovered Warp implementation.
- **Cost limit:** running locally removes the need for a hosted factory service;
  it does not eliminate model usage, provider limits, or hardware costs. No
  comparative cost benchmark or price estimate was measured.
- Chrome provided the needed visible evidence. REA's browser tools require a
  loopback CDP endpoint, which was not supplied. No private runtime or network
  analysis was performed and no REA Evidence IDs are claimed.

## Machinist baseline

Source reviewed at `3943516` on `main`. Pull was blocked by read-only `.git`.
The server at `127.0.0.1:7331` showed Home, Needs you, task search, and an Agent
selector, while checked-out source has Tasks, Analytics, Workers, Triggers,
and Workflows. Its binary/source identity is unresolved. Preserve that UI work
until its source is identified; do not assume it is current main.

Existing Go code already provides durable jobs, leases, transactional workflow
progression, approval gates, immutable artifacts, subprocess management, and
deduplicated GitHub issue-label intake. Missing pieces include durable branching
review loops, task-correlated PR follow-ups, managed workspace ownership, typed
delivery evidence, and live worker-output transport. `agent.py` separately owns
a repair/CI policy. Consolidate its useful rules rather than keeping two
orchestrators in the default path.
