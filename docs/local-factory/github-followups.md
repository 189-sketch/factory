# GitHub follow-ups

Machinist watches recorded PRs for jobs awaiting merge (PR ready). Keep the control
plane running and GitHub connected. It checks up to ten PRs per minute, rotating
through larger sets. Active, blocked, cancelled and publication-held jobs are
excluded; open draft PRs do not trigger repairs.

## From feedback to the same PR

For standard jobs, the flow is:

**PR ready → qualifying CI or review feedback → Build → independent Review → PR ready**

Repairs reuse the same task, branch and PR. Single-agent jobs retain their existing
policy and do not gain an independent Review stage.

The following signals can start a repair:

- Completed failing checks or commit statuses reporting failure or error on the
  recorded PR head. Pending, skipped and cancelled checks do not start repairs.
- A trusted reviewer's latest applicable change-request review, with nonempty
  feedback on the recorded head, or nonempty feedback from the originating
  reviewer in an unresolved, current inline review thread. Trusted reviewers are
  authenticated repository owners, members and collaborators.

General PR discussion, untrusted authors, resolved or outdated threads, and
implementer replies do not dispatch repairs. Feedback is evidence for the agent
to assess, not permission to change workflow rules. Consumed signals are saved,
so repeated polls and restarts do not repeat a repair. The normal limit of three
repair rounds applies; exhausting it blocks the task for human attention.

## When the task completes

Merge the PR on GitHub when it is ready. The follow-up watcher never merges PRs.
It completes the task only when the PR head matches Machinist's recorded head,
GitHub reports the PR merged, and a merge commit is confirmed. The merge commit
does not have to share the PR head's SHA. Green CI or an approval alone does not
complete the task.

## When to intervene

- **The PR head changed outside the task:** Machinist blocks the task. Inspect the
  new commit before retrying.
- **The PR was closed without merging:** Machinist blocks the task. Reopen the PR
  or cancel the task; reopening alone does not automatically resume blocked work.
- **A publication hold is active:** automatic follow-ups and dispatch are
  prevented. Retries and prompt edits cannot release the hold. Review the content
  and destination, then use the dedicated release action described in
  [Recovery and privacy](local-jobs.md#recovery-and-privacy).

Incomplete GitHub evidence or a transient API failure preserves the current task
state and retries automatically, with a notice on the run page. You can inspect
the PR on GitHub while waiting.
