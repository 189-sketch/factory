# How Factory works

Factory treats coding agents as supervised processes. It renders a trusted agent
prompt with one work request, starts the configured executable in a Git
repository, streams its output, and records the result.

Factory does not parse an agent's prose to decide whether the requested product
outcome is correct. The agent prompt defines the workflow and its review gates;
Factory records execution facts.

## Two execution modes

### Direct mode

`factory run` starts immediately and does not contact a server:

```sh
factory run \
  --agent=foreman \
  --repo=/absolute/path/to/repository \
  --prompt="Complete https://github.com/example/project/issues/123"
```

Use direct mode for one task, local experimentation, or scripts that already
have their own scheduling.

Factory deliberately does not manage clones, worktrees, branches, commits, or
pull requests in direct mode. The selected agent and its reviewed prompt own
those actions.

### Managed mode

Managed mode separates a local server from one or more workers:

```text
browser -> control plane -> queue -> worker -> configured agent executable
                         <- status <-        <- result and event log
```

The server owns portable definitions, rendered prompts, the job queue, and run
history. Each worker owns executable commands, repository paths, credentials,
and local artifacts. See [Local control plane](control-plane.md) for setup and
security boundaries.

## Agents and pipelines

An agent combines three things:

- a prompt template;
- an executor name; and
- a timeout.

The worker maps the executor name to a local command. This keeps credentials,
provider setup, and machine paths out of the shared Factory definition.

A pipeline is an ordered list of agents. Every agent receives the same input
prompt. Factory stops before the next step when an agent process fails. A zero
pipeline exit status means every process exited successfully; it does not mean
Factory interpreted or approved the agents' output.

## Run artifacts

Direct runs write to:

```text
<data_directory>/runs/<run-id>/
├── events.jsonl
└── result.json
```

Managed attempts include the lease token so an abandoned attempt is never
overwritten by a redispatched one:

```text
<data_directory>/runs/<run-id>/<lease-token>/
```

`events.jsonl` stores ordered, byte-faithful stdout and stderr chunks encoded as
base64. `result.json` stores the terminal state, exit code, duration, and exact
token usage when the executor reports it through `FACTORY_TOKEN_USAGE_PATH`.
Factory does not estimate missing token usage.

Recording stops after 64 MiB of process output or when the encoded event file
reaches 32 MiB. Factory adds a truncation event, but the live agent process keeps
running.

## Exit behavior

Factory returns:

| Exit code | Meaning |
| --- | --- |
| agent exit code | the agent process exited unsuccessfully |
| `1` | Factory could not run or record the process |
| `2` | the command or configuration was invalid |
| `124` | the configured timeout expired |
| `130` | the run was cancelled |

Ctrl-C and configured timeouts cancel the complete agent process group, not only
the top-level executable.

## Trust boundary

Factory templates insert the runtime request as plain text. They do not execute
it as a shell command or expand parameters inside it. That protects the runner,
but the agent still reads repository content, issues, comments, and tool output.
Agent prompts must treat those sources as untrusted instructions.

Review every prompt and executor command before use. Scope the worker's
credentials and repository access to the work you want it to perform.
