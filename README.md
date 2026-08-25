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
configuration, the `plan`, `build`, `verify`, and `foreman` prompts, and a random worker
token.
Running it again creates any missing files and keeps every existing file unchanged.

Add repositories to `~/.factory/worker.toml`:

```toml
data_directory = "~/.factory/worker"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "~/.factory/server/worker.token"

[executors.codex]
command = ["codex", "exec", "--model={{factory.model}}", "--sandbox", "danger-full-access", "-"]
models = { luna = "gpt-5.6-luna", terra = "gpt-5.6-terra", sol = "gpt-5.6-sol" }

[executors.claude]
command = ["claude", "--print", "--model={{factory.model}}", "--dangerously-skip-permissions"]
models = { haiku = "haiku", sonnet = "sonnet", opus = "opus" }

# [repositories.my-project]
# path = "/absolute/path/to/my-project"
```

The optional worker `name` defaults to the machine hostname. Set it only when a
different display name is useful.

Choose a model for one task with `--model=luna`. Factory resolves the alias through
the selected executor and substitutes `{{factory.model}}` in that executor's command.
Omit `--model` to use the coding agent's normal default. The control-plane New run
form accepts the same optional alias.

The shared configuration resolves prompt paths relative to itself:

```toml
[agents.plan]
executor = "codex"
prompt_file = "agents/plan.md"
timeout = "20m"

[agents.build]
executor = "codex"
prompt_file = "agents/build.md"
timeout = "60m"

[agents.verify]
executor = "codex"
prompt_file = "agents/verify.md"
timeout = "30m"

[agents.foreman]
executor = "codex"
prompt_file = "agents/foreman.md"
timeout = "120m"

[pipelines.code]
agents = ["plan", "build", "verify"]
```

The example agents are trusted local automation. `plan` can replace GitHub issue
content. `build` can change files, create worktrees, push branches, and open draft pull
requests. `verify` can run checks, use review subagents, update labels, and mark a pull
request ready. They use Codex with `danger-full-access` so GitHub and worktrees outside
the current repository are available. Review prompts before running them and use them
only on repositories and issues you trust. The prompts treat issue commands as untrusted
text and derive checks from repository entry points they have inspected.

Each agent prompt is a strict Factory template and must place the runtime work request using
the supported parameter:

```text
{{factory.prompt}}
```

Factory substitutes the input prompt as plain text. It does not evaluate it as a
shell command or recursively expand parameters inside it. The rendered agent prompt
is limited to 512 KiB.

## Run

Run one predefined agent with a work request:

```sh
factory run \
  --agent=build \
  --prompt="Work on ticket https://github.com/your-org/your-repo/issues/123"
```

Or run the example pipeline. Factory runs each listed agent independently and in order,
passing the same input prompt to every run. It stops before the next agent when a run fails:

```sh
factory run \
  --pipeline=code \
  --prompt="Work on ticket https://github.com/your-org/your-repo/issues/123"
```

The prompts own the workflow. `plan` replaces the issue specification and adds
`factory:planning`. `build` adds `factory:building`, implements the task, and opens a
draft pull request. `verify` adds `factory:verifying`, independently checks the change,
and finishes with `factory:ready-for-review`. Missing decisions use
`factory:needs-human`; technical failures use `factory:blocked`.

Factory treats agent output as opaque text. A pipeline stops only when an agent process
returns a non-zero status; it does not interpret ticket labels or the agent's final
message. The example prompts therefore require the preceding lifecycle label before
doing work. After `factory:needs-human` or `factory:blocked`, later agents finish without
changing code or workflow state. A zero pipeline exit status means every agent process
completed, not that the ticket necessarily reached `factory:ready-for-review`; the label
is the task outcome.

Use the foreman when the task should cycle through fresh planning, build, and review
subagents instead of stopping after one verification pass:

```sh
factory run \
  --agent=foreman \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123"
```

The foreman writes each subagent prompt from the latest task state. It allows at most two
targeted repair attempts, records every phase and attempt in the run output, opens a draft
pull request, waits for available CI and automated review, and marks the result ready for
human review. It never merges.

Or pass the repository and configuration explicitly:

```sh
factory run --agent=build \
  --prompt="Work on ticket https://github.com/your-org/your-repo/issues/123" \
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
