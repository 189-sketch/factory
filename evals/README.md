# Local evals

The first Factory eval exercises the complete default workflow and checks its issue-label
lifecycle. It is intentionally small. It does not judge implementation quality.

The eval creates a disposable issue and allows the `foreman` agent to create a pull
request. After capturing the issue events, it closes the pull request and issue, deletes
the remote branch, and removes the generated local worktree when it is clean. GitHub does
not allow pull requests or issues to be deleted, so the closed resources remain in the
scratch repository's history.

Use a dedicated scratch repository with a clean local checkout. The GitHub CLI must be
authenticated, and the Factory configuration must define the default `foreman` agent.

```sh
just build

python3 -m evals.pipeline_labels \
  --repository=your-org/factory-evals \
  --repo-path=/absolute/path/to/factory-evals \
  --factory=./bin/factory
```

Optional `--worker-config`, `--factory-config`, and `--model` arguments select non-default
Factory configuration. The command prints agent output while it runs and exits non-zero
when Factory fails, the label lifecycle is wrong, or cleanup is incomplete.

Run the local, non-mutating tests with:

```sh
python3 -m unittest discover -s evals -p 'test_*.py'
```
