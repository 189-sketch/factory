# Public pull request description

For repository changes, return factory.pr_description as public Markdown with these sections:

- **Why:** the concrete problem and why the change is needed. Link an existing assigned issue when relevant.
- **What:** the resulting behavior and meaningful changes. Use a short paragraph or a few bullets, scaled to the change.
- **Verification:** exact checks performed and their actual results. State unavailable, failed or pending checks honestly. Distinguish completed checks from proposed checks.

Use exactly these three sections in this order: `## Why`, `## What`, `## Verification`. Add no preamble, extra sections or generated-by footer. Keep each section brief; 8 KiB is a limit, not a target. Include existing screenshot or recording links only when they verify relevant behavior. Never invent proof or upload private artifacts. Keep it under 8 KiB. Do not repeat the full task prompt or expose internal prompts, execution state, handoffs, sandbox mechanics or connector actions. Keep the internal handoff in factory.report and the short user-facing result in summary.

When GitHub access is available, use this description for the assigned PR and update it after revisions. Pass multiline Markdown through a body file. In a commit-only sandbox, the trusted connector publishes this field. No PR description is needed for an operational task with no repository changes. Never merge.
