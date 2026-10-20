# Examples

`machinist init` installs the files at this level:

- `config.toml` defines the default factory stages and legacy commands.
  - `triage`, `spec`, `implement`, and `review` support the app's Standard pipeline.
  - `task-to-pr` takes a task or GitHub issue to a reviewed pull request.
  - `audit` finds verified correctness bugs and reports them as GitHub issues.
- `worker.toml` shows local Codex and Claude Code executors.
- `prompts/` contains seed prompts. Edit shared app prompts in **Agents**;
  direct commands continue to use their configured prompt files.

The [workflow examples](workflows/README.md) compare this prompt-driven style with a
workflow written as a script.

The [GitHub comment intake example](github-actions/README.md) turns a new, authorized
`@machinist` issue comment into a `machinist:requested` label for a managed GitHub trigger.
