# Run Machinist on a VM

A VM can host the whole control plane and local worker, or act as a remote worker
for a control plane elsewhere. Choose one arrangement. Do not start two control
planes against copies of the same active state.

## Host the control plane on the VM

Use a dedicated unprivileged account on a trusted Linux VM. Install Git, GitHub
CLI, Go 1.26.6, and the coding CLI you intend to use. Build the current source
from the repository root; the production React bundle is already committed:

```sh
go build -trimpath -o ./bin/machinist ./cmd/machinist
./bin/machinist init
./bin/machinist start
```

Authenticate the coding CLI interactively as the runtime account on that VM.
From your computer, forward the listener through SSH. The default port is 7331;
use the address printed by your actual configuration:

```sh
ssh -N -L 7331:127.0.0.1:7331 USER@VM_HOST
```

Open `http://127.0.0.1:7331` and complete [GitHub App setup](local-factory/README.md).
Choose **This computer**, meaning the VM hosting the control plane. The managed
local worker starts automatically after setup. Add repositories through Projects;
you do not need a separate worker service or a manually copied GitHub token.

Keep the control plane running under your service manager for scheduled
[automations](local-factory/automations.md). The checked-in
[control-plane unit](../deploy/systemd/machinist-control-plane.service) assumes
`/usr/local/bin/machinist` and `/home/machinist`; adapt these paths to your runtime
account before installing it. Never grant that account passwordless sudo or
access to unrelated host credentials. Keep the HTTP listener on loopback.

## Connect a remote worker instead

If the control plane stays on your computer, choose **Remote VM** in setup.
Install the current Machinist binary, Git, GitHub CLI and coding CLI on the VM.
Authenticate GitHub and the coding provider there. Ordinary VM pairing does not
send the control plane's GitHub App token to the VM.

Download the private worker setup script from the app. Run the displayed tunnel
command **on the VM**, then run the script there. The control-plane host must be
reachable over SSH. The script configures the worker, clones selected projects
using VM credentials and starts it. Finish with the browser's real PONG check.
Keep the script private because it contains a worker connection credential.

When adding projects later, stop the old worker, download updated VM setup from
Projects, and register the new project list. Do not run a duplicate worker under
the same identity. See [remote VM setup](local-factory/README.md#remote-vm).

## Update and recover

Before replacing the binary, stop admitting new work and wait for active agents
to finish or cancel and verify them stopped. Back up the full private runtime,
including a consistent SQLite snapshot, credentials, checkouts and worker outputs.
Install the reviewed binary and restart the same control plane. Confirm history,
projects, connection health and publication holds survived before new work.

`machinist update` installs a published release, which may lag current source.
Do not use an older release to open newer runtime state. For reproducible source
updates, record the commit and keep the previous binary plus a private backup.

The shipped `scripts/setup-vm.sh` and separate worker service also support older
releases and lower-level worker configurations. They do not replace current App
onboarding. Do not enable that worker alongside the app's managed local worker.

For an always-on cloud control plane with sandboxed agents, see the
[GKE deployment guide](local-factory/cloud-control-plane.md). Its dispatcher uses
an in-cluster service account instead of a personal gcloud login.
