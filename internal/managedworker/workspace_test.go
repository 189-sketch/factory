package managedworker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestFactoryWorkspacePreservesTaskAndReviewSnapshot(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote := filepath.Join(root, "remote.git")
	if err := os.Mkdir(remote, 0700); err != nil {
		t.Fatal(err)
	}
	git(remote, "init", "--bare", "--initial-branch=trunk")
	seed := filepath.Join(root, "seed")
	git(root, "clone", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "file"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "file")
	git(seed, "commit", "-m", "initial")
	git(seed, "push", "origin", "trunk")
	repo := filepath.Join(root, "checkout")
	git(root, "clone", remote, repo)
	worker := &Worker{config: config.Worker{DataDirectory: filepath.Join(root, "worker")}}
	spec := protocol.RunSpec{ID: "run1", JobID: "task1", Factory: &protocol.FactoryContext{Role: "triage"}}
	path, err := worker.factoryWorkspace(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if branch := git(path, "branch", "--show-current"); branch != "codex/task1" {
		t.Fatal(branch)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("task edits"), 0600); err != nil {
		t.Fatal(err)
	}
	spec.Factory.Role = "implement"
	again, err := worker.factoryWorkspace(context.Background(), repo, spec)
	if err == nil {
		t.Fatalf("dirty scratch was reused: %s", again)
	}
	body, _ := os.ReadFile(filepath.Join(path, "file"))
	if string(body) != "task edits" {
		t.Fatal("task edits lost")
	}
	git(path, "push", "origin", "HEAD")
	git(repo, "config", "url."+remote+".insteadOf", "https://github.com/example/project.git")
	git(repo, "remote", "set-url", "origin", "https://github.com/example/project.git")
	sha := git(path, "rev-parse", "HEAD")
	spec.Factory = &protocol.FactoryContext{Role: "review", HeadSHA: sha, Branch: "codex/task1"}
	review, err := worker.factoryWorkspace(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(review, "file"))
	if string(body) != "initial" {
		t.Fatal("review included uncommitted edits")
	}
	if branch := git(repo, "branch", "--show-current"); branch != "trunk" {
		t.Fatal("registered checkout changed")
	}
	git(path, "add", "file")
	git(path, "commit", "-m", "new head")
	git(path, "push", "origin", "HEAD")
	spec.ID = "run2"
	if _, err := worker.factoryWorkspace(context.Background(), repo, spec); err == nil {
		t.Fatal("accepted stale review head")
	}
	spec.Factory.HeadSHA = git(path, "rev-parse", "HEAD")
	git(repo, "worktree", "remove", "--force", path)
	spec.Factory.Role = "implement"
	recovered, err := worker.factoryWorkspace(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	recoveredHead := git(recovered, "rev-parse", "HEAD")
	if recoveredHead == sha {
		t.Fatal("lost workspace did not recover pushed branch")
	}
	body, _ = os.ReadFile(filepath.Join(recovered, "file"))
	if string(body) != "task edits" {
		t.Fatal("pushed work was not reconstructed")
	}
	// Host B advances the same GitHub branch while host A retains stale scratch.
	hostB := filepath.Join(root, "host-b")
	git(root, "clone", remote, hostB)
	git(hostB, "switch", "codex/task1")
	if err := os.WriteFile(filepath.Join(hostB, "other"), []byte("implemented on host B"), 0600); err != nil {
		t.Fatal(err)
	}
	git(hostB, "add", "other")
	git(hostB, "commit", "-m", "implementation from host B")
	git(hostB, "push", "origin", "HEAD")
	spec.Factory.HeadSHA = git(hostB, "rev-parse", "HEAD")
	synced, err := worker.factoryWorkspace(context.Background(), repo, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(synced, "rev-parse", "HEAD"); got != spec.Factory.HeadSHA {
		t.Fatalf("stale host A head %s", got)
	}
	if content, err := os.ReadFile(filepath.Join(synced, "other")); err != nil || string(content) != "implemented on host B" {
		t.Fatal("host B implementation missing")
	}
	// Unpublished host A commits cannot be discarded or silently resumed.
	if err := os.WriteFile(filepath.Join(synced, "local"), []byte("unpublished"), 0600); err != nil {
		t.Fatal(err)
	}
	git(synced, "add", "local")
	git(synced, "commit", "-m", "local scratch only")
	localHead := git(synced, "rev-parse", "HEAD")
	if _, err := worker.factoryWorkspace(context.Background(), repo, spec); err == nil {
		t.Fatal("accepted unpublished host A commit")
	}
	if got := git(synced, "rev-parse", "HEAD"); got != localHead {
		t.Fatal("unpublished commit discarded")
	}
	spec.JobID = "../escape"
	if _, err := worker.factoryWorkspace(context.Background(), repo, spec); err == nil {
		t.Fatal("accepted traversal")
	}
}
