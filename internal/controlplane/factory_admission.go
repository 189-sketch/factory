package controlplane

import (
	"context"
	"sort"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// All admission helpers run under mu. Unconfirmed shutdown remains an occupied
// slot even after the cleanup upload window or heartbeat lease expires.
type factoryAdmissionUsage struct {
	total                 int
	repositories          map[string]int
	uncertainRepositories map[string]bool
	workers               map[string]bool
}

func (s *GitHubFactoryServer) admissionUsage(ctx context.Context) factoryAdmissionUsage {
	u := factoryAdmissionUsage{repositories: map[string]int{}, uncertainRepositories: map[string]bool{}, workers: map[string]bool{}}
	// Automation stop confirmations already exist durably. Honor only the
	// jobs covered by that confirmation, never a newer cancellation.
	confirmed := map[string]bool{}
	if automations, err := s.local.automations(ctx); err == nil {
		for _, a := range automations {
			if a.StopConfirmedAt == nil {
				continue
			}
			urls, err := s.automationJobs(ctx, a)
			if err != nil {
				continue
			} // Fail closed if confirmation cannot be read.
			for _, task := range s.tasks {
				job := task.state.Job
				if urls[task.issue.HTMLURL] && job.State == "cancelled" && !job.UpdatedAt.After(*a.StopConfirmedAt) {
					confirmed[job.ID] = true
				}
			}
		}
	}
	counted := map[string]bool{}
	for _, task := range s.tasks {
		job := task.state.Job
		if len(job.Runs) == 0 {
			continue
		}
		run := job.Runs[len(job.Runs)-1]
		unfinished := (run.State == "cancelled" || run.State == "interrupted") && !run.StartedAt.IsZero() && run.CompletedAt.IsZero()
		uncertain := !run.ProcessStopped && (run.CleanupUnconfirmed || (unfinished && !confirmed[job.ID] && task.state.StopConfirmedRun != run.ID))
		if run.State != "running" && !uncertain {
			continue
		}
		counted[run.ID] = true
		u.total++
		u.repositories[job.Repository]++
		u.workers[run.WorkerName] = true
		if uncertain {
			u.uncertainRepositories[job.Repository] = true
		}
	}
	workers := s.liveWorkers()
	for id, lease := range s.liveLeases() {
		if worker, ok := workers[lease.instance]; ok {
			u.workers[worker.Name] = true
		}
		if counted[id] {
			continue
		}
		u.total++
		u.repositories[lease.spec.Repository]++
	}
	return u
}

// Round robin across repositories; oldest creation time then ID within each
// repository. Only a successful dispatch advances the cursor. Ineligible jobs
// are skipped so a worker cannot block work it can actually execute.
func (s *GitHubFactoryServer) dispatchOrder() []*githubTask {
	groups := map[string][]*githubTask{}
	for _, task := range s.tasks {
		if task.state.Job.State == "queued" && task.state.Job.PublicationHold == "" {
			repo := task.state.Job.Repository
			groups[repo] = append(groups[repo], task)
		}
	}
	repos := make([]string, 0, len(groups))
	for repo, tasks := range groups {
		repos = append(repos, repo)
		sort.Slice(tasks, func(i, j int) bool {
			a, b := tasks[i].state.Job, tasks[j].state.Job
			if a.CreatedAt.Equal(b.CreatedAt) {
				return a.ID < b.ID
			}
			return a.CreatedAt.Before(b.CreatedAt)
		})
	}
	sort.Strings(repos)
	start := sort.SearchStrings(repos, s.dispatchCursor)
	for start < len(repos) && repos[start] <= s.dispatchCursor {
		start++
	}
	ordered := []*githubTask{}
	for i := range repos {
		ordered = append(ordered, groups[repos[(start+i)%len(repos)]]...)
	}
	return ordered
}

func (s *GitHubFactoryServer) workerSupports(ctx context.Context, task *githubTask, p protocol.PollRequest) bool {
	job := task.state.Job
	if !p.Factory || !p.Workflows || !stringSet(p.Repositories)[job.Repository] {
		return false
	}
	if s.setup != nil {
		selection := s.setup.snapshot()
		if !selection.Finished || (selection.Selection.Host == "kubernetes") != (p.Runtime == "kubernetes") {
			return false
		}
	}
	if job.Recipe == "native-v1" && (!p.NativeAgents || p.Runtime == "kubernetes") {
		return false
	}
	if (job.Recipe == "single" && !p.SingleAgent) || (job.Recipe != "single" && !p.Reviews) {
		return false
	}
	if job.Workflow == nil {
		return false
	}
	executors, readonly := stringSet(p.Executors), stringSet(p.ReadOnlyExecutors)
	for _, stage := range job.Workflow.Steps {
		command, err := s.jobCommand(ctx, task, stage)
		if err != nil {
			return false
		}
		if job.Recipe == "native-v1" && p.Adapters[command.Executor] != "claude-stream" && p.Adapters[command.Executor] != "codex-app-server" {
			return false
		}
		if !executors[command.Executor] || !supportsModel(p.Models, command.Executor, command.Model) || ((stage == "review" || stage == "triage" || job.Recipe == "answer") && !readonly[command.Executor]) {
			return false
		}
	}
	return true
}

func (s *GitHubFactoryServer) waitingReasons(ctx context.Context, task *githubTask, u factoryAdmissionUsage) []string {
	job := task.state.Job
	if job.PublicationHold != "" {
		return []string{"publication_hold"}
	}
	reasons := []string{}
	if u.uncertainRepositories[job.Repository] {
		reasons = append(reasons, "cleanup_unconfirmed")
	}
	if s.maxConcurrent > 0 && u.total >= s.maxConcurrent {
		reasons = append(reasons, "global_capacity")
	}
	if s.maxPerRepository > 0 && u.repositories[job.Repository] >= s.maxPerRepository {
		reasons = append(reasons, "repository_capacity")
	}
	capable, available, serialized := false, false, false
	for id, worker := range s.liveWorkers() {
		p, ok := s.workerCapabilities[id]
		if !ok || time.Since(worker.LastSeenAt) >= workerAvailabilityWindow || !s.workerSupports(ctx, task, p) {
			continue
		}
		capable = true
		if !u.workers[worker.Name] {
			available = true
		}
		if p.Runtime == "kubernetes" {
			serialized = true
		}
	}
	if !capable {
		reasons = append(reasons, "awaiting_runner")
	} else if !available {
		reasons = append(reasons, "runner_busy")
	}
	// This is a known constraint, not a measurement of lock ownership or queue
	// position. The control plane cannot observe the Kubernetes credential lock.
	if serialized {
		reasons = append(reasons, "credential_serialization_possible")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "awaiting_dispatch")
	}
	return reasons
}

func (s *GitHubFactoryServer) capacityStatus(u factoryAdmissionUsage) map[string]any {
	names := map[string]bool{}
	for id, worker := range s.liveWorkers() {
		p := s.workerCapabilities[id]
		if time.Since(worker.LastSeenAt) < workerAvailabilityWindow && p.Factory && p.Workflows {
			if s.setup != nil {
				state := s.setup.snapshot()
				if !state.Finished || (state.Selection.Host == "kubernetes") != (p.Runtime == "kubernetes") {
					continue
				}
			}
			names[worker.Name] = true
		}
	}
	upper := len(names)
	if s.maxConcurrent > 0 {
		upper = min(upper, s.maxConcurrent)
	}
	return map[string]any{
		"max_concurrent_jobs":                      s.maxConcurrent,
		"max_concurrent_jobs_per_repository":       s.maxPerRepository,
		"occupied_slots":                           u.total,
		"repository_occupied_slots":                u.repositories,
		"connected_factory_workers":                len(names),
		"effective_capacity_upper_bound":           upper,
		"managed_worker_parallelism":               1,
		"kubernetes_credential_stream_parallelism": 1,
		"credential_lock_state":                    "unobserved",
	}
}
