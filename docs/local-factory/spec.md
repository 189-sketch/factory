# Local four-agent factory

This is the delivered core contract. It supersedes the earlier proposal for
local specification artifacts and a factory SQLite schema.

## Task

Deliver a GitHub-backed four-agent pipeline and a quiet Warp-inspired interface,
using the user's own Codex or Claude Code processes locally or on a remote VM.
Remove local specification viewers and unnecessary factory artifact storage.

## Acceptance criteria

- AC-1: A description creates a GitHub issue; an existing issue URL adopts the
  matching repository issue once. Cross-repository sources are rejected.
- AC-2: Triage routes to optional Spec or Implement. Spec publishes a GitHub
  document and draft PR, then waits for approval of the exact producing attempt.
- AC-3: Implement reuses the branch and PR, advances its head, and publishes a
  ready PR. Host Git and GitHub evidence must match the claimed result.
- AC-4: Independent read-only Review receives the exact diff and check evidence.
  Definite findings trigger at most three repair rounds on the same PR. Accepted
  review waits for human merge and a GitHub-verified completion check.
- AC-5: GitHub issues, trusted chained comments, and prompt files hold durable
  workflow state. Restart rebuilds tasks and interrupts running stages. Retry
  requires confirmation that the previous process stopped. No local database or
  task artifact snapshots are opened by the default factory backend.
- AC-6: Inbox, Runs, and Agents expose concise progress and GitHub links. Factory
  tasks do not show local Markdown file lists or embedded specification viewers.
  Prompt edits reject stale versions and retain required task inputs.
- AC-7: Local and remote workers use the same allowlisted executor protocol.
  Missing capabilities, GitHub outages, invalid JSON, missing evidence, and stale
  attempts fail closed rather than imply completion.

## Boundaries

One foreman and one stable GitHub identity serve each repository. Credentials,
host configuration, logs, and working checkouts remain local. Legacy SQLite
sequence workflows remain compatible but are not migrated. The current slice
excludes automatic CI/review follow-ups, merge polling, automation editing, live
streaming, and provider-session recovery.

## Checks

Run `just check` for frontend tests/build, Go formatting, vet, race tests/build,
and existing Python and intake tests. HTTP integration tests use a fake GitHub
client to prove routing, approval, reconstruction, outage handling, intake, and
uncertain-write recovery. Worker tests use real temporary Git repositories and
fake CLI responses. Browser verification checks the composed interface separately.
A real model completing a live GitHub task remains separate integration evidence.

See [the guide](README.md), [architecture](../../ARCHITECTURE.md), and
[Warp observations](warp-evidence.md).
