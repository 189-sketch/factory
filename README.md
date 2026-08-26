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

`config.toml` contains shared server, agent, and pipeline configuration.
`worker.toml` contains machine-local executors, repositories, and credentials.

```sh
factory init
```

This installs editable defaults in `~/.factory`: shared configuration, worker
configuration, the `foreman` and `audit` prompts, and a random worker token.
Running it again creates any missing files and keeps every existing file unchanged.

Add repositories to `~/.factory/worker.toml`:

```toml
data_directory = "~/.factory/worker"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "~/.factory/server/worker.token"

[executors.codex]
command = ["codex", "exec", "--sandbox", "danger-full-access", "-"]

[executors.claude]
command = ["claude", "--print", "--dangerously-skip-permissions"]

# [repositories.my-project]
# path = "/absolute/path/to/my-project"
```

The optional worker `name` defaults to the machine hostname. Set it only when a
different display name is useful.

The shared configuration resolves prompt paths relative to itself:

```toml
[agents.foreman]
executor = "codex"
prompt_file = "agents/foreman.md"
timeout = "120m"

[agents.audit]
executor = "codex"
prompt_file = "agents/audit.md"
timeout = "60m"
```

The default agents are trusted local automation. `foreman` supervises fresh planning,
build, and review subagents and can change files and GitHub state. `audit` delegates
repository inspection and independent bug verification to fresh subagents. It keeps the
repository read-only and can create at most three GitHub issues for verified,
non-duplicate correctness bugs. Both use Codex with `danger-full-access` so GitHub and
worktrees outside the current repository are available. Review prompts before running
them and use them only on repositories and issues you trust. The prompts treat task and
repository content as untrusted text.

Each agent prompt is a strict Factory template and must place the runtime work request using
the supported parameter:

```text
{{factory.prompt}}
```

Factory substitutes the input prompt as plain text. It does not evaluate it as a
shell command or recursively expand parameters inside it. The rendered agent prompt
is limited to 512 KiB.

## Run

Use the foreman for supervised coding work:

```sh
factory run \
  --agent=foreman \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123"
```

The foreman writes each subagent prompt from the latest task state. It allows at most two
targeted repair attempts, requires one-line subagent summaries, records every phase and
attempt in the run output, and opens a non-draft pull request after local approval. The
issue stays in `factory:verifying` while available CI and automated review run, and changes
to `factory:ready-for-review` only when every review gate passes. The foreman never merges.

Use the audit agent to inspect a repository without changing it:

```sh
factory run \
  --agent=audit \
  --prompt="Audit the request handling and persistence code"
```

The audit sends inspection to fresh general-purpose subagents and uses a different fresh
subagent to verify each candidate correctness bug. It checks current open GitHub issues
for duplicates before creating up to three evidence-backed bug tickets. It never edits
code, creates a branch or commit, pushes code, or opens a pull request.

Factory still supports user-defined agents and pipelines. For example, after adding the
corresponding prompt files, a configuration can define its own quality pipeline:

```toml
[agents.lint]
executor = "codex"
prompt_file = "agents/lint.md"
timeout = "20m"

[agents.test]
executor = "codex"
prompt_file = "agents/test.md"
timeout = "30m"

[pipelines.quality]
agents = ["lint", "test"]
```

Run a user-defined pipeline by name:

```sh
factory run \
  --pipeline=quality \
  --prompt="Check the current repository"
```

Factory runs each listed agent independently and in order, passing the same input prompt
to every run. It stops before the next agent when a run fails. Agent output remains
opaque: a zero pipeline exit status means every agent process completed successfully,
not that Factory interpreted or approved the result.

Or pass the repository and configuration explicitly:

```sh
factory run --agent=foreman \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123" \
  --repo=/absolute/path/to/repository \
  --config=/absolute/path/to/worker.toml
```

Each agent receives its rendered prompt on standard input and runs with the Git
repository as its working directory. `--prompt` is required and replaces every
`{{factory.prompt}}` token byte-for-byte. The input may be a ticket instruction or any
other work request. Standard output and error remain live.
Factory records byte-faithful output chunks as base64 under
`<data_directory>/runs/<run-id>/events.jsonl` and writes the terminal outcome to
`result.json` in the same directory. Managed redispatches place each lease attempt under
`<data_directory>/runs/<run-id>/<lease-token>/` so an abandoned attempt remains intact.
Every result records measured duration. An executor may explicitly report its exact total
token usage by writing one non-negative base-10 integer to `FACTORY_TOKEN_USAGE_PATH`.
Factory stores and displays that value without estimating it; when the file is not written
or is invalid, token usage remains unavailable rather than becoming zero.
Recording stops after 64 MiB of process output or
when the encoded event file reaches 32 MiB, and adds a truncation event while live output
and the agent run continue.

Ctrl-C cancels the complete agent process group. A configured timeout does the
same. Factory returns the agent's exit status, `1` for a Worker runtime failure,
`124` for timeout, `130` for cancellation, and `2` for invalid commands or
configuration.

## Control plane

The optional local control plane stores jobs and runs in SQLite and serves an embedded
React application. Node is needed only to rebuild the frontend; the resulting Factory
binary contains the static assets.

`factory init` creates the shared worker token. Start both processes after adding
at least one repository to `worker.toml`. Each command reads its default configuration
file:

```sh
factory start
factory worker start
```

Factory listens on [http://127.0.0.1:7331](http://127.0.0.1:7331). Port 8080 is not
used. This first control-plane phase deliberately rejects non-loopback listeners. The
server owns prompts and pipelines; the worker owns executor commands, repository paths,
and credentials. Workers receive only a rendered prompt, executor name, repository key,
timeout, and opaque lease. Managed leases expire after 30 seconds and are renewed every
10 seconds while an agent runs. Polling requeues abandoned runs whose lease is no longer
renewed, allowing a compatible worker to receive a new token and execute them. A stale
worker may finish locally after losing connectivity, but it cannot update control-plane
state after redispatch.

The control plane treats output as opaque. It records whether each agent process is
queued, running, or terminal, but it does not interpret ticket labels or decide whether
the requested product outcome is complete.

Rebuild the React assets and Go binary together with:

```sh
just build
```

## Verify

```sh
just check
```

## License

This project is licensed under the [MIT License](LICENSE).
