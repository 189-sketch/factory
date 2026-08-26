# Factory

Run a local team of coding agents that turns GitHub issues into reviewed pull
requests.

Factory is an open-source runtime and control plane for supervised coding work.
Give its foreman an issue. It plans the work, dispatches fresh agents to build
and review the change, waits for the repository's checks, and hands you a pull
request to merge.

```text
GitHub issue -> plan -> build -> independent review -> CI -> pull request
                                                            ^
                                                      you decide to merge
```

Factory runs on your machine, in your repositories, with the coding tools you
already use. Agent prompts and workflows are files you can read, change, and
version. The control plane records what ran without trying to guess whether an
agent's prose means the job is done.

## Why Factory?

- **One request, a complete workflow.** The foreman coordinates planning,
  implementation, review, repair, and CI instead of stopping after one coding
  session.
- **Review before handoff.** A fresh agent reviews the exact change. Failed
  checks and valid findings go back through a bounded repair loop.
- **Your tools and models.** Factory launches configured executors such as Codex
  or Claude. Model aliases let you choose per task without hard-coding provider
  details into prompts.
- **Local by default.** Repository paths, credentials, and executor commands
  stay on the worker. Managed results and event logs are copied only to the
  loopback control plane's local SQLite database.
- **A human owns the merge.** The shipped foreman can prepare and verify a pull
  request. It never merges one.

Factory also ships a read-only `audit` agent that inspects a repository,
independently verifies possible bugs, and opens evidence-backed issues for the
ones it can prove.

## Quick start

You need macOS or Linux, Go 1.26+, Git, an authenticated
[GitHub CLI](https://cli.github.com/), and an authenticated
[Codex CLI](https://developers.openai.com/codex/cli/). The default foreman uses
Codex and its native subagents.

### 1. Build and initialize

From a Factory source checkout:

```sh
mkdir -p ./bin
go build -o ./bin/factory ./cmd/factory
./bin/factory init
```

`factory init` creates editable configuration and agent prompts in
`~/.factory`. It keeps existing files unchanged when you run it again.

### 2. Give the foreman an issue

```sh
./bin/factory run \
  --agent=foreman \
  --repo=/absolute/path/to/your-repository \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123"
```

Use a small, well-defined issue for the first run. Factory streams the agent's
output and saves an ordered event log and terminal result under
`~/.factory/worker/runs/`.

### 3. Review the pull request

The foreman leaves the issue and pull request ready for a person to review. It
does not merge.

To inspect a repository without changing it:

```sh
./bin/factory run \
  --agent=audit \
  --repo=/absolute/path/to/your-repository \
  --prompt="Audit the request handling and persistence code"
```

## Run the local control plane

Direct mode is the fastest way to try Factory. When you want a queue, durable
run history, and a browser UI, add a repository to
`~/.factory/worker.toml`:

```toml
[repositories.my-project]
path = "/absolute/path/to/my-project"
```

Then start the server and worker in separate terminals:

```sh
./bin/factory start
```

```sh
./bin/factory worker start
```

Open [http://127.0.0.1:7331](http://127.0.0.1:7331), choose a repository and
agent, and submit a work request.

You can also queue managed work from the CLI. Use the repository name from
`worker.toml`, not its local path:

```sh
./bin/factory submit \
  --agent=foreman \
  --prompt="Complete https://github.com/your-org/your-repo/issues/123" \
  --repo=my-project
```

`factory run` executes immediately and never contacts the control plane.
`factory submit` validates the selection with the control plane, queues it, and
prints the admitted job ID. `factory worker run` remains available as the
worker-namespaced direct path for a single agent.

## Documentation

- [Getting started](docs/getting-started.md): requirements, installation, and
  first runs
- [How Factory works](docs/how-it-works.md): direct runs, managed runs,
  supervision, and artifacts
- [Configuration](docs/configuration.md): agents, executors, models, prompts,
  and pipelines
- [Local control plane](docs/control-plane.md): server, workers, security, and
  failure recovery
- [Development](docs/development.md): build, test, and project layout
- [Control-plane design](docs/control-plane/design.md): the detailed V1 design
  and invariants

## Project status

Factory is early software. It currently targets trusted local automation on
macOS and Linux. The control plane intentionally accepts only loopback listeners;
remote deployment needs a separate authentication and TLS boundary.

The opt-in Python eval under [`evals/`](evals/) runs the complete default workflow against
a dedicated scratch repository and verifies its issue-label lifecycle. It is separate
from `just check` because it creates real GitHub issues and pull requests.

## License

[MIT](LICENSE)
