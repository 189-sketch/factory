package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func admissionSubmit(t *testing.T, s *GitHubFactoryServer, repo string) string {
	t.Helper()
	raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": repo, "workflow": "factory", "title": "Task", "spec": "Useful change"}, 201)
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result["id"]
}
func admissionWorker(name string, repos ...string) protocol.PollRequest {
	return protocol.PollRequest{InstanceID: name, Name: name, Repositories: repos, Executors: []string{"codex"}, ReadOnlyExecutors: []string{"codex"}, Factory: true, Workflows: true, Reviews: true, SingleAgent: true}
}
func admissionPoll(t *testing.T, s *GitHubFactoryServer, p protocol.PollRequest) *protocol.RunSpec {
	t.Helper()
	var result protocol.PollResponse
	if err := json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200), &result); err != nil {
		t.Fatal(err)
	}
	return result.Run
}
func admissionFinish(t *testing.T, s *GitHubFactoryServer, p protocol.PollRequest, run *protocol.RunSpec) {
	t.Helper()
	completion := factoryCompletion(run, protocol.FactoryResult{Resolved: true})
	completion.InstanceID = p.InstanceID
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
}

func TestFactoryAdmissionFairFIFOAndReplay(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories["other"] = "owner/other"
	s.maxConcurrent = 2
	first := admissionSubmit(t, s, "other")
	second := admissionSubmit(t, s, "other")
	third := admissionSubmit(t, s, "repo")
	fourth := admissionSubmit(t, s, "repo")
	a, b, c := admissionWorker("a", "other", "repo"), admissionWorker("b", "other", "repo"), admissionWorker("c", "other", "repo")
	r1 := admissionPoll(t, s, a)
	if r1 == nil || r1.JobID != first {
		t.Fatalf("oldest first: %+v", r1)
	}
	r2 := admissionPoll(t, s, b)
	if r2 == nil || r2.JobID != third {
		t.Fatalf("round robin: %+v", r2)
	}
	if r := admissionPoll(t, s, c); r != nil {
		t.Fatalf("global cap exceeded: %+v", r)
	}
	if r := admissionPoll(t, s, a); r == nil || r.ID != r1.ID || r.LeaseToken != r1.LeaseToken {
		t.Fatalf("lease replay at capacity: %+v", r)
	}
	admissionFinish(t, s, a, r1)
	r3 := admissionPoll(t, s, c)
	if r3 == nil || r3.JobID != second {
		t.Fatalf("FIFO second: %+v", r3)
	}
	admissionFinish(t, s, b, r2)
	r4 := admissionPoll(t, s, a)
	if r4 == nil || r4.JobID != fourth {
		t.Fatalf("FIFO other project: %+v", r4)
	}
}

func TestFactoryAdmissionRepositoryCapAndUnsupportedWork(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories["other"] = "owner/other"
	s.maxConcurrent, s.maxPerRepository = 3, 1
	first := admissionSubmit(t, s, "other")
	admissionSubmit(t, s, "other")
	eligible := admissionSubmit(t, s, "repo")
	a, b := admissionWorker("a", "other", "repo"), admissionWorker("b", "other", "repo")
	if r := admissionPoll(t, s, a); r == nil || r.JobID != first {
		t.Fatalf("first: %+v", r)
	}
	// Force the capped project to the front: its queued job must not block repo.
	s.dispatchCursor = ""
	if r := admissionPoll(t, s, b); r == nil || r.JobID != eligible {
		t.Fatalf("repository cap: %+v", r)
	}
	if r := admissionPoll(t, s, admissionWorker("c", "other", "repo")); r != nil {
		t.Fatalf("per-project cap exceeded: %+v", r)
	}
}

func TestFactoryAdmissionSkipsUnsupportedProject(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories["aaa"] = "owner/aaa"
	raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "aaa", "recipe": "single", "spec": "Single agent job"}, 201)
	var created map[string]string
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	unsupported := created["id"]
	eligible := admissionSubmit(t, s, "repo")
	// A project with unsupported recipe capabilities precedes an eligible project.
	p := admissionWorker("a", "aaa", "repo")
	p.SingleAgent = false
	if r := admissionPoll(t, s, p); r == nil || r.JobID != eligible {
		t.Fatalf("unsupported job blocked dispatch: %+v", r)
	}
	if job := factoryJob(t, s, unsupported); !slices.Contains(job.WaitingReasons, "awaiting_runner") {
		t.Fatalf("missing runner reason: %+v", job.WaitingReasons)
	}
}

func TestFactoryAdmissionCancelledCleanup(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged", true: "expired"}[expire], func(t *testing.T) {
			s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
			s.repositories["other"] = "owner/other"
			s.maxConcurrent = 2
			id := admissionSubmit(t, s, "repo")
			a := admissionWorker("a", "repo")
			run := admissionPoll(t, s, a)
			factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": run.ID}, 200)
			blocked := admissionSubmit(t, s, "repo")
			other := admissionSubmit(t, s, "other")
			if expire {
				s.liveMu.Lock()
				s.cleanupLeases[run.ID].cleanupUntil = time.Now().Add(-time.Second)
				s.liveMu.Unlock()
				s.cleanupLease(run.ID)
			}
			if r := admissionPoll(t, s, admissionWorker("b", "repo")); r != nil {
				t.Fatalf("uncertain cleanup admitted conflict: %+v", r)
			}
			if r := admissionPoll(t, s, admissionWorker("a", "other")); r != nil {
				t.Fatalf("worker with uncertain cleanup admitted other work: %+v", r)
			}
			if job := factoryJob(t, s, blocked); !slices.Contains(job.WaitingReasons, "cleanup_unconfirmed") {
				t.Fatalf("missing cleanup reason: %+v", job.WaitingReasons)
			}
			if r := admissionPoll(t, s, admissionWorker("c", "other")); r == nil || r.JobID != other {
				t.Fatalf("independent work blocked: %+v", r)
			}
			if r := admissionPoll(t, s, admissionWorker("d", "repo", "other")); r != nil {
				t.Fatal("cleanup not counted globally")
			}
			if !expire {
				completion := protocol.Completion{InstanceID: a.InstanceID, LeaseToken: run.LeaseToken, ProcessStopped: true, State: "cancelled"}
				factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
				if r := admissionPoll(t, s, a); r == nil || r.JobID != blocked {
					t.Fatalf("confirmed cleanup did not free slot: %+v", r)
				}
			} else {
				s = restartFactoryTestServer(t, s, newMemoryFactoryGitHub())
				if r := admissionPoll(t, s, admissionWorker("b", "repo")); r != nil {
					t.Fatal("restart forgot uncertain cleanup")
				}
			}
		})
	}
}

func TestFactoryAdmissionStatusAndStableTie(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	first, second := factorySubmit(t, s), factorySubmit(t, s)
	stamp := time.Now()
	s.tasks[first].state.Job.CreatedAt, s.tasks[second].state.Job.CreatedAt = stamp, stamp
	ordered := s.dispatchOrder()
	if ordered[0].state.Job.ID != min(first, second) {
		t.Fatal("unstable creation-time tie")
	}
	p := admissionWorker("a", "repo")
	p.Runtime = "kubernetes"
	admissionPoll(t, s, p)
	var status struct {
		Capacity map[string]any
		Jobs     []Job
	}
	if err := json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200), &status); err != nil {
		t.Fatal(err)
	}
	if status.Capacity["occupied_slots"] != float64(1) || status.Capacity["effective_capacity_upper_bound"] != float64(1) || status.Capacity["credential_lock_state"] != "unobserved" {
		t.Fatalf("capacity: %+v", status.Capacity)
	}
	for _, job := range status.Jobs {
		if job.State == "queued" && (!slices.Contains(job.WaitingReasons, "global_capacity") || !slices.Contains(job.WaitingReasons, "runner_busy") || !slices.Contains(job.WaitingReasons, "credential_serialization_possible")) {
			t.Fatalf("waiting: %+v", job.WaitingReasons)
		}
	}
}

func TestFactoryAdmissionInterruptedRetainsCapacity(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories["other"] = "owner/other"
	id := admissionSubmit(t, s, "repo")
	run := admissionPoll(t, s, admissionWorker("a", "repo"))
	queued := admissionSubmit(t, s, "other")
	s.liveMu.Lock()
	s.leases[run.ID].last = time.Now().Add(-2 * leaseDuration)
	s.liveMu.Unlock()
	s.loaded = time.Time{}
	if r := admissionPoll(t, s, admissionWorker("b", "other")); r != nil {
		t.Fatal("expired lease freed uncertain slot")
	}
	if job := factoryJob(t, s, id); job.State != "interrupted" {
		t.Fatalf("state=%s", job.State)
	}
	if job := factoryJob(t, s, queued); !slices.Contains(job.WaitingReasons, "global_capacity") {
		t.Fatalf("reasons=%v", job.WaitingReasons)
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID, "previous_process_stopped": true}, 200)
	if r := admissionPoll(t, s, admissionWorker("b", "other")); r == nil || r.JobID != queued {
		t.Fatalf("confirmed stop did not free capacity: %+v", r)
	}
}

func TestFactoryAdmissionPublicationStopConfirmation(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := factorySubmit(t, s)
	run := factoryPoll(t, s)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": run.ID}, 200)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]any{"feedback": "Private", "previous_process_stopped": true}, 200)
	next := factorySubmit(t, s)
	s = restartFactoryTestServer(t, s, newMemoryFactoryGitHub())
	if r := factoryPoll(t, s); r == nil || r.JobID != next {
		t.Fatalf("durable stop confirmation ignored: %+v", r)
	}
	if job := factoryJob(t, s, id); job.PublicationHold != "Private" {
		t.Fatal("publication hold lost")
	}
}

func TestFactoryAdmissionConcurrentPollsRespectCaps(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories["other"] = "owner/other"
	s.maxConcurrent, s.maxPerRepository = 2, 1
	for range 3 {
		admissionSubmit(t, s, "repo")
		admissionSubmit(t, s, "other")
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := range 8 {
		go func() {
			raw, _ := json.Marshal(admissionWorker(fmt.Sprintf("worker-%d", i), "repo", "other"))
			req := httptest.NewRequest("POST", "/api/v1/workers/poll", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer secret")
			out := httptest.NewRecorder()
			s.Handler().ServeHTTP(out, req)
			responses <- out
		}()
	}
	admitted := map[string]int{}
	for range 8 {
		out := <-responses
		if out.Code != 200 {
			t.Fatalf("poll status %d: %s", out.Code, out.Body.String())
		}
		var response protocol.PollResponse
		if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Run != nil {
			admitted[response.Run.Repository]++
		}
	}
	if admitted["repo"] != 1 || admitted["other"] != 1 {
		t.Fatalf("concurrent admission: %v", admitted)
	}
}

func TestFactoryAdmissionFailedCleanupPersistsUntilConfirmedRetry(t *testing.T) {
	for _, mode := range []string{"stopped", "legacy", "unsafe"} {
		t.Run(mode, func(t *testing.T) {
			unsafe := mode == "unsafe"
			s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
			s.repositories["other"] = "owner/other"
			id := admissionSubmit(t, s, "repo")
			a := admissionWorker("a", "repo")
			run := admissionPoll(t, s, a)
			completion := protocol.Completion{InstanceID: a.InstanceID, LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1, Error: "process enumeration failed", CleanupUnconfirmed: unsafe, ProcessStopped: mode == "stopped"}
			factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
			admissionSubmit(t, s, "repo")
			admissionSubmit(t, s, "other")
			if unsafe {
				for _, restart := range []bool{false, true} {
					if restart {
						s = restartFactoryTestServer(t, s, newMemoryFactoryGitHub())
					}
					job := factoryJob(t, s, id)
					if !job.Runs[0].CleanupUnconfirmed || job.Runs[0].CompletedAt.IsZero() {
						t.Fatalf("lost completion evidence: %+v", job.Runs[0])
					}
					if r := admissionPoll(t, s, admissionWorker("b", "other")); r != nil {
						t.Fatal("global slot released")
					}
					s.maxConcurrent = 3
					if r := admissionPoll(t, s, admissionWorker("b", "repo")); r != nil {
						t.Fatal("repository conflict admitted")
					}
					if r := admissionPoll(t, s, admissionWorker("a", "other")); r != nil {
						t.Fatal("worker conflict admitted")
					}
					s.maxConcurrent = 1
				}
				factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID}, 409)
			}
			factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID, "previous_process_stopped": unsafe}, 200)
			s = restartFactoryTestServer(t, s, newMemoryFactoryGitHub())
			if r := admissionPoll(t, s, a); r == nil || r.JobID != id {
				t.Fatalf("confirmed/safe retry blocked: %+v", r)
			}
		})
	}
}

func TestFactoryAdmissionHealthRespectsCapacityAndReplay(t *testing.T) {
	for _, constraint := range []string{"global", "repository", "worker"} {
		t.Run(constraint, func(t *testing.T) {
			s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
			id := admissionSubmit(t, s, "repo")
			a := admissionWorker("a", "repo")
			run := admissionPoll(t, s, a)
			s.maxConcurrent = 3
			if constraint == "global" {
				s.maxConcurrent = 1
			}
			if constraint == "repository" {
				s.maxPerRepository = 1
			}
			s.setup = &onboarding{server: s, settings: setupSettings{Finished: true}, health: &setupHealth{spec: protocol.RunSpec{ID: "health", Repository: "repo", Executor: "codex", HealthCheck: true}}}
			p := admissionWorker("b", "repo")
			p.HealthChecks = true
			if constraint == "worker" {
				p.Name = "a"
			}
			if r := admissionPoll(t, s, p); r != nil {
				t.Fatalf("health bypassed %s: %+v", constraint, r)
			}
			a.HealthChecks = true
			if r := admissionPoll(t, s, a); r == nil || r.ID != run.ID {
				t.Fatalf("health displaced active lease: %+v", r)
			}
			factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": run.ID}, 200)
			if r := admissionPoll(t, s, p); r != nil {
				t.Fatal("health bypassed uncertain cleanup")
			}
			factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", protocol.Completion{InstanceID: a.InstanceID, LeaseToken: run.LeaseToken, ProcessStopped: true}, 204)
			p = admissionWorker("c", "repo")
			p.HealthChecks = true
			health := admissionPoll(t, s, p)
			if health == nil || !health.HealthCheck {
				t.Fatalf("health not admitted after cleanup: %+v", health)
			}
			if r := admissionPoll(t, s, p); r == nil || r.ID != health.ID {
				t.Fatal("health lease not replayed")
			}
		})
	}
}
