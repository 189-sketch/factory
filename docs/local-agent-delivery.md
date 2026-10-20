# Deliver work with your own coding agent

Machinist runs coding agents on your computer, a VM, or a configured Kubernetes
cluster. Start with the [setup guide](local-factory/README.md), then submit a
request through **New run** or the API.

## Connect projects and GitHub

Sign in through the GitHub App and grant access to the repositories you want to
use. Add more repositories from **Projects → Add project**. Each project has its
own checkout and uses the shared agent and execution target.

Local agents receive a refreshed GitHub App token. The Git credential helper
limits normal Git authentication to the selected repository; the API token still
has access to all repositories granted to the App. Local and VM agents are trusted
with their host account permissions, so project selection is not a security sandbox. On a VM, the worker uses the GitHub login
configured on that host. Kubernetes agent pods receive no GitHub credentials;
the trusted connector publishes their changes. See
[Kubernetes setup](local-factory/kubernetes.md).

## Choose the run mode

**Standard pipeline** starts with Triage, adds Spec and human approval when
needed, then runs Build and independent Review. Each stage uses a fresh agent.
Review findings return to Build on the same branch and pull request, for up to
three repair rounds.

**Single agent** completes a task end to end without a separate reviewer. Saved
[automations](local-factory/automations.md) use this mode from the repository root.

Requested files, including reports and documents, are committed on the task
branch and delivered through one pull request. A question or health check that
requests no file changes can return its answer directly.

## Keep internal work private

The control plane keeps job state, prompt snapshots, approvals, feedback, and
receipts in its private SQLite database. Factory jobs use `jobs.db` beside the
worker token, normally under `~/.machinist/server`. Back up the whole runtime
directory, including worker outputs.

GitHub holds linked issues, published specifications, code, and pull requests.
Linked issues receive a concise status comment updated in place. Internal
prompts, logs, and handoffs are never posted to issues or committed as requested
deliverables.

## Review and merge

Inspect the pull request, agent review, and CI checks on GitHub. You decide when
to merge. **Check merge status** verifies that the reviewed head was merged before
completing the task. The [GitHub follow-up watcher](local-factory/github-followups.md)
also confirms merges and handles qualifying CI/review feedback. Optional
[repository maintenance](local-factory/automations.md#repository-maintenance) can
auto-merge qualifying independently reviewed factory documentation PRs. Single-agent
and unrelated PRs remain ineligible.

Direct CLI commands remain available for scripts. `machinist run` invokes the
configured command in a repository you supply; its prompt determines its work.
See the [command reference](workflows.md) for this lower-level interface.
