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
