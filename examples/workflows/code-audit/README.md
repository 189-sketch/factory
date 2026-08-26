# Code audit

This example inspects a repository for correctness bugs. It separates discovery from
verification and opens at most three deduplicated GitHub issues. It never changes code.

Factory does not schedule jobs yet, so this is a manual run. The prompt is ready to use
unchanged if scheduled intake is added later.

## Set up

You need authenticated `git`, `gh`, and `codex` commands. Initialize Factory once and make
sure the `codex` executor exists in `~/.factory/worker.toml`:

```sh
factory init
```

Set these paths for your checkouts:

```sh
FACTORY_EXAMPLE_ROOT=/absolute/path/to/factory-v2/examples/workflows/code-audit
FACTORY_TARGET_REPO=/absolute/path/to/the/repository
```

## Run it

Name a bounded area rather than asking for a vague review of everything:

```sh
factory run \
  --factory-config="$FACTORY_EXAMPLE_ROOT/config.toml" \
  --agent=code-audit \
  --repo="$FACTORY_TARGET_REPO" \
  --prompt="Audit request validation and SQLite persistence for correctness bugs"
```

The final line reports the number of candidates, independently verified bugs, and issue
URLs. No issue means the run found no bug that met the evidence bar.
