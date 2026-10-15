# Local four-agent factory

This is the core contract, updated for the approved GitHub App onboarding and
local execution-state ownership. Published specifications remain on GitHub.

## Task

Deliver a GitHub-backed four-agent pipeline and a quiet Warp-inspired interface,
using the user's own Codex or Claude Code processes locally or on a remote VM.
Remove local specification viewers and unnecessary factory artifact storage.

## Acceptance criteria

- AC-1: A description creates a local task with no GitHub writes; an existing
  issue URL adopts the matching repository issue once. Cross-repository sources
  are rejected. Informational requests can finish in Triage.
- AC-2: Triage routes to optional Spec or Implement. Spec publishes a GitHub
  document and draft PR, then waits for approval of the exact producing attempt.
- AC-3: Implement reuses the branch and PR, advances its head, and publishes a
  ready PR. Requested reports, documents, and other files follow this same PR
  delivery rule, including document-only changes. Plain questions and health checks
  without a requested file may finish in Triage. Host Git and GitHub evidence must match the claimed result.
- AC-4: Independent read-only Review receives the exact diff and check evidence.
  Definite findings trigger at most three repair rounds on the same PR. Accepted
  review waits for human merge and a GitHub-verified completion check.
- AC-5: Local SQLite stores execution history, prompt versions, approvals, and
  receipts. GitHub holds issues, published specs, code, and PRs. No internal
  prompts or events are posted to issues; a linked issue gets one readable status
  comment updated in place. Restart interrupts running stages. Retry requires
  confirmation that the old process stopped. Factory artifact snapshots are not
  published.
- AC-6: Inbox, Runs, and Agents expose concise progress and GitHub links. Factory
  tasks do not show local Markdown file lists or embedded specification viewers.
  Prompt edits reject stale versions and retain required task inputs.
- AC-7: Local and remote workers use the same allowlisted executor protocol.
  Missing capabilities, GitHub outages, invalid JSON, missing evidence, and stale
  attempts fail closed rather than imply completion.

- AC-8: First-run onboarding connects a GitHub App through device authorization,
  lists only installation/user-accessible repositories, verifies write permissions
  and checkout identity, selects a local or VM agent, and requires a real PONG.
  No TOML editing or issue creation is needed. Tokens stay on their authorized host.

## Boundaries

One foreman owns a local factory database. Credentials,
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
The real local PING previously passed. Full four-agent code delivery and live App
authorization remain separate integration evidence; fixture tests do not prove
account-owner consent or a reachable VM.

See [the guide](README.md), [architecture](../../ARCHITECTURE.md), and
[Warp observations](warp-evidence.md).
