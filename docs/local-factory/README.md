# Set up the local factory

Machinist connects GitHub to Codex or Claude Code running on your computer, a VM, or Kubernetes.
No Machinist cloud account is required. The control plane can also run in your
[GKE cluster](cloud-control-plane.md); agents still execute on infrastructure you control.

## First run

```sh
just build
./bin/machinist init
./bin/machinist start
```

Open the printed local URL. Setup guides you through four steps:

1. **GitHub:** sign in with the Machinist Local GitHub App and install it on the
   repositories you choose. No token paste or client secret is needed.
2. **Repository:** choose an accessible repository. Machinist can clone it or
   verify an existing checkout. It checks the App permissions and your write access.
3. **Agent:** choose Codex or Claude Code on this computer or a remote VM. A configured
   Kubernetes target runs Codex in isolated gVisor Jobs.
4. **Verify:** run a real PING with no GitHub issue, commit, or PR. Setup completes
   only after it returns PONG. Local workers start automatically after setup.

Install Git, GitHub CLI, and the selected coding CLI first. Sign in to the coding
CLI on its execution host. Claude Code requires version 2.1.248 or later for
restricted Triage and Review; older versions receive an upgrade error before
starting an agent. See the [Claude CLI reference](https://code.claude.com/docs/en/cli-reference). Leave model selection unset to use the CLI default.
The CLI needs a writable runtime directory. If launching inside another agent's
restricted environment, use a separate writable provider runtime or start
Machinist from your normal terminal.

Settings lets you return to setup. Changed repository/agent selections invalidate
the earlier check. Agents are shared configurations, independent of repositories. Edit their Markdown
prompts in **Agents**; choose a project when starting a job. Saves apply to future
attempts across all projects. Running attempts retain their prompt snapshots, and
stale saves are rejected. Prompt edits stay in Machinist and never become issue
comments or commits just because you pressed Save.

On upgrade, each shared prompt is seeded once from the first repository with a
saved prompt, ordered by repository name. Other legacy prompt rows are retained
in the private database for recovery; they no longer override shared agents.
Existing shared edits always take precedence on subsequent restarts.

After setup, add more repositories from **Projects → Add project**. Grant access
through **Manage GitHub access** and refresh if another repository is missing.
Adding a project preserves the existing projects and shared agent settings.

## GitHub App ownership

The default public App is [Machinist Local](https://github.com/apps/machinist-local).
Its public client ID is included in Machinist. Device Flow is enabled. Clients do
not receive the App owner's private key or a shared client secret.

GitHub requires the App owner to generate a private key once before its first
installation. That is an owner administration step, not a key that ordinary users
supply to Machinist. Keep it private and outside repositories. To use a different
App, use **Use your own GitHub App**, enter its public client ID and installation
URL, and enable Device Flow in GitHub's App settings.

Required repository permissions are Contents, Issues, and Pull requests read/write,
Checks and Commit statuses read, plus mandatory Metadata read. No organization
administration, repository administration, secrets, or workflow editing permission
is requested. Permissions are still subject to repository rules and organization
approval.

App access tokens and refresh tokens are saved locally in a private directory.
Local agent Git/gh processes receive the refreshed token through their environment.
The Git credential helper limits normal Git authentication to the selected
repository, but the API token can access every repository granted to the App.
Local and VM agents are trusted with their host account permissions; their
checkout selection is not a security sandbox. Use Kubernetes when agents must
run without GitHub credentials. Tokens are never
written to remote URLs, executor arguments, or issue comments.

## Remote VM

Choose **Remote VM** during setup. Install Machinist, Git, GitHub CLI, and the coding
CLI on the VM, then authenticate GitHub and the provider there. Download the private
worker setup script from Machinist. It contains a worker connection credential;
keep it private and remove the downloaded copy when no longer needed.

Run the displayed SSH tunnel command **on the VM** to reach this computer. The
foreman host must be reachable over SSH with an appropriate account. The forwarded
VM endpoint is `http://127.0.0.1:17341`. Machinist remains bound to loopback and does
not enable SSH or provision a VM. Run the setup script on the VM to create private
worker configuration, clone the repository using VM credentials, and start the
worker. Then run verification in the browser. It checks VM repository identity,
Git/gh write access, and a real read-only agent PONG before allowing setup to finish.

The VM does not receive the local GitHub App token. It uses its own host credentials.
If SSH is initiated from this computer instead, use reverse forwarding to the VM
rather than copying the displayed VM-to-foreman command unchanged.

## Tasks and GitHub updates

A plain request creates a local task. Paste an existing issue URL to connect it to
GitHub. PING and other informational requests can finish in Triage. Code work runs
through optional Spec and approval, Implement, and independent Review. Published
specifications and implementation use one branch and PR.

GitHub owns issues, published specs, code, PRs, and review. Machinist's local SQLite
database owns scheduling, prompt snapshots, attempts, approvals, feedback, and
receipts. Linked issues receive one concise status comment updated in place.
Internal prompts, JSON events, logs, and handoffs are never posted to issues.
There is no local specification viewer or Markdown artifact list in factory tasks.

Review findings return to the same PR for at most three repair rounds. The
[GitHub follow-up watcher](github-followups.md) handles qualifying CI/review repairs
and verifies confirmed merges. You can also use **Check merge status**. Neither
green CI nor an approval alone completes a delivery.

Merge manually by default. Optional
[repository maintenance](automations.md#repository-maintenance) can auto-merge only
small, independently reviewed Machinist documentation PRs that meet its fixed
policy and branch protections. Single-agent deliveries remain ineligible.

## Recovery and compatibility

Back up the full private runtime with a consistent SQLite snapshot, including
credentials, configuration, checkouts and worker outputs. Restart preserves
history and interrupts previously running attempts. Confirm the old process stopped
before retrying. Published branches allow workspace recovery without provider-session
memory. One foreman owns each local database.

New installations use `server.state = "factory"`; the older `github` value remains
an alias. Runtime files live beside the worker token, normally under
`~/.machinist/server`. Existing source definitions and credentials are preserved by
`init`. Legacy direct/sequence modes remain available explicitly.

Old GitHub journal comments are not deleted or imported automatically. They remain
historical. Re-adopt an issue explicitly to begin a new local task. This avoids
silently resuming work while migrating away from comment-based persistence.

Saved [automations](automations.md) run a single agent on demand or on a schedule,
from the repository root. GitHub follow-ups run automatically for delivered Machinist PRs. Webhook relays
and live log streaming are not required for this polling-based integration.

### Issue labels

For linked issues, Triage receives the existing repository label names and may suggest up to ten exact classification labels in `factory.labels`. The foreman validates those suggestions against the current catalog and adds matching labels through the GitHub App API. It never creates classification labels or removes existing human labels. Local tasks make no label requests.

The foreman owns only `machinist:needs-spec`, `machinist:ready`, and `machinist:blocked`. It creates these on demand and replaces only these three labels as the route changes. Other `machinist:*` labels are preserved. Terminal tasks and PRs awaiting merge have no routing label. Label publication is best effort after local execution state is saved; a later task transition retries reconciliation. GitHub failures do not discard agent results.

### Deliverables use pull requests

Every requested file, including reports, documents, and HTML pages, is committed on the assigned task branch and delivered through its PR. Use existing repository paths, or `docs/reports/<task-name>.<extension>` when no convention exists. Document-only work follows Implement and independent Review; it does not require a production code change. Plain questions and health checks without a requested deliverable can still finish in Triage. Internal agent handoffs stay local and are not committed as deliverables.

### Dashboard

Dashboard is the default page and the first navigation item. The older `#/analytics` link opens it too. Current workload spans all dates: active jobs, PRs ready for review, and pending approvals, blockers or failures. Counts open matching run filters; entering Runs from another page clears an unrelated search.

The 7/30-day activity chart counts jobs by creation day in your browser's timezone. Outcomes, recent jobs, timed execution and reported tokens use that same creation window. Current runner availability and schedules stay independent of the time window. Missing token usage is shown as unreported, without inferred costs.

### GitHub follow-ups

Machinist watches PR-ready jobs for CI failures, actionable trusted review feedback,
and confirmed merges. Repairs stay on the same task, branch and PR. See
[GitHub follow-ups](github-followups.md) for the repair flow, completion rules,
and cases that need human attention.
