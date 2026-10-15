# Reliable local jobs

The control plane remains one service. Jobs use a local or VM worker, or a
configured Kubernetes target. Each target follows the same job and PR delivery
model. Kubernetes runs agent attempts in gVisor Jobs while the trusted connector
publishes changes through GitHub.

## Two built-in modes

- **Standard pipeline** (`factory`): Triage, optional Spec and approval, Build,
  independent Review, then human merge. Triage and Review use restricted read-only
  executors. Review findings return to Build, on the same PR, up to three repairs.
- **Single agent** (`single`): one agent performs an operational task or automation
  and returns a result. There is no separate triage, planning, or reviewer. Requested
  repository changes still require a PR and human merge. An unchanged operational
  task can succeed without a PR.

The mode is fixed at creation. There is no custom workflow editor or arbitrary
workflow choice. Old direct/answer records remain readable only for history and
recovery; new submissions reject those modes. Source issues are optional, and local
jobs never create an issue merely to track execution.

Submit through `POST /api/v1/jobs` using the local CSRF header or configured API
credential:

```json
{
  "repository": "owner/repository",
  "recipe": "single",
  "spec": "Check the repository and report the result",
  "client_request_id": "client-generated-unique-request-id"
}
```

Use `recipe: "factory"` for the Standard pipeline. Omitting the recipe preserves
older factory API clients. The first response is 201 with `id`; an identical
replay is 200 with the same ID. Reusing that request ID for a different payload
returns 409. Keep the ID after a lost response. Jobs use the configured executor
profile and never silently switch model providers.

## Recovery and privacy

One process owns `jobs.db`, enforced by a host lock. On the first upgrade, SQLite
creates a consistent snapshot of the former factory database, then the old file
is retired under `archive/factory-v1.db`. Tasks, events, prompt snapshots and
completion receipts are retained. `jobs-migration.json` records store locations.
Legacy direct/sequence stores remain unchanged. No worker output is removed.
Back up the full runtime directory; restoring the database alone is insufficient
for private deliverables. Do not resume an older binary from the archive.

Restart marks unconfirmed running work interrupted. It never silently launches
it again. A retry requires confirmation that the previous process stopped.
Repeating a committed completion is acknowledged without advancing twice.

Imported blocked jobs receive a publication hold. Held work cannot be dispatched,
retried, approved, or posted as a GitHub status update. Changing a prompt cannot
release it. Release requires the dedicated UI action after reviewing the content
and destination, or `POST /api/v1/jobs/{id}/release_publication` naming the current
`hold_version`. Stale versions return 409. A private hold can also be placed on
stopped work with `publication_hold`, a reason in `feedback`, and `hold_version`.
Running work must first be cancelled and its process stopped. A hold cannot undo
an external write already issued by the compatibility worker.

## Scope and checks

The app provides two built-in modes, duplicate-safe submission, private holds,
and a compact sidebar and table. Local agents use per-run GitHub App credentials.
Kubernetes pods receive no GitHub credentials; the trusted connector publishes
their changes. See [Kubernetes setup and recovery](kubernetes.md).
Foreman chat and PostgreSQL remain future work.

Proof covers Standard routing and bounded repairs, Single-agent completion without Review, file/PR evidence checks, replay
after restart, legacy store migration with receipts, a second-owner rejection,
and stale hold release denial. UI checks cover filtering and lost-response retry.
Full CI runs the frontend tests/build and Go race tests on Linux and macOS.

The cloud destination is the operator-owned Google Cloud project `machinist-prod`.
No cloud resource is required to run this slice locally, and no cluster is created
as a side effect of startup.


The table shows stage dots for planning, building and review. Hover a dot or focus
the group with the keyboard for named statuses, including skipped stages. Mobile
places progress beneath the title. The UI self-hosts Inter and uses a compact
wordmark. Native CLI results may contain one complete JSON Markdown fence; only
that wrapper is removed, and the same strict size/schema validation still runs.
Claude read-only profiles disable inherited hooks, plugins and MCP servers.
