# Configuration

## App configuration

`machinist init` installs the default configuration under `~/.machinist`.
`server.state = "factory"` selects the current app; `github` is a compatibility
alias. Complete GitHub, project, agent and runner setup in the browser. Local
workers are managed automatically after setup. Use Projects to add repositories,
Agents to edit shared Markdown prompts, and Automations for saved jobs.

Agents are independent of projects. The app offers only Standard pipeline
(`factory`) and Single agent (`single`), not a custom workflow editor.

The default listener is `127.0.0.1:7331`; deployments may override it, including
the GKE overlay at port 7341. Runtime state lives beside
`server.worker_token_file`, normally `~/.machinist/server`. Protect and back up
that directory. Factory jobs use `jobs.db`, not the legacy artifact store below.
See [setup](local-factory/README.md), [job state](local-factory/local-jobs.md),
and [Kubernetes configuration](local-factory/kubernetes.md).

## Factory admission and capacity

Both limits are optional positive integers. Omitting them leaves admission
unlimited, as before. The repository limit applies to each project alias in the
factory app (not the legacy SQLite command scheduler):

```toml
[server]
max_concurrent_jobs = 4
max_concurrent_jobs_per_repository = 1
```

Eligible projects take turns at dispatch. Within each project, eligible jobs
are ordered by creation time, with job ID breaking ties. Unsupported jobs do not
block other eligible work. The round-robin cursor is local to the server process
and resets on restart. Replaying a live lease does not consume another slot or
advance the turn.

The built-in managed worker executes one job at a time. A single Kubernetes
Codex credential stream also executes serially, enforced by its existing
exclusive credential lock. Raising admission limits does not make one
subscription parallel. Multiple eligible, independently provisioned workers can
consume global capacity; do not clone credentials or remove locks to obtain
parallelism.

Cancelled or interrupted started attempts without confirmed shutdown still
occupy a slot and block new work in their repository and on the same named
worker. Expiring a heartbeat or cleanup-upload grant is not proof of shutdown.
The existing stopped acknowledgement or explicit stopped confirmation on retry,
publication access changes, or automation rerun releases the covered uncertainty.

`GET /api/v1/status` adds `capacity` with the configured limits (zero means no
configured cap), occupied slots by repository, connected factory-worker count,
and `effective_capacity_upper_bound`. The upper bound is the lesser of the
global limit and recently seen distinct worker names; it is not a count of free
slots or a promise of parallel execution. Job compatibility, repository caps,
cleanup and shared credentials can reduce actual capacity. Managed-worker and
Kubernetes credential-stream parallelism are each reported as one.
`credential_lock_state` is `unobserved`: lock ownership and subscription
capacity are not measured by the control plane.

Queued jobs add `waiting_reasons`: `awaiting_runner` (no recently seen compatible
worker), `runner_busy`, `global_capacity`, `repository_capacity`,
`cleanup_unconfirmed`, `publication_hold`, or `awaiting_dispatch`.
`credential_serialization_possible` additionally identifies compatible
Kubernetes runners subject to the exclusive stream lock; it does not claim the
job is currently waiting on that lock. These fields contain no credentials.

## Direct commands and legacy workers

The following settings apply to lower-level commands and explicit workers.
Custom sequence APIs require `server.state = "sqlite"`; they are separate from
the factory app and its private job store.

Commands use an executor, optional prompt template, and timeout:

```toml
[commands.audit]
executor = "codex"
prompt_file = "prompts/audit.md"
timeout = "30m"

[commands.custom-workflow]
executor = "custom-workflow-script"
timeout = "2h"
```

Without `prompt_file`, the input prompt is sent unchanged. With a template, include
`{{machinist.prompt}}` for legacy command runs, or task variables such as
`{{task.spec}}` and `{{task.output_dir}}` for workflow tasks. Executors and repositories remain worker-owned:

```toml
[executors.custom-workflow-script]
command = ["./scripts/custom-workflow.sh"]

[repositories.my-project]
path = "/absolute/path/to/my-project"
```

Managed triggers select one command with `command = "audit"`. Model selection remains
available when the executor command includes `{{machinist.model}}`.

## Task workflows

```toml
[workflows.deliver]
steps = ["build"]

[workflows.plan_then_build]
steps = ["plan", { command = "build", approval = "before" }]
```

Each named command must be configured. Workflow prompts can read `{{task.title}}`,
`{{task.source_url}}`, `{{task.spec}}`, and `{{task.output_dir}}`. The shared folder
is restored from the latest completed stage before execution. Keep temporary work
in `MACHINIST_SCRATCH_DIR`. [Storage settings](artifacts.md#storage) control file
limits and location. Configuration changes apply to new tasks; submitted tasks
keep their saved definitions.

## Artifact storage directory

Files are stored on the control-plane server’s disk, separately from the worker’s
scratch directory. Choose a persistent directory in the server config:

```toml
[storage.artifacts]
path = "/var/lib/machinist/artifacts"
```

The default is an `artifacts` directory beside the server database. Relative paths
resolve against the config file; `~` expands to the server user’s home. The server
creates the directory and must have permission to write there. Restart the server
after changing storage settings. When relocating existing storage, stop the
server and copy the entire artifact directory before changing the path; changing
the setting alone does not move saved files. Back up the database and files together.
See [storage settings](artifacts.md#storage) for size limits. Files are kept until their task is deleted.

## Migration

The `agents` table was renamed to `commands`. Move `[agents.NAME]` to `[commands.NAME]`
and replace `--agent` with `--command`.

Legacy `[pipelines]` configuration is not supported. Use `[workflows.NAME]` with
an ordered `steps` list for stages tracked by Machinist, or use one script command
when the script should own its internal process. See [the task workflow guide](task-guide.md)
and [workflow configuration](workflows.md). The old pre-command schema is a separate legacy migration. It does not describe
current factory recovery: `factory.db` is consistently snapshotted into `jobs.db`,
with the original archived and publication holds preserved. See
[job migration and recovery](local-factory/local-jobs.md#recovery-and-privacy).
