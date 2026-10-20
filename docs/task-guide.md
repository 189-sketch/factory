# Run your first job

Complete [setup](local-factory/README.md) and verify PONG before submitting work.
The selected project determines where the job runs. Agents are reusable
configuration, independent of the repository.

## Submit a request

1. Open **Runs → New run**.
2. Describe the requested outcome and how to check it, or paste a GitHub issue URL.
3. Choose a connected project and run mode, then submit.

Use **Standard pipeline** for a repository change that needs independent review.
Triage decides whether a specification is necessary. A small, clear fix normally
goes directly to Build. A plain question can finish in Triage without creating
an issue or PR.

For example, ask: “Add a link in the README to an existing guide so new users can
find it. Change only the navigation table and verify the link resolves.” This
requests a file change, so delivery requires a PR even though no code changes.

Use **Single agent** for an operational task, such as checking a package and
reporting proven bugs without changing files. It has no separate reviewer. Saved
[automations](local-factory/automations.md) use this mode too.

## Follow the stages

The runs table shows stage dots; open a run for named stages, attempts, activity,
and GitHub links. Heartbeats indicate that the worker is connected, not that the
agent has completed its work.

If Triage requests Spec, read the specification and draft PR on GitHub. Approve
that attempt or request changes in Machinist. Build reuses the branch and PR.
Independent Review checks the published revision. Definite findings return to
Build for at most three repair rounds.

Internal handoffs and prompts stay private. Requested documents and reports are
repository changes delivered through the PR, not a list of local spec files.

## Complete delivery

For a PR-ready job, inspect the diff, review and current CI on GitHub. Merge when
ready. Machinist's [follow-up watcher](local-factory/github-followups.md) confirms
the recorded head was merged before completing the job; **Check merge status**
performs an explicit check too. Green checks alone do not complete it.

Optional [repository maintenance](local-factory/automations.md#repository-maintenance)
can auto-merge a narrow class of independently reviewed Machinist documentation
PRs under enforced branch protections. Other PRs still need human merge.
Single-agent deliveries never qualify for this policy.

## Recover a failed job

Read the failure and its stage before retrying. Resolve missing credentials,
configuration or task requirements first. After cancellation, restart, or a lost
worker, confirm the old process and any Kubernetes Pods have stopped. Retry
creates another attempt and preserves history; it does not resume a provider
conversation or promise exactly-once external actions.

Publication-held jobs need a separate, explicit release after reviewing their
content and destination. Retry and prompt editing cannot release a hold. See
[job recovery and privacy](local-factory/local-jobs.md#recovery-and-privacy).

For API submissions, use a stable `client_request_id` and the
[jobs API contract](local-factory/local-jobs.md#two-built-in-modes). Lower-level
custom sequences are documented separately in [Workflows](workflows.md).
