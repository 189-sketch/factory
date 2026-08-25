package managedworker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/factory-v2/internal/config"
	"github.com/owainlewis/factory-v2/internal/controlplane"
	"github.com/owainlewis/factory-v2/internal/protocol"
)

func TestManagedWorkerExecutesControlPlaneRun(t *testing.T) {
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository")
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	definitionPath := filepath.Join(directory, "factory.toml")
	promptPath := filepath.Join(directory, "plan.md")
	if err := os.WriteFile(promptPath, []byte("{{factory.prompt}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definitionPath, []byte("[agents.plan]\nexecutor=\"test\"\nprompt_file=\"plan.md\"\ntimeout=\"5s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := controlplane.OpenStore(filepath.Join(directory, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agent, err := config.LoadAgent(definitionPath, "plan")
	if err != nil {
		t.Fatal(err)
	}
	agent, err = config.RenderPrompt(agent, "managed request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), "managed request", "factory", "agent", "plan", []config.ResolvedAgent{agent}); err != nil {
		t.Fatal(err)
	}
	server, err := controlplane.NewServer(store, definitionPath, "secret")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := New(config.Worker{
		Name:          "local-test",
		DataDirectory: filepath.Join(directory, "worker-data"),
		ControlPlane:  config.ControlPlane{URL: httpServer.URL, TokenFile: tokenPath},
		Executors:     map[string]config.Executor{"test": {Command: []string{"/bin/sh", "-c", "cat >/dev/null; printf managed-output"}}},
		Repositories:  map[string]config.Repository{"factory": {Path: repository}},
	}, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := store.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Jobs) == 1 && snapshot.Jobs[0].State == "succeeded" {
			run := snapshot.Jobs[0].Runs[0]
			output, outputErr := store.RunOutput(t.Context(), run.ID)
			if run.State != "succeeded" || outputErr != nil || output.Events == "" || output.Result == "" || run.WorkerName != "local-test" {
				t.Fatalf("completed run = %#v", run)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete: %#v", snapshot.Jobs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestManagedWorkerReportsPreflightFailureWithoutStartingRun(t *testing.T) {
	directory := t.TempDir()
	worker := &Worker{
		config: config.Worker{
			DataDirectory: directory,
			Executors:     map[string]config.Executor{},
			Repositories:  map[string]config.Repository{},
		},
		instanceID: "worker-test",
	}
	completion := worker.execute(t.Context(), protocol.RunSpec{ID: "run_0123456789abcdef01234567", Executor: "missing", Repository: "missing", LeaseToken: "lease"})
	if completion.State != "failed" || completion.Error == "" || completion.Result != nil || completion.Events != "" {
		t.Fatalf("completion = %#v", completion)
	}
	if _, err := os.Stat(filepath.Join(directory, "runs")); !os.IsNotExist(err) {
		t.Fatalf("runner started during preflight: %v", err)
	}
}

func TestManagedWorkerRequiresHTTPSForRemoteControlPlane(t *testing.T) {
	_, err := New(config.Worker{
		Name:         "remote",
		ControlPlane: config.ControlPlane{URL: "http://10.0.0.5:7331", TokenFile: "unused"},
		Executors:    map[string]config.Executor{"test": {Command: []string{"agent"}}},
		Repositories: map[string]config.Repository{"factory": {Path: "."}},
	}, os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompletionDoesNotRetryPermanentClientError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(response, "too large", http.StatusRequestEntityTooLarge)
	}))
	defer server.Close()
	worker := &Worker{
		config:     config.Worker{ControlPlane: config.ControlPlane{URL: server.URL}},
		token:      "secret",
		instanceID: "worker-test",
		client:     server.Client(),
		stderr:     io.Discard,
	}
	err := worker.deliver(t.Context(), "run_0123456789abcdef01234567", protocol.Completion{})
	if err == nil || requests.Load() != 1 {
		t.Fatalf("error = %v, requests = %d", err, requests.Load())
	}
}
