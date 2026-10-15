package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func setupTestServer(t *testing.T, repositoryNames ...string) (*GitHubFactoryServer, *onboarding) {
	if len(repositoryNames) == 0 {
		repositoryNames = []string{"owner/repo"}
	}
	t.Helper()
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	o, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	s.setup = o
	o.connection = testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("setup performed GitHub write: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/user/installations":
			w.Write([]byte(`{"installations":[{"id":1,"permissions":{"contents":"write","issues":"write","pull_requests":"write","checks":"read","statuses":"read"}}]}`))
		case "/user/installations/1/repositories":
			repos := []map[string]any{}
			for i, name := range repositoryNames {
				repos = append(repos, map[string]any{"id": i + 1, "full_name": name, "permissions": map[string]bool{"push": true}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
		case "/repos/owner/repo":
			w.Write([]byte(`{"permissions":{"push":true}}`))
		default:
			t.Errorf("unexpected setup path %s", r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	})
	o.connection.credentials = githubCredentials{AccessToken: "private-test-token", Account: "owner"}
	o.baseURL = "http://127.0.0.1:7331"
	return s, o
}
func TestOnboardingRequiresVerificationAndDoesNotExposeTokens(t *testing.T) {
	s, o := setupTestServer(t)
	raw := factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200)
	if !strings.Contains(string(raw), `"required":true`) || strings.Contains(string(raw), "private-test-token") || strings.Contains(string(raw), "secret-refresh") {
		t.Fatalf("unsafe status: %s", raw)
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"workflow": "factory", "repository": "repo", "spec": "PING"}, 409)
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/finish", map[string]any{}, 400)
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: t.TempDir(), Agent: "codex", Host: "remote"}
	o.runCheck = func(context.Context, setupSelection) setupCheck {
		return setupCheck{"failed", "Sign in to the agent CLI"}
	}
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/check", map[string]any{}, 200)
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/finish", map[string]any{}, 400)
	if o.snapshot().Finished {
		t.Fatal("failed check completed setup")
	}
}

type setupLocalWorker struct{ started chan struct{} }

func (w *setupLocalWorker) SetGitHubTokenProvider(func(context.Context, string) (string, error)) {}
func (w *setupLocalWorker) Run(ctx context.Context) error                                        { close(w.started); <-ctx.Done(); return nil }
func TestOnboardingFinishesAndRestoresSelection(t *testing.T) {
	s, o := setupTestServer(t)
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: t.TempDir(), Agent: "codex", Host: "local"}
	o.runCheck = func(context.Context, setupSelection) setupCheck { return setupCheck{"passed", "PONG"} }
	started := make(chan struct{})
	s.SetLocalWorkerFactory(func(cfg config.Worker) (LocalWorker, error) {
		if cfg.ControlPlane.URL != "http://127.0.0.1:7331" || cfg.Repositories["owner/repo"].Path != o.snapshot().Selection.Path {
			t.Fatal("worker not bound to setup")
		}
		return &setupLocalWorker{started}, nil
	})
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/check", map[string]any{}, 200)
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/finish", map[string]any{}, 200)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("local worker did not start")
	}
	o.stopWorker()
	reloaded, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.snapshot().Finished || reloaded.snapshot().Selection != o.snapshot().Selection {
		t.Fatal("setup was not saved")
	}
}
func TestVMOnboardingRequiresActualWorkerPong(t *testing.T) {
	s, o := setupTestServer(t)
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: t.TempDir(), Agent: "codex", Host: "remote"}
	poll := protocol.PollRequest{InstanceID: "vm-instance", Name: "vm", HealthChecks: true, Repositories: []string{"owner/repo"}, Executors: []string{"codex", "codex-review"}, ReadOnlyExecutors: []string{"codex-review"}, Factory: true, Workflows: true, Reviews: true}
	factoryHTTP(t, s, "POST", "/api/v1/workers/poll", poll, 200)
	response := make(chan []byte, 1)
	go func() {
		req := httptest.NewRequest("POST", "/api/v1/onboarding/check", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer secret")
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, req)
		response <- out.Body.Bytes()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		o.mu.Lock()
		pending := o.health != nil
		o.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("VM check was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	var assigned protocol.PollResponse
	raw := factoryHTTP(t, s, "POST", "/api/v1/workers/poll", poll, 200)
	if err := json.Unmarshal(raw, &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Run == nil || !assigned.Run.HealthCheck || assigned.Run.Executor != "codex-review" {
		t.Fatalf("invalid VM check: %s", raw)
	}
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+assigned.Run.ID+"/complete", protocol.Completion{InstanceID: poll.InstanceID, LeaseToken: assigned.Run.LeaseToken, State: "succeeded", Result: json.RawMessage(`{"final_message":"PONG"}`)}, 204)
	select {
	case raw := <-response:
		if !bytes.Contains(raw, []byte(`"state":"passed"`)) {
			t.Fatalf("check: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("VM check did not finish")
	}
	if len(s.tasks) != 0 {
		t.Fatal("connection check created a task")
	}
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/finish", map[string]any{}, 200)
}
func TestWorkerSetupDownloadIsPrivateAndNoGitHubCredential(t *testing.T) {
	s, o := setupTestServer(t)
	o.settings.Selection = setupSelection{Repository: "owner/repo", Agent: "codex", Host: "remote"}
	raw := factoryHTTP(t, s, "GET", "/api/v1/onboarding/worker-config", nil, 200)
	if bytes.Contains(raw, []byte("private-test-token")) || !bytes.Contains(raw, []byte("gh auth status")) || !bytes.Contains(raw, []byte("umask 077")) {
		t.Fatalf("invalid worker setup")
	}
	req := httptest.NewRequest("GET", "/api/v1/onboarding/worker-config", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatal("cross-site worker secret download allowed")
	}
}
func TestCheckoutRepositoryIdentity(t *testing.T) {
	for _, remote := range []string{"https://github.com/owner/repo.git", "git@github.com:owner/repo.git", "ssh://git@github.com/owner/repo"} {
		if !sameGitHubRepository(remote, "owner/repo") {
			t.Fatal(remote)
		}
	}
	for _, remote := range []string{"https://evil.example/owner/repo", "https://github.com/other/repo", "https://github.com/owner/repo/extra", "https://token@github.com/owner/repo"} {
		if sameGitHubRepository(remote, "owner/repo") {
			t.Fatal(remote)
		}
	}
}

type failingSetupWorker struct{}

func (*failingSetupWorker) SetGitHubTokenProvider(func(context.Context, string) (string, error)) {}
func (*failingSetupWorker) Run(context.Context) error {
	return errors.New("worker connection rejected")
}
func TestLocalWorkerExitInvalidatesReadinessAndCanRestart(t *testing.T) {
	s, o := setupTestServer(t)
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: t.TempDir(), Agent: "codex", Host: "local"}
	o.settings.Finished = true
	o.settings.Check = setupCheck{"passed", "PONG"}
	o.mu.Lock()
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	o.mu.Unlock()
	calls := 0
	s.SetLocalWorkerFactory(func(config.Worker) (LocalWorker, error) { calls++; return &failingSetupWorker{}, nil })
	for attempt := 0; attempt < 2; attempt++ {
		if err := o.startWorker(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for {
			o.mu.Lock()
			stopped := o.done == nil
			ready := o.settings.Finished
			o.mu.Unlock()
			if stopped {
				if ready {
					t.Fatal("exited worker left setup ready")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("worker exit not reconciled")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if calls != 2 {
		t.Fatalf("worker could not restart: calls=%d", calls)
	}
}
func TestFinishRejectsConcurrentSelectionChange(t *testing.T) {
	s, o := setupTestServer(t)
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: t.TempDir(), Agent: "codex", Host: "local"}
	o.settings.Check = setupCheck{"passed", "PONG"}
	started := make(chan struct{})
	s.SetLocalWorkerFactory(func(config.Worker) (LocalWorker, error) {
		o.mu.Lock()
		o.settings.Selection.Agent = "claude"
		o.settings.Check = setupCheck{}
		o.mu.Unlock()
		return &setupLocalWorker{started}, nil
	})
	o.mu.Lock()
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	o.mu.Unlock()
	factoryHTTP(t, s, "POST", "/api/v1/onboarding/finish", map[string]any{}, 400)
	if o.snapshot().Finished {
		t.Fatal("unverified selection completed setup")
	}
}
