# Local Control Plane V1

Status: Proposed for local implementation

## 1. Outcome

Factory can run immediately from the CLI or receive work from one local control plane.
The control plane stores jobs and runs in SQLite, owns agent templates and pipelines,
and shows process state in a small React application. Vite bundles the frontend at build
time and Go embeds the static output, so Factory still deploys as one binary with no Node
runtime. A worker polls outbound, executes one run at a time with the existing runner,
and reports the terminal result.

The control plane records only execution facts. It does not interpret agent output,
GitHub labels, Linear state, or whether the requested product outcome is correct.

## 2. Commands

Direct mode remains immediate and independent:

```sh
factory run --agent=plan --prompt="Work on ticket LINEAR-123"
factory run --pipeline=code --prompt="Work on ticket LINEAR-123"
```

Managed mode uses two long-running commands:

```sh
factory start --config=/path/to/config.toml
factory worker start --config=/path/to/worker.toml
```

`factory start` listens on `127.0.0.1:7331` so it does not interfere with the existing
application on port 8080. This local phase rejects non-loopback listeners. Remote VM and
Kubernetes deployment remains a target, but requires a separate authenticated human web
surface and TLS boundary before Factory exposes the server beyond the host.

## 3. Configuration ownership

The shared Factory configuration owns the server, portable prompts, and pipelines:

```toml
[server]
listen = "127.0.0.1:7331"
database = "~/.factory/server/factory.db"
worker_token_file = "~/.factory/server/worker.token"

[agents.plan]
executor = "codex"
prompt_file = "agents/plan.md"
timeout = "20m"

[pipelines.code]
agents = ["plan", "build", "verify"]
```

The worker file owns machine-specific execution, repositories, and credentials:

```toml
data_directory = "~/.factory/worker"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "~/.factory/worker.token"

[executors.codex]
command = ["codex", "exec", "--sandbox", "danger-full-access", "-"]

[repositories.factory]
path = "/workspace/factory"
```

Direct mode reads `config.toml` and resolves its named executor through `worker.toml`.
Managed mode renders the same configuration on the server and the worker
resolves the named executor and repository through its own `worker.toml`.

## 4. Shared execution boundary

Both modes produce one resolved execution request for the existing runner. The runner
receives only a complete prompt, command, repository path, timeout, and run ID.

For managed work, the server sends this immutable run specification:

```text
run ID
job ID
agent name and definition hash
executor name
repository key
rendered prompt
timeout
opaque lease token
```

The server never sends an executable command, local path, environment variable, or
credential. Each poll advertises the worker's executor and repository names. The server
leases only compatible work. The worker still rejects an unknown name before starting a
process and reports that preflight failure without a local event file.

## 5. Job and run state

A submitted job contains an input prompt, repository key, and either one agent or one
pipeline. A pipeline expands to ordered runs with immutable rendered prompts. Only the
first run starts as queued; later runs start as pending. Exit status zero promotes the
next pending run to queued. Any other terminal state fails the job and marks later runs
skipped.

```text
job: queued -> running -> succeeded | failed
run: pending -> queued -> running -> succeeded | failed | timed_out | cancelled
     pending -----------------------------------------------> skipped
```

Each worker process generates a random instance ID at startup; the configured worker name
is display metadata only. Leasing selects a compatible queued run, creates an opaque
lease token, and changes the run to running for that instance in one SQLite transaction.
Completion requires the lease token. Two concurrent polls cannot receive the same run.

Polling is idempotent for a worker instance. If a lease commits but its HTTP response is
lost, the instance's next poll returns the same active run and lease token instead of
claiming new work. A worker instance therefore has at most one active run.

There is no execution retry, reassignment, or abandoned-run recovery in V1. One worker
executes at most one run at a time. A worker process interruption can leave a run marked
running; the operator can see that state and restart the demo with a new job.

## 6. Persistence

SQLite stores:

- `jobs`: input prompt, repository key, selection, state, and timestamps.
- `runs`: ordered agent runs, immutable rendered prompt metadata, worker, process outcome,
  and timestamps.
- `workers`: worker instance ID, display name, and last poll time.

The worker continues writing its existing local `events.jsonl` and `result.json`. On
completion it uploads the result and event file to the server. A preflight failure instead
uploads a terminal state, exit code, and diagnostic with no files. The server stores the
payload with the run so completed output survives restarts.

The runner caps the actual encoded event file at 32 MiB, including per-event metadata.
The completion endpoint accepts a 96 MiB JSON envelope, which safely contains the event
string even under worst-case JSON escaping plus the result and completion metadata.

Completion is idempotent for the leased run and worker. If the database commits but the
response is lost, sending the same completion again succeeds without changing state. A
live worker retries the same local completion payload with bounded backoff until the
server acknowledges it or the worker is stopped. Permanent HTTP 4xx responses stop the
worker instead of retrying an impossible payload forever. This retries result delivery,
never the agent execution. The server rejects terminal states that contradict their exit
code. Live log streaming is not in V1. Losing the control-plane connection does not
terminate the local agent process.

## 7. HTTP surface

- `GET /`: embedded React application.
- `GET /api/v1/status`: summary-only jobs, runs, workers, available selections, and CSRF
  token. Stored result and event payloads are not included in periodic status responses.
- `POST /api/v1/jobs`: submit an agent or pipeline job from the local application. It requires a
  server-generated CSRF token plus matching loopback Host and Origin headers.
- `POST /api/v1/workers/poll`: authenticate a worker, update last-seen state, and lease
  one compatible run when available. The request includes the worker name and its
  process instance ID, executor names, and repository names. Repeated polls return that
  instance's existing active lease.
- `POST /api/v1/runs/{id}/complete`: authenticate the owning worker and record the
  terminal result and optional result/events payload. The request must include the opaque
  lease token.

Worker endpoints require `Authorization: Bearer <token>`. The V1 status page and form are
intended for the loopback listener and have no user authentication. The submission form
uses a random process-local CSRF token, and submissions with a missing or foreign Origin
are rejected. Starting the server on a non-loopback address is rejected so that this
limitation is explicit.

Workers permit plain HTTP only for loopback control-plane URLs. Non-loopback URLs require
HTTPS so bearer tokens and prompts are never sent across a network in cleartext.

## 8. Invariants

- INV-1: `factory run` never requires or contacts a control plane.
- INV-2: a managed run uses the server-rendered prompt but only worker-owned executors,
  repositories, environment, and credentials.
- INV-3: the runner receives a final rendered prompt and knows nothing about templates,
  tickets, labels, or control-plane semantics.
- INV-4: one worker process instance has at most one active run, and it receives only work
  matching its advertised executor and repository names.
- INV-5: a run is transactionally leased once to one worker instance and opaque lease
  token; V1 never retries or reassigns its execution automatically.
- INV-6: the control plane advances pipelines only from process terminal state.
- INV-7: job, run, result, and completed output state survives a server restart.

## 9. Acceptance criteria and checks

- AC-1: existing direct agent and pipeline tests continue to pass without a server.
- AC-2: a local status-page submission is persisted, leased by a configured worker,
  executed by the existing runner, and shown as succeeded or failed.
- AC-3: a three-agent pipeline leases agents in order, uses the same input prompt for
  every template, and marks later steps skipped after the first non-zero result.
- AC-4: invalid worker tokens receive HTTP 401 and cannot lease or complete runs.
- AC-5: concurrent compatible polls lease a queued run once, while incompatible workers
  receive no work. Unknown names that still reach a worker fail locally without starting
  an agent and report a diagnostic without event files.
- AC-6: restarting the server against the same database preserves jobs, runs, results,
  completed output, and worker last-seen state.
- AC-7: the default server binds only to `127.0.0.1:7331`; port 8080 is never used.
- AC-8: `just check` passes, focused HTTP/store tests pass, and a real browser verifies
  submission and terminal status in the page.
- AC-9: repeating completion after a committed-but-unacknowledged response succeeds
  idempotently and does not advance a pipeline twice.
- AC-10: polling again after a committed-but-unacknowledged lease returns the same run and
  lease token to the same worker instance; another instance cannot complete it.

## 10. Out of scope

GitHub or Linear intake, ticket synchronization, label interpretation, prompt result
interpretation, live log streaming, worker enrollment, token issuance, execution retries,
reassignment, heartbeats, cancellation, repository cloning, worktrees, scheduling,
multiple control-plane replicas, non-loopback serving, TLS, and production web
authentication.
