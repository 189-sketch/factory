package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func submitRecipe(t *testing.T, s *GitHubFactoryServer, recipe, key string) string {
	t.Helper()
	if recipe == "direct" || recipe == "answer" {
		issue := GitHubIssue{Title: "Imported job", Body: "Test requested behavior", HTMLURL: "local:legacy-" + key, State: "open", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := s.local.createRequest(context.Background(), localFactoryTask{issue: issue, repository: "owner/repo", alias: "repo"}, "", "", recipe); err != nil {
			t.Fatal(err)
		}
		if err := s.refresh(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		for id, task := range s.tasks {
			if task.issue.HTMLURL == issue.HTMLURL {
				return id
			}
		}
		t.Fatal("imported recipe not restored")
	}
	raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": recipe, "client_request_id": key, "spec": "Test requested behavior"}, 201)
	var created map[string]string
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	return created["id"]
}

func TestLocalDirectJobSubmissionAndReview(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := submitRecipe(t, s, "direct", "one")
	run := factoryPoll(t, s)
	if run == nil || run.Factory.Role != "implement" || run.Factory.Recipe != "direct" {
		t.Fatalf("expected direct implementation: %+v", run)
	}
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Branch: "codex/task"})
	review := factoryPoll(t, s)
	if review == nil || review.Factory.Role != "review" {
		t.Fatal("direct delivery skipped independent review")
	}
	factoryFinish(t, s, review, protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Verdict: "accepted"})
	job := factoryJob(t, s, id)
	if job.State != "awaiting_merge" || len(job.Runs) != 2 || job.Runs[0].DurationMillis == nil {
		t.Fatalf("job %+v", job)
	}
	if client.creates != 0 || len(client.comments) != 0 {
		t.Fatal("local job created issue or journal")
	}
}

func TestLocalSubmissionIsIdempotentAcrossRestart(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := submitRecipe(t, s, "single", "retry-key")
	s = restartFactoryTestServer(t, s, client)
	raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": "single", "client_request_id": "retry-key", "spec": "Test requested behavior"}, 200)
	var replay map[string]string
	json.Unmarshal(raw, &replay)
	if replay["id"] != id || len(s.tasks) != 1 || factoryJob(t, s, id).Recipe != "single" {
		t.Fatal("duplicate or recipe changed after restart")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": "factory", "client_request_id": "retry-key", "spec": "Test requested behavior"}, 409)
	if len(s.tasks) != 1 {
		t.Fatal("conflicting replay created work")
	}
}

func TestLocalAnswerCannotRouteOrPublishDeliverable(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		t.Run(map[bool]string{false: "routes-to-build", true: "claims-delivery"}[resolved], func(t *testing.T) {
			s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
			id := submitRecipe(t, s, "answer", "answer")
			run := factoryPoll(t, s)
			if run == nil || run.Factory.Recipe != "answer" {
				t.Fatal("missing answer assignment")
			}
			f := protocol.FactoryResult{Resolved: resolved}
			if resolved {
				f.PRURL = "https://github.com/owner/repo/pull/1"
			}
			factoryFinish(t, s, run, f)
			if factoryJob(t, s, id).State != "blocked" || factoryPoll(t, s) != nil {
				t.Fatal("answer escalated into delivery")
			}
		})
	}
}

func TestPrivateHoldSurvivesRestartAndAllNormalActions(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	job := factoryJob(t, s, id)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]any{"hold_version": job.HoldVersion, "feedback": "Keep report private"}, 200)
	s = restartFactoryTestServer(t, s, client)
	job = factoryJob(t, s, id)
	if job.PublicationHold != "Keep report private" || factoryPoll(t, s) != nil {
		t.Fatal("held work dispatched")
	}
	for _, action := range []string{"retry", "approve", "request_changes", "complete_merge"} {
		factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/"+action, map[string]any{"run_id": job.Runs[len(job.Runs)-1].ID}, 409)
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/release_publication", map[string]any{"hold_version": job.HoldVersion - 1}, 409)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/release_publication", map[string]any{"hold_version": job.HoldVersion}, 200)
	if factoryPoll(t, s) == nil {
		t.Fatal("dedicated release did not restore dispatch")
	}
}

func TestLocalStoreMigrationRetainsHistoryAndReceipts(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	run := factoryPoll(t, s)
	completion := factoryCompletion(run, protocol.FactoryResult{})
	// Failed state and receipt are durable v1 data; seed migration with blocked work.
	completion.Result = []byte(`{"step_result":{"outcome":"blocked","summary":"Private report","factory":{"version":1,"role":"triage","report":"private"}}}`)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	if _, err := s.local.db.Exec(`DELETE FROM store_metadata`); err != nil {
		t.Fatal(err)
	}
	path := s.dataDirectory
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(path, "jobs.db"), filepath.Join(path, "factory.db")); err != nil {
		t.Fatal(err)
	}
	next, err := newGitHubFactoryServer(s.definitionPath, "secret", 1, s.repositories, client)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	job := factoryJob(t, next, id)
	if job.PublicationHold == "" || job.State != "blocked" || len(job.Runs) != 1 {
		t.Fatalf("lost blocked history: %+v", job)
	}
	if !next.local.receipt(context.Background(), run.ID, "worker-instance", run.LeaseToken) {
		t.Fatal("lost completion receipt")
	}
	if _, err = os.Stat(filepath.Join(path, "archive/factory-v1.db")); err != nil {
		t.Fatal(err)
	}
	if _, err = newGitHubFactoryServer(s.definitionPath, "secret", 1, s.repositories, client); err == nil {
		t.Fatal("second control plane opened active store")
	}
}

func TestAnswerFeedbackCannotSwitchIntoImplementation(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "answer", "answer-feedback")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{})
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/request_changes", map[string]any{"run_id": run.ID, "feedback": "Build instead"}, 409)
	if factoryJob(t, s, id).Runs[0].Command != "triage" || factoryPoll(t, s) != nil {
		t.Fatal("Answer routed into implementation")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/retry", map[string]any{"run_id": run.ID, "feedback": "Answer only"}, 200)
	if next := factoryPoll(t, s); next == nil || next.Factory.Role != "triage" || next.Factory.Recipe != "answer" {
		t.Fatal("retry changed recipe")
	}
}

func TestExplicitFactoryCannotResolveWithoutDelivery(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "factory", "delivery")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{Resolved: true})
	if factoryJob(t, s, id).State != "blocked" {
		t.Fatal("delivery job completed without PR")
	}
}

func TestCancelledJobHoldRequiresStoppedConfirmation(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "direct", "cancel-held")
	run := factoryPoll(t, s)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]any{"run_id": run.ID}, 200)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]any{"feedback": "Private"}, 409)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]any{"feedback": "Private", "previous_process_stopped": true}, 200)
	job := factoryJob(t, s, id)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/release_publication", map[string]any{"hold_version": job.HoldVersion}, 409)
}

func TestRepositoryStatusIncludesCanonicalMapping(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	raw := factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200)
	var status struct {
		Repositories []string          `json:"repositories"`
		Mappings     map[string]string `json:"repository_mappings"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Repositories) != 1 || status.Repositories[0] != "repo" || status.Mappings["repo"] != "owner/repo" {
		t.Fatalf("alias mapping lost: %+v", status)
	}
}

func TestExplicitAnswerSucceedsWithoutPR(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := submitRecipe(t, s, "answer", "positive-answer")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{Resolved: true, Report: "PONG"})
	job := factoryJob(t, s, id)
	if job.State != "succeeded" || len(job.Runs) != 1 || factoryPoll(t, s) != nil || client.creates != 0 {
		t.Fatalf("Answer did not complete without delivery: %+v", job)
	}
}

func TestDefaultClaudeReadOnlyProfileDisablesCustomizations(t *testing.T) {
	profile := setupExecutors("claude")["claude-review"]
	command := strings.Join(profile.Command, " ")
	for _, flag := range []string{"--safe-mode", "--restricted", "--strict-mcp-config", "--tools Read,Glob,Grep"} {
		if !strings.Contains(command, flag) {
			t.Fatalf("review profile lacks %s", flag)
		}
	}
	if !profile.ReadOnly {
		t.Fatal("review profile is not read-only")
	}
}

func TestFailedReviewCanReviseExistingPR(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "direct", "repair-review")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Branch: "codex/task"})
	review := factoryPoll(t, s)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+review.ID+"/complete", protocol.Completion{InstanceID: "worker-instance", LeaseToken: review.LeaseToken, State: "failed", Error: "invalid JSON"}, 204)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/request_changes", map[string]any{"run_id": review.ID, "feedback": "Clarify the guide's prerequisites"}, 200)
	next := factoryPoll(t, s)
	if next == nil || next.Factory.Role != "implement" || next.Factory.PRURL != "https://github.com/owner/repo/pull/2" || next.Factory.Branch != "codex/task" {
		t.Fatal("review failure lost task PR during repair")
	}
}

func TestSingleAgentOperationalTaskHasNoReviewOrPR(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "single", "operational")
	run := factoryPoll(t, s)
	if run == nil || run.Factory.Role != "implement" || run.Factory.Recipe != "single" {
		t.Fatal("wrong single-agent assignment")
	}
	factoryFinish(t, s, run, protocol.FactoryResult{Report: "PONG"})
	job := factoryJob(t, s, id)
	if job.State != "succeeded" || len(job.Runs) != 1 || factoryPoll(t, s) != nil {
		t.Fatalf("single task did not complete directly: %+v", job)
	}
}
func TestSingleAgentPRWaitsForHumanWithoutReview(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "single", "single-pr")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Branch: "codex/single"})
	job := factoryJob(t, s, id)
	if job.State != "awaiting_merge" || len(job.Runs) != 1 || factoryPoll(t, s) != nil {
		t.Fatal("single agent invoked a reviewer or completed before human merge")
	}
}
func TestOnlyTwoBuiltInModesCanBeSubmitted(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	for i, mode := range []string{"answer", "direct", "custom"} {
		factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": mode, "spec": "test", "client_request_id": fmt.Sprint(i)}, 400)
	}
}

func TestSingleAgentRepairCannotForgetItsPR(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "single", "single-existing-pr")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Branch: "codex/single"})
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/request_changes", map[string]any{"run_id": run.ID, "feedback": "Revise the existing PR"}, 200)
	repair := factoryPoll(t, s)
	factoryFinish(t, s, repair, protocol.FactoryResult{Report: "No more work"})
	if factoryJob(t, s, id).State != "blocked" {
		t.Fatal("repair completed without its existing PR or merge")
	}
}

func TestSingleAgentRequiresExplicitWorkerCapability(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	submitRecipe(t, s, "single", "capability")
	p := protocol.PollRequest{InstanceID: "one", Name: "worker", Factory: true, Workflows: true, Executors: []string{"codex"}, Repositories: []string{"repo"}}
	var polled protocol.PollResponse
	raw := factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200)
	json.Unmarshal(raw, &polled)
	if polled.Run != nil {
		t.Fatal("legacy worker received new single-agent contract")
	}
	p.SingleAgent = true
	raw = factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200)
	json.Unmarshal(raw, &polled)
	if polled.Run == nil {
		t.Fatal("single-agent runner incorrectly requires Review capability")
	}
}
