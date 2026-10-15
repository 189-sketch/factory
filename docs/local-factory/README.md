# Run the local software factory

Machinist runs Triage, Spec, Implement, and Review through your own Codex or
Claude Code CLI. Workers run on your host or a remote VM. GitHub holds the task,
approvals, stage results, specification, and pull request.

## Setup

Run `just build` and `./bin/machinist init`. Existing configuration is preserved.
For an existing installation, copy the factory workflow and specialist commands
from `examples/config.toml`, then set `[server] state = "github"` and configure
`[github.repositories]` with logical aliases mapped to `owner/repo`.
Install the four prompt files from `examples/prompts` beside the definition.

Configure matching repository aliases and paths in the worker configuration.
Use `codex` or `claude` for writable stages and `codex-review` or `claude-review`
for Review. See `examples/worker.toml` for executor restrictions. The worker must
support the entire route. Both foreman and workers need working `gh` and Git
authentication for the configured repository.

Start the foreman with `machinist start --config=...` and workers with
`machinist worker start --config=...`. Use one foreman per repository and a unique
worker name on each host. The foreman listens on loopback; use the existing VM
deployment guide for a remote worker connection.

## Follow a task

New task creates a GitHub issue or adopts an existing issue URL in the selected
repository. Adopted issues receive the `machinist:task` label. Repeating intake
for the same issue reuses its task.

1. Triage researches requirements and chooses whether a specification is needed.
2. Spec pushes a tracked document to the task branch and opens one draft PR.
   Approve that revision through Inbox. Open spec takes you to GitHub.
3. Implement reuses the task branch and PR, runs checks, pushes the result, and
   marks the PR ready. Simple tasks skip Spec.
4. Review uses a detached snapshot of the recorded commit and host-collected
   PR metadata, diff, checks, and statuses. Findings return to Implement on the
   same PR, for at most three repair rounds.

The interface shows short summaries and GitHub links. It does not store or show
local `triage.md`, `spec.md`, or `implementation.md` artifacts. A specification
can exist in a temporary working checkout while being authored, but its published
GitHub revision is canonical.

An accepted review waits for human merge. Open PR shows the checks and review
on GitHub. Check merge status completes the task only when GitHub confirms that
the reviewed head was merged. Machinist never merges automatically.

## State and recovery

The issue and trusted journal comments are the durable workflow record. Only
comments authored by the foreman's authenticated GitHub account are reduced.
Keep that identity stable. Run one foreman per repository; this journal is not
a distributed lock for multiple foremen.

Agent prompts are edited in Agents and saved to `.machinist/agents/{role}.md`
in GitHub using commit-SHA concurrency checks. Bundled prompts are the fallback
when that file does not exist. Each stage journals its prompt snapshot before
execution. A later stage uses the then-current prompt.

Machine paths, credentials, executors, configuration, logs, and working checkouts
stay on the host. Connection caches and leases are disposable. Restart rebuilds
tasks from GitHub and interrupts previously running attempts rather than replaying
external effects. Before retrying an interruption, confirm the old process stopped.
Published task branches allow another worker to reconstruct a checkout.

Review requires a genuinely restricted executor. `read_only` declares worker
capability; it does not itself sandbox an arbitrary command. Evidence is bounded;
oversized diffs and unavailable checks block rather than silently passing.

The default GitHub factory does not open a local workflow database or publish
artifact snapshots. Legacy direct and sequence modes retain their existing SQLite
and artifact behavior when selected explicitly. Existing SQLite tasks are not
automatically migrated into GitHub issues.

This slice does not include automatic CI follow-ups, GitHub review-event routing,
merge polling, live output streaming, server pagination, or an automation editor.
The Automations page describes the configured behavior without implying these
features exist. Use New task for the GitHub factory.
