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

## Run

Define each coding task as a named agent prompt, then run it from a Git
repository:

```sh
factory worker run --agent=plan
```

Or pass the repository and configuration explicitly:

```sh
factory worker run --agent=plan \
  --repo=/absolute/path/to/repository \
  --config=/absolute/path/to/worker.toml
```

The agent receives the configured prompt unchanged on standard input and runs
with the Git repository as its working directory. Its standard output and error
remain live. Factory records byte-faithful output chunks as base64 under
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
