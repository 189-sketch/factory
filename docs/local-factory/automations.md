# Automations

An automation is a saved single-agent job: a name, connected repository and Markdown instructions. It uses the coding agent and runner selected in setup, including local, VM and Kubernetes runners. Each run starts in the root of its isolated repository checkout.

Open **Automations** in the sidebar, choose **New automation**, write instructions and save. **Run now** queues one normal job. Open its run to see activity, results and any PR. For a read-only review, explicitly ask the agent to report findings without changing files. Repository changes use the same PR delivery as other single-agent jobs, without a separate reviewer. Internal prompts and findings are not posted to issues.

Schedules are optional. Choose an hourly, daily or weekly preset, or a five-field cron expression with an IANA timezone such as `Europe/London`. Scheduled times use that timezone; next/last run timestamps include the viewer's timezone. Pause stops future scheduled admissions. Manual runs remain available while paused. Saving an automation never runs it immediately.

Machinist's control plane must stay running. It checks due schedules every 15 seconds. Definitions and the next due time persist in the existing local SQLite database. After downtime it queues at most one missed occurrence per automation, then advances to the next future occurrence. It does not replay a backlog.

An automation waits for all its prior jobs, including historical retries. Active work, blockers and PRs awaiting merge prevent another admission. Busy scheduled occurrences are skipped and shown on the automation. Finish the job, merge and check its status, or cancel it to release the schedule. If a cancelled attempt started a process, verify it has stopped and choose **Confirm stopped and run** before resuming. Retries of older runs cannot overlap newer work. A lost response to Run now reuses the same admission key.

Edit opens the saved instructions and schedule. Pause/Resume uses buttons. Delete requires inline confirmation and removes only the definition; existing run history remains.

## API

All mutations require the normal CSRF/origin protection or worker bearer authorization.

- `GET /api/v1/automations`: list definitions and latest run links.
- `POST /api/v1/automations`: create `{name, repository, prompt, cron, timezone, paused}`. Empty `cron` means manual only.
- `PUT /api/v1/automations/{id}`: replace those fields with the current `version`. Stale versions return 409.
- `POST /api/v1/automations/{id}/run`: queue once with `{client_request_id}`. Reuse the key to recover a lost response. An optional `previous_process_stopped: true` explicitly confirms cancelled processes have stopped.
- `DELETE /api/v1/automations/{id}`: delete with `{version}`.

Schedule cursor advancement, job creation and the admission receipt commit in one database transaction. Jobs use the existing execution leases, cancellation, recovery, result validation and PR publisher. There is no second workflow engine or external scheduling service.
