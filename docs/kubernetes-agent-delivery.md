# Kubernetes agent delivery

Machinist's control plane can stay on your computer or a VM while Codex runs in
Kubernetes. Each stage runs as one isolated Kubernetes Job.

Triage and Review receive read-only source. Agent pods receive no GitHub
credentials or Kubernetes API credentials. The trusted connector publishes the
task branch and pull request.

Review the pull request and decide when to merge. You remain in control of merging.
