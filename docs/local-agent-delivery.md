# How Machinist delivers work with your local agent

Machinist runs your own coding agent, Codex or Claude Code, on a computer you
control. It connects the repositories you choose through a GitHub App, keeps its
internal working notes private, and delivers each task as one pull request that is
reviewed independently and merged by a person.

There is no Machinist cloud account and no hosted agent execution.

## Your agent runs on your host or VM

During setup you pick the coding CLI and where it runs:

- **This computer.** Machinist starts a local worker after setup completes.
- **Remote VM.** You install Machinist, Git, GitHub CLI, and the coding CLI on the
  VM and sign in there. The VM worker reaches Machinist through an SSH tunnel that
  you run. Machinist does not provision the VM or enable SSH.

Sign in to the coding CLI on the host where it runs. The agent uses that host's
login, and model selection defaults to the CLI's own default.

Setup ends with a real health check: the agent must answer PING with PONG. This
check creates no issue, commit, or PR.

## Repositories connect through a GitHub App

You sign in with the Machinist Local GitHub App and install it only on the
repositories you choose. You do not paste a token or client secret. Machinist
checks the App's permissions and your write access before it uses a repository.

The App asks for Contents, Issues, and Pull requests read/write, Checks and Commit
statuses read, and the mandatory Metadata read. It does not ask for repository
administration, organization administration, secrets, or workflow editing.

App tokens are stored locally in a private directory. Local agent processes get a
refreshed token through their environment, limited to the selected repository.
Tokens are never written to remote URLs, command arguments, or issue comments. A
remote VM does not receive this token; it uses its own GitHub credentials.

## Internal prompts and handoffs stay private

Machinist keeps scheduling, prompt snapshots, attempts, approvals, feedback, and
the handoffs between stages in a local database on the foreman host.

These are never posted to GitHub:

- Agent prompts
- Execution events and logs
- Internal handoffs between Triage, Spec, Implement, and Review

A linked issue gets one short status comment, updated in place. You can start a
task from a plain request with no issue at all, and Machinist will not create one.

## One pull request per task

A task moves through Triage, an optional Spec with your approval, Implement, and
independent Review. Plain questions and health checks can finish in Triage with no
PR.

Everything the task asks for is committed on the task's branch and delivered in a
single pull request. That includes code, and also requested documents such as
reports, guides, and HTML pages. A published spec uses the same branch and PR.
Work is never delivered only as a local file.

## Independent review and human merge

After Implement finishes, a separate Review run inspects the exact pushed commit
without changing files. If it finds problems, they go back to Implement on the
same PR, for at most three repair rounds.

Machinist never merges. When you are satisfied with the PR and its CI, merge it on
GitHub yourself. **Check merge status** then confirms that the reviewed commit was
the one merged before the task is marked complete.
