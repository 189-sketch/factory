package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestFactoryCatalogSynchronizesProjectChanges(t *testing.T) {
	s, _ := setupTestServer(t)
	var changes sync.WaitGroup
	changes.Add(1)
	go func() {
		defer changes.Done()
		for i := 0; i < 500; i++ {
			s.mu.Lock()
			s.repositories[fmt.Sprint("project", i)] = "owner/repo"
			s.mu.Unlock()
		}
	}()
	for i := 0; i < 50; i++ {
		factoryHTTP(t, s, "GET", "/api/v1/catalog", nil, 200)
	}
	changes.Wait()
}
func TestFactoryRunRetainsPromptAndExecutionMetrics(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	var created map[string]string
	json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": "single", "spec": "Check health"}, 201), &created)
	assigned := factoryPoll(t, s)
	completion := factoryCompletion(assigned, protocol.FactoryResult{Report: "Healthy"})
	var payload map[string]any
	json.Unmarshal(completion.Result, &payload)
	payload["token_usage"], payload["duration_millis"] = 123, 456
	completion.Result, _ = json.Marshal(payload)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+assigned.ID+"/complete", completion, 204)
	s = restartFactoryTestServer(t, s, client)
	run := factoryJob(t, s, created["id"]).Runs[0]
	if run.Prompt != assigned.RenderedPrompt || run.CommandHash != assigned.CommandHash || run.TokenUsage == nil || *run.TokenUsage != 123 || run.DurationMillis == nil || *run.DurationMillis != 456 || run.ExitCode == nil || *run.ExitCode != 0 {
		t.Fatalf("lost audit metadata: %+v", run)
	}
}
func TestOversizedRenderedPromptBecomesBlocked(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	// JSON's HTML escaping can expand an otherwise valid prompt past the event limit.
	if _, err := s.local.savePrompt(t.Context(), "", "triage", strings.Repeat("<", 180<<10), "", ""); err != nil {
		t.Fatal(err)
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "spec": "Check"}, 201)
	if run := factoryPoll(t, s); run != nil {
		t.Fatal("oversized stage dispatched")
	}
	for _, task := range s.tasks {
		if task.state.Job.State != "blocked" || !strings.Contains(task.state.Job.Runs[0].Summary, "too large") {
			t.Fatal("oversized task did not become actionable")
		}
	}
}
func TestAutomationCancelledQueuedRetryNeedsNoProcessConfirmation(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "")
	id := automationRun(t, s, a, "first", 201)
	run := factoryPoll(t, s)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", protocol.Completion{InstanceID: "worker-instance", LeaseToken: run.LeaseToken, State: "failed", Error: "failed"}, 204)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID}, 200)
	job := factoryJob(t, s, id)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]any{"run_id": job.Runs[len(job.Runs)-1].ID}, 200)
	automationRun(t, s, a, "next", 201)
}

func TestAutomationListImmediatelyAfterAdmission(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	a := createAutomation(t, s, "")
	automationRun(t, s, a, "run", 201)
	var list []factoryAutomation
	json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/automations", nil, 200), &list)
	if len(list) != 1 || !list[0].Busy {
		t.Fatal("newly admitted job missing from automation runtime status")
	}
}
