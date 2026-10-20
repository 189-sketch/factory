package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeEventsReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, e := openFactoryLocalStore(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	first := json.RawMessage(`{"sequence":1,"type":"agent.activity","time":"2026-10-20T12:00:00Z"}`)
	for range 2 {
		if c, e := s.ingestRunEvents(ctx, "run", []json.RawMessage{first}); e != nil || c != 1 {
			t.Fatalf("%d %v", c, e)
		}
	}
	for _, raw := range []string{`{"sequence":1,"type":"different"}`, `{"sequence":3,"type":"gap"}`} {
		if _, e := s.ingestRunEvents(ctx, "run", []json.RawMessage{json.RawMessage(raw)}); e == nil {
			t.Fatal("accepted conflict/gap")
		}
	}
	s.db.Close()
	s, e = openFactoryLocalStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	server := &GitHubFactoryServer{local: s}
	r := httptest.NewRequest("GET", "/?after=0", nil)
	r.SetPathValue("id", "run")
	w := httptest.NewRecorder()
	server.runEvents(w, r)
	var page protocol.RunEventPage
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Cursor != 1 || len(page.Events) != 1 {
		t.Fatalf("bad page %s", w.Body.String())
	}
}
func TestNativeEventsLease(t *testing.T) {
	s, e := openFactoryLocalStore(filepath.Join(t.TempDir(), "events.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	server := &GitHubFactoryServer{local: s, leases: map[string]*githubLease{"run": {instance: "worker", spec: protocol.RunSpec{LeaseToken: "token"}, last: time.Now().Add(-2 * leaseDuration)}}}
	body := []byte(`{"instance_id":"worker","lease_token":"token","events":[{"sequence":1,"type":"agent.activity"}]}`)
	for _, status := range []int{409, 200} {
		r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		r.SetPathValue("id", "run")
		w := httptest.NewRecorder()
		server.ingestEvents(w, r)
		if w.Code != status {
			t.Fatalf("got %d want %d: %s", w.Code, status, w.Body.String())
		}
		server.leases["run"].last = time.Now()
	}
}

func TestNativeFactoryPrimaryLifecycleAndReplay(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	file, e := os.OpenFile(s.definitionPath, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = file.WriteString("\n[commands.primary]\nexecutor='codex'\ntimeout='1m'\n")
	file.Close()
	if e != nil {
		t.Fatal(e)
	}
	raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": "native-v1", "spec": "Answer from native agents"}, 201)
	var job map[string]string
	_ = json.Unmarshal(raw, &job)
	if run := factoryPoll(t, s); run != nil {
		t.Fatal("legacy worker claimed native run")
	}
	poll := protocol.PollRequest{NativeAgents: true, Adapters: map[string]string{"codex": "codex-app-server"}, Runtime: "host", Factory: true, Workflows: true, Reviews: true, Executors: []string{"codex"}, Repositories: []string{"repo"}, InstanceID: "worker-instance", Name: "worker"}
	raw = factoryHTTP(t, s, "POST", "/api/v1/workers/poll", poll, 200)
	var response protocol.PollResponse
	_ = json.Unmarshal(raw, &response)
	run := response.Run
	if run == nil || run.Factory.Role != "primary" || run.Factory.Recipe != "native-v1" {
		t.Fatalf("bad native assignment %s", raw)
	}
	batch := protocol.RunEventBatch{InstanceID: "worker-instance", LeaseToken: run.LeaseToken, Events: []json.RawMessage{json.RawMessage(`{"sequence":1,"type":"agent.activity","message":"observed"}`)}}
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/events", batch, 200)
	completion := factoryCompletion(run, protocol.FactoryResult{Report: "Native answer"})
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	s = restartFactoryTestServer(t, s, client)
	current := factoryJob(t, s, job["id"])
	if current.State != "succeeded" || len(current.Runs) != 1 || len(current.Workflow.Steps) != 1 || current.Workflow.Steps[0] != "primary" {
		t.Fatalf("native replay: %+v", current)
	}
	factoryHTTP(t, s, "GET", "/api/v1/runs/"+run.ID+"/events?after=0", nil, 200)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/events", batch, 409)
}

func TestNativeFeedbackRetainsPrimaryBeyondLegacyRepairLimit(t *testing.T) {
	for _, kind := range []string{"request_changes", "github_followup"} {
		t.Run(kind, func(t *testing.T) {
			j := &journalFixture{t: t, issue: GitHubIssue{HTMLURL: "https://github.com/owner/repo/issues/1", Title: "Native", Body: "Deliver it"}}
			j.add(GitHubFactoryEvent{Kind: "created", Role: "primary", Recipe: "native-v1"})
			for i := 0; i < 5; i++ {
				j.finish(protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: fmt.Sprintf("%040x", i+1), Branch: "codex/native"})
				if j.state.Job.State != "awaiting_merge" {
					t.Fatal(j.state.Job.State)
				}
				j.add(GitHubFactoryEvent{Kind: kind, Feedback: "Revise the existing PR", FollowupIDs: []string{fmt.Sprintf("signal-%d", i)}})
				if j.state.Role != "primary" || j.state.Job.State != "queued" || j.state.Job.Workflow.Factory.RepairRound != 0 {
					t.Fatalf("native feedback entered legacy repair routing: %+v", j.state)
				}
			}
			for _, run := range j.state.Job.Runs {
				if run.Command != "primary" || strings.Contains(run.ID, "implement") {
					t.Fatal("synthetic implementation stage")
				}
			}
		})
	}
}
