# Deliver work with your local agent

Machinist runs your own coding agent on a computer you control and turns a task
into one pull request. This guide explains where the agent runs, how it reaches
GitHub, what stays private, and how the shipped `task-to-pr` command delivers
work.

## Your agent runs on your host or VM

Machinist runs on your machine. A command maps to a fixed executable on the
worker, such as Codex or Claude Code. Machinist sends the rendered prompt on
standard input and runs it in the repository's directory.

- **This computer.** Build Machinist, run `machinist init`, and run a command
  with `machinist run`. See the [quick start](../README.md#quick-start).
- **A remote VM.** Run the control plane and worker as services on a VM you own.
  See [VM deployment](vm-deployment.md).

Sign in to the coding CLI on the host where it runs. Do not copy credential
files from another computer.

## GitHub access uses the host's own logins

Machinist does not hold GitHub credentials for you. The agent uses the GitHub
CLI and Git credentials on the host:

- GitHub CLI authentication (`gh auth login`) for issues, pull requests,
  comments, and checks.
- A Git credential, such as a GitHub SSH key created on that host, for clone,
  fetch, and push.

Use a GitHub identity with access to only the repositories the worker needs.
Managed tasks can use only the repositories named in the worker configuration,
such as `[repositories.my_repo]` in `~/.machinist/worker.toml`.

## Prompts and run history stay local

The control plane stores jobs, rendered prompts, runs, and workflow attempts in
a local SQLite database. By default it is `~/.machinist/server/machinist.db`.
Saved task files are kept on the same server's disk. See
[configuration](configuration.md#artifact-storage-directory).

Machinist does not post prompts or run logs to GitHub. What reaches GitHub is
what your command's agent pushes or posts.

## One pull request per task

`machinist init` writes `~/.machinist/config.toml` with a `task-to-pr` command.
Its prompt, [`examples/prompts/task-to-pr.md`](../examples/prompts/task-to-pr.md),
tells the agent to:

1. Read the issue or plain request and the repository instructions.
2. Work on an isolated branch, or reuse the existing branch and pull request for
   the task.
3. Commit the change, push the branch, and create or update one pull request.
   For an issue, the body includes `Fixes #<issue-number>`.

A requested document is part of the change, so it is committed on the branch
and delivered in the same pull request as code would be.

## Independent review and human merge

Before pushing, `task-to-pr` asks a fresh read-only subagent to review the
change and repeats until the change is approved. After pushing, it waits for CI
and automated reviews, fixes valid findings, and stops after three repair rounds.

The prompt says never to merge. You review the pull request and merge it on
GitHub yourself.

Machinist runs the steps you configure. It does not add review or merging to
every task. To split work into separate stages or add an approval before a
step, configure a [workflow](workflows.md). Merging stays with a person unless
you add a merge policy, such as the
[classify-then-merge example](../examples/workflows/risk_delivery/README.md).

A local factory with GitHub App sign-in is in preview in
[PR #514](https://github.com/owainlewis/machinist/pull/514) and is not on `main`.
