# Documentation

Machinist runs coding-agent jobs on infrastructure you control. Start with the
[setup guide](local-factory/README.md), then [run your first job](task-guide.md).
The app offers a Standard pipeline and a Single-agent task runner.

## Set up and use Machinist

| Guide | Use it to |
| --- | --- |
| [Setup](local-factory/README.md) | Connect the GitHub App, choose an agent and runner, verify PONG, and add projects |
| [Your first job](task-guide.md) | Submit work, approve a spec, inspect review, and confirm delivery |
| [Automations](local-factory/automations.md) | Save manual or scheduled jobs and configure repository maintenance |
| [GitHub follow-ups](local-factory/github-followups.md) | Understand CI repairs, review feedback, and merge confirmation |
| [Job state and recovery](local-factory/local-jobs.md) | Use the API safely, back up state, and manage publication holds |

## Run the infrastructure

| Guide | Use it to |
| --- | --- |
| [VM deployment](vm-deployment.md) | Host the control plane or connect a separate VM worker |
| [Kubernetes runners](local-factory/kubernetes.md) | Configure gVisor Jobs, native Codex login, isolation, and recovery |
| [GKE control plane](local-factory/cloud-control-plane.md) | Deploy the dispatcher and private SQLite state to an existing cluster |
| [Architecture](../ARCHITECTURE.md) | Understand ownership, execution boundaries, and failure handling |
| [Configuration](configuration.md) | Distinguish app setup from lower-level command and worker configuration |
| [Development](development.md) | Build, test, and contribute |

## Lower-level interfaces and examples

These are separate from the app's two built-in modes. Custom commands run through
`machinist run`; sequence workflows require the legacy SQLite control plane.
Their artifact contracts do not describe factory PR delivery.

- [Command and sequence reference](workflows.md)
- [Legacy artifact workflows](artifacts.md)
- [Runnable examples](../examples/README.md)
- [Optional Python issue-delivery adapter](agent-workflow.md)
- [Python workflow article](coding-workflows-in-code.md)
- [Standalone GitHub Actions triage demo](issue-triage-demo.md)

## Historical design notes

These record earlier decisions and proposals, not current setup instructions:
[task/artifact model](task-artifact-model.md), [workflow roadmap](workflow-roadmap.md),
[original factory spec](local-factory/spec.md), and
[Warp observations](local-factory/warp-evidence.md).
