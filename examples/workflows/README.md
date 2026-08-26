# Workflow examples

These examples are small, runnable Factory definitions. Each one keeps the agent prompt
in five sections: Role, Input, Required result, Procedure, and Boundaries. This makes the
contract easy to scan without adding another Factory schema.

| Example | What it does |
| --- | --- |
| [Issue to pull request](issue-to-pr/README.md) | Gives one foreman a GitHub issue and hands a reviewed pull request to a person. |
| [Multi-review](multi-review/README.md) | Runs a Codex review and then a Claude Code review against the same pull request. |
| [Code audit](code-audit/README.md) | Audits a repository and opens issues only for independently verified bugs. |

Every example runs directly with `factory run`. To use one with the control plane, copy
its agent and pipeline definitions into `~/.factory/config.toml`, copy its prompt files
under `~/.factory/agents/`, and register the target checkout in
`~/.factory/worker.toml`:

```toml
[repositories.my-project]
path = "/absolute/path/to/my-project"
```

Restart `factory start` and `factory worker start`, then submit with
`factory submit --repo=my-project`.
