# Development

## Requirements

- Go 1.26.6 or newer
- Node.js 22.22.2 and npm for the control-plane frontend
- `just` to use the repository shortcuts

## Build

Build the React application, embed it, and compile Machinist:

```sh
just build
```

The binary is written to `bin/machinist`.

For backend-only changes that do not touch the frontend, the tracked production
assets allow a direct Go build:

```sh
go build ./...
```

## Run locally

For the current factory app, initialize a private runtime and start the binary:

```sh
./bin/machinist init
./bin/machinist start
```

Complete browser setup against a repository you control. Setup starts the managed
local worker automatically. Do not start a second explicit worker for this target.
Use a separate runtime and listener for tests; never point experiments at a live
cloud or production state directory. The default listener is port 7331, while
individual configurations may use another port.

`just local` is a lower-level development shortcut that starts both the example
control plane and an explicit worker using `~/.machinist/worker.toml`. It is not
the normal factory onboarding path. Do not use it alongside an already configured
managed local worker.

## Verify

Run the complete project check before opening a pull request:

```sh
just check
```

This installs the locked frontend dependencies, runs frontend tests, rebuilds
the embedded assets, runs Python eval tests, checks and vets the Go code, runs
Go tests with the race detector, and builds all Go packages.

Focused commands are also available:

```sh
just test
python3 -m unittest discover -s evals -p 'test_*.py'
(cd internal/controlplane/web && npm test)
go test ./internal/runner
```

CI requires `check`, `Linux checks`, and `macOS checks` on the current PR
revision. Run frontend builds before Go checks, not concurrently: Go embeds
`web/dist`, so replacing that directory during a build can race file discovery.
For infrastructure changes, distinguish fake-client tests from live cluster proof.
Use the [deployment guide](local-factory/cloud-control-plane.md) for state-preserving
rollouts; never put runtime archives or authentication files in build contexts.

## Project layout

```text
cmd/machinist/                CLI entry point
examples/                     embedded default configuration and prompts
internal/cli/                 command behavior
internal/config/              strict TOML loading and template resolution
internal/runner/              process execution and event recording
internal/controlplane/        HTTP server, SQLite store, and embedded UI
internal/managedworker/       polling, leases, execution, and result delivery
internal/kuberunner/          gVisor Jobs, proxy, auth retention, and cleanup
deploy/kubernetes/            runner and control-plane deployment resources
docs/                         user and design documentation
```

The frontend source lives in `internal/controlplane/web/src`. Its production
bundle lives in `internal/controlplane/web/dist` because Go embeds those files at
compile time.
