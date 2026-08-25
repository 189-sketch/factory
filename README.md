# Factory

Factory runs coding agents as supervised workloads. V1 is a standalone Worker:
one command selects a configured agent prompt, runs it inside a Git repository,
streams its output, and saves an ordered event log and terminal result.

Factory deliberately does not manage clones, worktrees, branches, commits, or
pull requests in this version. Those responsibilities belong to the selected
agent and its trusted prompt.

## Build

Requirements are macOS or Linux, Go 1.26 or newer, Git, and the agent executable
named by your definition.

```sh
just build
```

## Configure

Agent definitions are shared configuration. Worker configuration contains only
machine-local paths.

```sh
mkdir -p ~/.factory/agents
cp examples/worker.toml ~/.factory/worker.toml
cp examples/factory.toml ~/.factory/factory.toml
cp examples/agents/plan.md ~/.factory/agents/plan.md
```

Update `~/.factory/worker.toml` so `definition_file` points to the copied
definition:

```toml
data_directory = "~/.factory/worker"
definition_file = "factory.toml"
```

The definition resolves prompt paths relative to itself:

```toml
[agents.plan]
command = ["codex", "exec", "--sandbox", "workspace-write", "-"]
prompt_file = "agents/plan.md"
timeout = "20m"
```

Each prompt is a strict Factory template and must place the runtime task using
the supported parameter:

```text
Assess whether this task is ready to build:

<task>
{{factory.task}}
</task>
```

Factory substitutes the task as plain text. It does not evaluate the task as a
shell command or recursively expand parameters inside it. The rendered prompt
is limited to 512 KiB.

## Run

Define each coding role as a named agent prompt, then pass the task when running
it from a Git repository:

```sh
factory worker run \
  --agent=plan \
  --task="https://github.com/your-org/your-repo/issues/123"
```

Or pass the repository and configuration explicitly:

```sh
factory worker run --agent=plan \
  --task="Fix the failing account creation flow" \
  --repo=/absolute/path/to/repository \
  --config=/absolute/path/to/worker.toml
```

The agent receives the rendered prompt on standard input and runs with the Git
repository as its working directory. `--task` is required and replaces every
`{{factory.task}}` token byte-for-byte. Standard output and error remain live.
Factory records byte-faithful output chunks as base64 under
`<data_directory>/runs/<run-id>/events.jsonl` and writes the terminal outcome to
`result.json` in the same directory. Event recording stops after 64 MiB and adds
a truncation event, while live output and the agent run continue.

Ctrl-C cancels the complete agent process group. A configured timeout does the
same. Factory returns the agent's exit status, `1` for a Worker runtime failure,
`124` for timeout, `130` for cancellation, and `2` for invalid commands or
configuration.

## Verify

```sh
just check
```
