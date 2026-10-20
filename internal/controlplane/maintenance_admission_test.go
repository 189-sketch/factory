package controlplane

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

type blockingMaintenance struct {
	*maintenanceFactoryFake
	phase   string
	entered chan struct{}
	release chan struct{}
}

func (f *blockingMaintenance) wait(ctx context.Context) error {
	select {
	case f.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *blockingMaintenance) MaintenanceSnapshot(ctx context.Context, repo string) (*maintenanceSnapshot, error) {
	if f.phase == "snapshot" {
		if err := f.wait(ctx); err != nil {
			return nil, err
		}
	}
	return f.maintenanceFactoryFake.MaintenanceSnapshot(ctx, repo)
}
func (f *blockingMaintenance) MaintenanceItem(ctx context.Context, repo string, item maintenanceItem) (maintenanceItem, error) {
	if f.phase == "reconcile" {
		if err := f.wait(ctx); err != nil {
			return item, err
		}
	}
	return f.maintenanceFactoryFake.MaintenanceItem(ctx, repo, item)
}
func (f *blockingMaintenance) AddMaintenanceLabels(ctx context.Context, repo string, number int, labels []string) ([]string, error) {
	if f.phase == "actions" {
		if err := f.wait(ctx); err != nil {
			return nil, err
		}
	}
	return f.maintenanceFactoryFake.AddMaintenanceLabels(ctx, repo, number, labels)
}

func (f *blockingMaintenance) MergeMaintenancePR(ctx context.Context, repo string, number int, head string, authorize func() (func(), error)) (maintenancePull, error) {
	if f.phase == "merge" || f.phase == "merge_hold" {
		if err := f.wait(ctx); err != nil {
			return maintenancePull{}, err
		}
	}
	if f.phase == "submitted" {
		return f.maintenanceFactoryFake.MergeMaintenancePR(ctx, repo, number, head, func() (func(), error) {
			release, err := authorize()
			if err != nil {
				return nil, err
			}
			if err = f.wait(ctx); err != nil {
				release()
				return nil, err
			}
			return release, nil
		})
	}
	return f.maintenanceFactoryFake.MergeMaintenancePR(ctx, repo, number, head, authorize)
}

func maintenanceDefinition(t *testing.T, s *GitHubFactoryServer) factoryAutomation {
	t.Helper()
	var a factoryAutomation
	json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/automations", map[string]any{"kind": "repository-maintenance", "name": "Maintain", "repository": "repo"}, 200), &a)
	return a
}
func startMaintenanceRequest(s *GitHubFactoryServer, a factoryAutomation, key string, stopped ...bool) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		input := map[string]any{"client_request_id": key}
		if len(stopped) > 0 {
			input["previous_process_stopped"] = stopped[0]
		}
		raw, _ := json.Marshal(input)
		request := httptest.NewRequest("POST", "/api/v1/automations/"+a.ID+"/run", strings.NewReader(string(raw)))
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		done <- response
	}()
	return done
}
func waitMaintenanceRead(t *testing.T, f *blockingMaintenance) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance read not started")
	}
}
func TestMaintenanceAdmissionRevalidatesAfterUnlockedFetch(t *testing.T) {
	f := &blockingMaintenance{maintenanceFactoryFake: maintenanceFixture(), phase: "snapshot", entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := githubFactoryTestServer(t, f)
	a := maintenanceDefinition(t, s)
	done := startMaintenanceRequest(s, a, "first")
	waitMaintenanceRead(t, f)
	// These endpoints must remain usable while the remote snapshot is blocked.
	statusDone := make(chan struct{})
	go func() { factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200); close(statusDone) }()
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		close(f.release)
		t.Fatal("GitHub snapshot blocked status")
	}
	a.Name = "Edited while fetching"
	factoryHTTP(t, s, "PUT", "/api/v1/automations/"+a.ID, a, 200)
	close(f.release)
	result := <-done
	if result.Code != 409 {
		t.Fatal("stale admission accepted", result.Code, result.Body.String())
	}
	current, err := s.local.automation(t.Context(), a.ID)
	if err != nil || current.Name != a.Name || current.Version != 2 || current.LastJob != "" {
		t.Fatal("concurrent definition overwritten", current, err)
	}
}
func TestMaintenanceConcurrentAdmissionRetainsSingleReceipt(t *testing.T) {
	f := &blockingMaintenance{maintenanceFactoryFake: maintenanceFixture(), phase: "snapshot", entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := githubFactoryTestServer(t, f)
	a := maintenanceDefinition(t, s)
	first := startMaintenanceRequest(s, a, "same")
	waitMaintenanceRead(t, f)
	second := startMaintenanceRequest(s, a, "same")
	waitMaintenanceRead(t, f)
	close(f.release)
	one, two := <-first, <-second
	if one.Code != 201 || two.Code != 201 || one.Body.String() != two.Body.String() {
		t.Fatal("duplicate receipt not reconciled", one.Code, two.Code, one.Body, two.Body)
	}
	var count int
	s.local.db.QueryRow(`SELECT count(*) FROM job_requests`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate job admitted", count)
	}
}
func TestMaintenanceReconciliationRechecksPrivateHoldAfterRead(t *testing.T) {
	f := &blockingMaintenance{maintenanceFactoryFake: maintenanceFixture(), phase: "reconcile", entered: make(chan struct{}, 2), release: make(chan struct{})}
	f.remoteMerged = true
	s := githubFactoryTestServer(t, f)
	id := factorySubmit(t, s)
	factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{})
	factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: maintenanceHead, Branch: "codex/source"})
	factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: maintenanceHead, Verdict: "accepted"})
	a := maintenanceDefinition(t, s)
	done := startMaintenanceRequest(s, a, "reconcile")
	waitMaintenanceRead(t, f)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]string{"feedback": "Keep private"}, 200)
	close(f.release)
	if response := <-done; response.Code != 201 {
		t.Fatal(response.Code, response.Body)
	}
	job := factoryJob(t, s, id)
	if job.State == "succeeded" || job.PublicationHold != "Keep private" {
		t.Fatal("stale reconciliation ignored hold")
	}
}

func TestMaintenanceStopConfirmationDoesNotCoverNewCancelledAttempt(t *testing.T) {
	f := &blockingMaintenance{maintenanceFactoryFake: maintenanceFixture(), phase: "snapshot", entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := githubFactoryTestServer(t, f)
	a := createAutomation(t, s, "")
	original := automationRun(t, s, a, "original", 201)
	first := factoryPoll(t, s)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+original+"/cancel", map[string]string{"run_id": first.ID}, 200)
	a.Kind = "repository-maintenance"
	json.Unmarshal(factoryHTTP(t, s, "PUT", "/api/v1/automations/"+a.ID, a, 200), &a)
	request := startMaintenanceRequest(s, a, "maintenance", true)
	waitMaintenanceRead(t, f)
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+original+"/retry", map[string]any{"run_id": first.ID, "previous_process_stopped": true}, 200)
	retry := factoryPoll(t, s)
	if retry == nil {
		t.Fatal("retry did not start")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+original+"/cancel", map[string]string{"run_id": retry.ID}, 200)
	close(f.release)
	if response := <-request; response.Code != 409 || !strings.Contains(response.Body.String(), "confirm the process stopped again") {
		t.Fatal("old stop confirmation reused", response.Code, response.Body)
	}
}

func TestMaintenanceCompletionKeepsControlsResponsiveAndRechecksCancellation(t *testing.T) {
	for _, phase := range []string{"actions", "merge", "merge_hold", "submitted"} {
		t.Run(phase, func(t *testing.T) {
			f := &blockingMaintenance{maintenanceFactoryFake: maintenanceFixture(), phase: phase, entered: make(chan struct{}, 2), release: make(chan struct{})}
			s := githubFactoryTestServer(t, f)
			source := factorySubmit(t, s)
			factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{})
			metadata := protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: maintenanceHead, Branch: "codex/source"}
			factoryFinish(t, s, factoryPoll(t, s), metadata)
			metadata.Verdict = "accepted"
			factoryFinish(t, s, factoryPoll(t, s), metadata)
			if _, reason := s.maintenanceTask("owner/repo", 2, maintenanceHead); reason != "" {
				t.Fatal(reason)
			}
			a := maintenanceDefinition(t, s)
			id := automationRun(t, s, a, "actions", 201)
			run := factoryPoll(t, s)
			report := `{"summary":"Triage","items":[{"kind":"pr","number":2,"assessment":"Needs review","labels":["bug"],"merge":true}]}`
			completion := factoryCompletion(run, protocol.FactoryResult{Resolved: true, Report: report})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				raw, _ := json.Marshal(completion)
				r := httptest.NewRequest("POST", "/api/v1/runs/"+run.ID+"/complete", strings.NewReader(string(raw)))
				r.Header.Set("Authorization", "Bearer secret")
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				done <- w
			}()
			waitMaintenanceRead(t, f)
			s.mu.Lock()
			s.loaded = time.Time{} // Force status to rebuild state during the remote action.
			s.mu.Unlock()
			controls := make(chan struct{})
			go func() {
				factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200)
				factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 409)
				if phase == "submitted" {
					for _, outcome := range []string{"blocked", "failed", "transport_failed"} {
						duplicate := completion
						if outcome == "transport_failed" {
							duplicate.State = "failed"
							duplicate.Error = "worker failed"
						} else {
							step := protocol.StepResult{Outcome: outcome, Summary: "Late duplicate", Factory: &protocol.FactoryResult{Version: 1, Role: "triage", Report: "untrusted report"}}
							duplicate.Result, _ = json.Marshal(map[string]any{"step_result": step})
						}
						factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", duplicate, 409)
					}
					factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": run.ID}, 409)
					factoryHTTP(t, s, "POST", "/api/v1/jobs/"+source+"/publication_hold", map[string]string{"feedback": "Hold publication"}, 409)
				} else if phase == "merge_hold" {
					factoryHTTP(t, s, "POST", "/api/v1/jobs/"+source+"/publication_hold", map[string]string{"feedback": "Keep private"}, 200)
				} else {
					factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": run.ID}, 200)
				}
				close(controls)
			}()
			select {
			case <-controls:
			case <-time.After(3 * time.Second):
				close(f.release)
				t.Fatal("remote maintenance action blocked status, duplicate completion or cancellation")
			}
			close(f.release)
			if phase == "submitted" {
				if response := <-done; response.Code != 204 {
					t.Fatal(response.Code, response.Body.String())
				}
				if factoryJob(t, s, id).State != "succeeded" || factoryJob(t, s, source).State != "succeeded" || f.merges != 1 {
					t.Fatal("authorized submission did not complete")
				}
				s.mu.Lock()
				guarded := s.tasks[id].mergeSubmitting || s.tasks[source].mergeSubmitting
				s.mu.Unlock()
				if guarded {
					t.Fatal("submission guard leaked")
				}
				return
			}
			if phase == "merge_hold" {
				if response := <-done; response.Code != 204 {
					t.Fatal(response.Code, response.Body.String())
				}
				if factoryJob(t, s, source).PublicationHold == "" || f.merges != 0 {
					t.Fatal("hold before submission bypassed")
				}
				return
			}
			if response := <-done; response.Code != 409 {
				t.Fatal(response.Code, response.Body.String())
			}
			if job := factoryJob(t, s, id); job.State != "cancelled" || factoryJob(t, s, source).State != "awaiting_merge" || f.merges != 0 || len(f.labels) != 1 {
				t.Fatal("stale completion or duplicate external actions", job.State, f.merges, len(f.labels))
			}

		})
	}
}
