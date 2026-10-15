# Architecture

Machinist is a local foreman for four coding specialists: Triage, Spec, Implement,
and Review. Deterministic Go routing owns gates and repair limits. Codex or Claude
Code processes perform the work on a local host or remote VM.

## Ownership

- GitHub issues and trusted journal comments own factory tasks, attempts, progress,
  approvals, feedback, prompt snapshots, and results.
- GitHub branches and pull requests own published specifications and code.
- `.machinist/agents/{role}.md` in GitHub owns edited role prompts. Missing files
  use bundled defaults; prompt saves use Contents API SHA concurrency checks.
- Host configuration owns repository aliases, checkout paths, executor argument
  arrays, model aliases, credentials, and timeouts. Worktrees and logs are scratch.

`internal/controlplane/github_factory_server.go` implements the default GitHub
backend. It reconstructs state through the pure reducer in
`github_factory_state.go`, before leasing work. Each event references the preceding
trusted comment. Cached state and worker leases are held in memory. Exactly one
foreman serves a repository with one stable GitHub identity. Multiple local or
remote workers may connect. GitHub comments do not provide a distributed lock.

## Execution

`internal/managedworker` resolves only approved executor and repository aliases.
It creates an isolated task worktree and a detached review snapshot.
`internal/runner` supplies prompts on stdin, bounds execution, and terminates
process trees on cancellation or timeout. Factory results come from the agent's
final JSON response, without local report-file publication.

Triage can answer informational requests without repository changes, or route code
work onward. Triage may skip Spec. Spec pushes a document and draft PR, then waits for approval
of that attempt. Implement reuses the branch and PR. Review checks the exact
published head using bounded host-collected diff and GitHub check evidence.
Findings trigger at most three repair rounds on the same PR. Accepted review waits
for human merge and an explicit verified merge check. A changed head cannot use
the earlier review. No factory agent merges automatically.

The worker validates actual clean Git state, pushed head, repository, PR identity,
and draft/ready status before accepting Spec or Implement evidence. Review requires
a configured read-only executor. GitHub source text is task data, not permission
to alter the host allowlist or executor policy.

After restart, running attempts become interrupted. Retry requires confirmation
that the old process stopped. The foreman persists transitions before dispatch or
acknowledgement; it fails closed when GitHub is unavailable.

## Interface and compatibility

The UI provides New task, Inbox, Runs, Agents, Automations, and Settings. Factory
views show concise summaries and links to GitHub stages, specifications, and PRs.
They do not expose local artifact lists or duplicate specification viewers.

`server.state = "github"` selects the factory backend without opening SQLite.
Legacy direct commands and sequence workflows retain their existing SQLite,
artifact, and result contracts under the legacy backend. No schema migration or
automatic conversion of legacy tasks is included. See the
[factory guide](docs/local-factory/README.md) for setup and current limits.
