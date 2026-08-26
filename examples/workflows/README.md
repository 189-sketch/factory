# Workflow examples

These examples are small, runnable Machinist definitions. Each one keeps the agent prompt
in five sections: Role, Input, Required result, Procedure, and Boundaries. This makes the
contract easy to scan without adding another Machinist schema.

| Example | What it does |
| --- | --- |
| [Issue to pull request](issue-to-pr/README.md) | Gives one foreman a GitHub issue and hands a reviewed pull request to a person. |
| [Multi-review](multi-review/README.md) | Runs a Codex review and then a Claude Code review against the same pull request. |
| [Code audit](code-audit/README.md) | Audits a repository and opens issues only for independently verified bugs. |

Every example runs directly with `machinist run`. Initialize Machinist once:

```sh
machinist init
```

Before a direct or managed run, ensure `~/.machinist/worker.toml` defines every executor
named by the selected example. Issue to PR and code audit require `codex`; multi-review
requires both `codex` and `claude`. The shipped [worker configuration](../worker.toml)
contains both definitions. `machinist init` keeps an existing worker file unchanged, so add
missing executors yourself when upgrading an existing setup.

To use an example with the control plane, copy its agent and pipeline definitions into
`~/.machinist/config.toml`, copy its prompt files under `~/.machinist/agents/`, and register
the target checkout in `~/.machinist/worker.toml`:

```toml
[repositories.my-project]
path = "/absolute/path/to/my-project"
```

Restart `machinist start` and `machinist worker start`, then submit a configured workflow. For
example:

```sh
machinist submit \
  --agent=issue-to-pr \
  --repo=my-project \
  --prompt="Complete https://github.com/owner/repository/issues/123"
```
