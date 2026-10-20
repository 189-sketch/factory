# Native agent runs

New runs in the web UI use `native-v1`: one primary run owns all observed child activity. The harness discovers repository definitions in the task worktree:

- Claude Code: `.claude/agents/*.md`.
- Codex: `.codex/agents/*.toml`, with `name`, `description`, and `developer_instructions`.

Commit those files to the repository. Machinist does not translate or copy agent definitions. The primary instructions come from `commands.primary.prompt_file` in the server config. The example config and `machinist init` include this command. Set its executor to `codex-native` or `claude-native`. Set a model only when it is available to the signed-in account. Codex checks `account/read` and `model/list` before starting a primary thread; the service can still reject a listed model at turn start.

Host workers use existing CLI subscription sign-in. Native adapters remove inherited desktop routing variables and API-token variables from the child environment. Codex requires a ChatGPT account; Claude rejects an explicit API key source. This is a personal host integration, not approval for hosted commercial subscription use.

`codex-app-server` owns one stdio JSON-RPC process per run. The configurable command must launch `codex app-server`; model selection travels in `thread/start`. The worker authorizes the assigned repository. Codex must already trust its main repository path in the CLI configuration. A linked worktree inherits trust from that Git common repository root. The adapter rejects project-configuration-disabled warnings instead of silently running without repository agents. Register only repositories whose configuration you trust. CLI sandbox settings and permissions remain in the configured command. `claude-stream` requires Claude print mode with `stream-json`; it must not use `--bare`, which disables subscription authentication. Ordinary process commands and existing CLI exec workflows still work.

Each run stores `native-input.json` locally, with the rendered primary prompt, adapter, argv, model, definition hash, and hashes of relevant repository configuration. Raw stdout/stderr remain in `events.jsonl`. Normalized child observations and primary/child messages share that spool. Unknown native events remain in raw output. Only primary completion and primary usage determine the run result. Child failures are observations the primary can recover from.

Workers send bounded, lease-authenticated event batches to `POST /api/v1/runs/{id}/events`. Sequence IDs are contiguous; identical replay is acknowledged, and conflicting payloads or stale leases are rejected. An acknowledged cursor is saved beside the worker spool. SQLite stores activity separately from the lifecycle journal. `GET /api/v1/runs/{id}/events?after=N` returns up to 128 events and the next cursor. The UI polls this API and displays connection loss with the last observations. Completion waits for pending event delivery. Timeout, cancellation, or a stale lease stops the owned process group. Confirmed interruption marks unfinished children interrupted; successful primary completion without a child completion signal leaves that child's status unknown.

The primary returns one JSON object:

```json
{"outcome":"complete","summary":"Implemented and verified the change","delivery":{"pr_url":"https://github.com/owner/repo/pull/123","head_sha":"0123456789012345678901234567890123456789","branch":"codex/change"}}
```

Outcomes are `complete`, `blocked`, or `failed`. Omit `delivery` for an answer with no file changes. Code delivery still requires a clean task worktree and matching local branch, pushed SHA, and PR evidence. PRs are never merged automatically. External publication holds remain enforced.

Native reviewer feedback is advisory. This mode does not enforce the legacy specification approval gate or create the legacy isolated read-only review checkout. Use a legacy pipeline if those enforced gates are required. Historical recipes, journal replay, existing tasks, and saved prompt overrides remain readable. Legacy APIs retain their omitted-recipe behavior; callers opt in with `recipe: "native-v1"`. New host automations use native mode when the primary command is configured; saved legacy automations retain their recipe.

Leases remain in memory. After a server restart, activity history survives, but a running process is not automatically resumed or duplicated. Existing manual-stop-before-retry handling still applies. Native mode is currently host-only; Kubernetes keeps its existing delivery contract and does not advertise this capability.

Checks:

```sh
go test ./internal/config ./internal/runner ./internal/managedworker ./internal/controlplane ./internal/cli
cd internal/controlplane/web && npm test && npm run build
MACHINIST_NATIVE_LIVE=1 MACHINIST_CODEX_LIVE_MODEL=ACCOUNT_SUPPORTED_MODEL go test ./internal/runner -run TestNativeLiveRepositoryAgents -v -count=1
```

The live test uses a temporary Git worktree and the host's existing login. It verifies repository child discovery using a value present only in the child's definition. Codex uses a temporary trusted configuration and a read-only symlink to the existing login; the user's saved configuration is unchanged. No API keys are used. Set `MACHINIST_NATIVE_LIVE_EXTENDED=1` to check two repeated, parallel child identities, live activity before completion, and missing-file recovery in both harnesses. The deterministic tests cover framing, parallel identities, child failure, primary-result isolation, unknown events, cancellation, timeout, durable replay, sequence conflicts, and stale leases.

Protocol references: [Codex app-server](https://learn.chatgpt.com/docs/app-server), [Codex custom agents](https://learn.chatgpt.com/docs/agent-configuration/subagents#custom-agents).
