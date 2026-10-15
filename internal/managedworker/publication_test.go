package managedworker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestCandidatePublicationResumesAfterPushAndAPIFailure(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote := filepath.Join(root, "remote.git")
	git(root, "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	git(root, "clone", remote, seed)
	os.WriteFile(filepath.Join(seed, "file"), []byte("base"), 0600)
	git(seed, "add", "file")
	git(seed, "commit", "-m", "base")
	git(seed, "push", "origin", "main")
	git(seed, "switch", "-c", "codex/task")
	git(seed, "push", "origin", "HEAD")
	base := git(seed, "rev-parse", "HEAD")
	registered := filepath.Join(root, "registered")
	git(root, "clone", remote, registered)
	workerDir := filepath.Join(root, "worker")
	workspace := filepath.Join(workerDir, "workspaces", "task")
	git(registered, "worktree", "add", workspace, "codex/task")
	git(registered, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	runDir := filepath.Join(workerDir, "runs", "first-attempt", "lease")
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	git(workspace, "bundle", "create", filepath.Join(runDir, "input.bundle"), "HEAD")
	os.WriteFile(filepath.Join(seed, "file"), []byte("candidate"), 0600)
	git(seed, "add", "file")
	git(seed, "commit", "-m", "candidate")
	head := git(seed, "rev-parse", "HEAD")
	bundle := filepath.Join(runDir, "candidate.bundle")
	git(seed, "bundle", "create", bundle, "HEAD")
	bin := filepath.Join(root, "bin")
	os.Mkdir(bin, 0700)
	failureMarker := filepath.Join(root, "patch-failed-once")
	// The real Git push succeeds; only GitHub's following metadata update fails once.
	gh := "#!/bin/sh\ncase \"$*\" in\n'api repos/owner/repo/pulls?'*) printf '[{\"html_url\":\"https://github.com/owner/repo/pull/12\"}]';;\n'api --method PATCH '*) if [ ! -f '" + failureMarker + "' ]; then touch '" + failureMarker + "'; exit 1; fi; printf '{}';;\n'pr ready '*) exit 0;;\n*) exit 7;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.WithValue(context.Background(), commandEnvironmentKey{}, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + remote + ".insteadOf", "GIT_CONFIG_VALUE_0=https://github.com/owner/repo.git"})
	allowHeartbeat := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/heartbeat") || !allowHeartbeat {
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	worker := &Worker{config: config.Worker{DataDirectory: workerDir, Kubernetes: &config.Kubernetes{}, Repositories: map[string]config.Repository{"repo": {Path: registered}}}, client: &Client{base: server.URL, http: server.Client()}, instanceID: "worker"}
	spec := protocol.RunSpec{ID: "first-attempt", JobID: "task", Repository: "repo", GitHubRepository: "owner/repo", LeaseToken: "lease", Factory: &protocol.FactoryContext{Role: "implement", Recipe: "factory", Branch: "codex/task", HeadSHA: base, PRURL: "https://github.com/owner/repo/pull/12"}}
	result := runner.Result{ID: spec.ID, EventsPath: filepath.Join(runDir, "events.jsonl"), State: runner.StateSucceeded, StepResult: &protocol.StepResult{Outcome: "complete", Summary: "Delivered candidate", Factory: &protocol.FactoryResult{Version: 1, Role: "implement", Report: "Verified change", Branch: "codex/task", HeadSHA: head}}}
	if err := worker.publishCandidate(ctx, workspace, bundle, spec, &result); err == nil {
		t.Fatal("expected post-push GitHub API failure")
	}
	if got := git(remote, "rev-parse", "refs/heads/codex/task"); got != head {
		t.Fatal("failure did not occur after push")
	}
	if got := git(workspace, "rev-parse", "HEAD"); got != base {
		t.Fatal("publisher changed trusted scratch")
	}
	if _, err := worker.factoryWorkspace(ctx, registered, spec); err == nil {
		t.Fatal("ordinary reconciliation should reject the now-stale assigned head")
	}
	// Simulate a restarted worker and a new lease. No configured executable or
	// cluster is available: recovery must finish publication without running an agent.
	restarted := *worker
	spec.ID, spec.LeaseToken = "retry-attempt", "retry-lease"
	completion := restarted.execute(ctx, spec)
	if completion.State != "succeeded" {
		t.Fatalf("publication did not recover: %s", completion.Error)
	}
	var recovered runner.Result
	if err := json.Unmarshal(completion.Result, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.ID != spec.ID || recovered.StepResult.Factory.HeadSHA != head || recovered.StepResult.Factory.PRURL != spec.Factory.PRURL {
		t.Fatal("retry lost assigned PR or candidate")
	}
	if repeated := restarted.execute(ctx, spec); repeated.State != "succeeded" {
		t.Fatalf("lost completion response cannot replay: %s", repeated.Error)
	}
	allowHeartbeat = false
	if rejected := restarted.execute(ctx, spec); rejected.State == "succeeded" {
		t.Fatal("publication accepted revoked lease")
	}
	allowHeartbeat = true
	feedback := *spec.Factory
	feedback.Context = "\nRequested changes or answer:\nNew requirements"
	changed := spec
	changed.Factory = &feedback
	if rejected := restarted.execute(ctx, changed); !strings.Contains(rejected.Error, "without new feedback") {
		t.Fatal("pending publication discarded new feedback")
	}
	advanced := *spec.Factory
	advanced.HeadSHA = head
	changed.Factory = &advanced
	if _, found := restarted.resumePublication(ctx, registered, changed); found {
		t.Fatal("old receipt applied to a new assigned head")
	}
	// An unrelated remote change must still be rejected, never overwritten.
	os.WriteFile(filepath.Join(seed, "file"), []byte("external change"), 0600)
	git(seed, "add", "file")
	git(seed, "commit", "-m", "external")
	git(seed, "push", "origin", "HEAD")
	if rejected := restarted.execute(ctx, spec); !strings.Contains(rejected.Error, "changed remotely") {
		t.Fatalf("external head accepted: %s", rejected.Error)
	}
}
