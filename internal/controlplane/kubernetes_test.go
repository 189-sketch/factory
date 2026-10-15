package controlplane

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestKubernetesHealthCheckRejectsHostRuntime(t *testing.T) {
	_, o := setupTestServer(t)
	o.health = &setupHealth{runtime: "kubernetes", spec: protocol.RunSpec{ID: "health", Repository: "repo", Executor: "codex-review"}}
	p := protocol.PollRequest{Runtime: "host", HealthChecks: true, InstanceID: "host", Repositories: []string{"repo"}, ReadOnlyExecutors: []string{"codex-review"}}
	if o.claimHealth(p) != nil {
		t.Fatal("host claimed cluster verification")
	}
	p.Runtime = "kubernetes"
	p.InstanceID = "cluster"
	if o.claimHealth(p) == nil {
		t.Fatal("cluster verification not routed")
	}
}

func TestKubernetesTaskCannotRunOnHostFallback(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	id := submitRecipe(t, s, "single", "target-routing")
	s.setup = &onboarding{server: s, settings: setupSettings{Finished: true, Selection: setupSelection{Host: "kubernetes", Agent: "codex"}}}
	p := protocol.PollRequest{Runtime: "host", InstanceID: "host", Name: "host", SingleAgent: true, Factory: true, Workflows: true, Reviews: true, Executors: []string{"codex", "codex-review"}, ReadOnlyExecutors: []string{"codex-review"}, Repositories: []string{"repo"}}
	var response protocol.PollResponse
	if err := json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200), &response); err != nil {
		t.Fatal(err)
	}
	if response.Run != nil || s.tasks[id].state.Job.State != "queued" {
		t.Fatal("host silently ran a Kubernetes task")
	}
	p.Runtime = "kubernetes"
	p.InstanceID = "cluster"
	p.Name = "machinist-kubernetes"
	if err := json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200), &response); err != nil {
		t.Fatal(err)
	}
	if response.Run == nil || response.Run.JobID != id {
		t.Fatal("Kubernetes task was not dispatched")
	}
}

func TestKubernetesConnectorResumesAfterRestart(t *testing.T) {
	s, o := setupTestServer(t)
	f, err := os.OpenFile(s.definitionPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[server]\nstate='factory'\nworker_token_file='" + s.definitionPath + ".token'\n[server.kubernetes]\ncontext='test'\nnamespace='runners'\nauth_secret='codex'\nproxy_url='http://proxy:3128'\nimage='example.test/runner@sha256:" + strings.Repeat("a", 64) + "'\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	o.settings.Selection = setupSelection{Repository: "owner/repo", Agent: "codex", Host: "kubernetes", Path: t.TempDir()}
	o.settings.Finished = true
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.SetLocalWorkerFactory(func(cfg config.Worker) (LocalWorker, error) {
		if cfg.Kubernetes == nil || cfg.Name != "machinist-kubernetes" {
			t.Error("resumed wrong runtime")
		}
		return &setupLocalWorker{started}, nil
	})
	o.listening(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7331})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("Kubernetes connector did not resume: %+v", o.snapshot().Check)
	}
	o.stopWorker()
}

func TestKubernetesShutdownWaitsForCredentialCleanup(t *testing.T) {
	o := &onboarding{settings: setupSettings{Selection: setupSelection{Host: "kubernetes"}}}
	_, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.done = make(chan struct{})
	done := o.done
	returned := make(chan struct{})
	go func() { o.stopWorker(); close(returned) }()
	select {
	case <-returned:
		t.Fatal("shutdown abandoned cluster cleanup")
	case <-time.After(5100 * time.Millisecond):
	}
	close(done)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after cleanup")
	}
}

func TestClearInboxPersistsWithoutDeletingTaskAndResurfacesOnChange(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := submitRecipe(t, s, "single", "inbox-clear")
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Branch: "codex/task"})
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/clear_inbox", map[string]any{}, 200)
	s = restartFactoryTestServer(t, s, client)
	job := factoryJob(t, s, id)
	if !job.InboxCleared || job.State != "awaiting_merge" || job.Workflow.Factory.PRURL == "" {
		t.Fatal("clearing changed or lost work")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/request_changes", map[string]any{"run_id": run.ID, "feedback": "Fix one detail"}, 200)
	if factoryJob(t, s, id).InboxCleared {
		t.Fatal("new activity stayed hidden")
	}
	if client.creates != 0 || len(client.comments) != 0 {
		t.Fatal("clearing inbox wrote to GitHub")
	}
}
