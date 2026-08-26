# Multi-review pipeline

This example reviews one pull request twice: Codex first, then Claude Code. Each stage gets
the same pull request URL and forms its judgment before reading existing reviews. Both
post a clearly named review comment.

Factory pipelines are sequential today. The second review starts after the first exits.
The stages do not pass prompt output to each other; the pull request is their durable
shared state. Parallel pipeline stages are intentionally outside this example.

## Set up

You need authenticated `git`, `gh`, `codex`, and `claude` commands. The default local
executors can access your host and credentials, so use this command only for a pull request
whose head you trust to execute. This example does not safely review untrusted code.
Doing that requires credential-free, network-restricted check execution and a separate
trusted process for GitHub access.

Initialize Factory once and confirm both executors exist in `~/.factory/worker.toml`:

```sh
factory init
```

Set these paths for your checkouts:

```sh
FACTORY_EXAMPLE_ROOT=/absolute/path/to/factory-v2/examples/workflows/multi-review
FACTORY_TARGET_REPO=/absolute/path/to/the/repository
```

## Run both reviews

```sh
factory run \
  --factory-config="$FACTORY_EXAMPLE_ROOT/config.toml" \
  --pipeline=multi-review \
  --repo="$FACTORY_TARGET_REPO" \
  --prompt="Review https://github.com/owner/repository/pull/123. I trust this pull request head to execute on this machine."
```

Do not pass `--model` to this mixed-executor pipeline. A model selection applies to a
whole run, while Codex and Claude use different model names. Configure each command's
default model in `~/.factory/worker.toml` or let each CLI use its own default.

To run only one review, select its agent directly:

```sh
factory run \
  --factory-config="$FACTORY_EXAMPLE_ROOT/config.toml" \
  --agent=review-codex \
  --repo="$FACTORY_TARGET_REPO" \
  --prompt="Review https://github.com/owner/repository/pull/123. I trust this pull request head to execute on this machine."
```

Replace `review-codex` with `review-claude` for the Claude Code stage.
