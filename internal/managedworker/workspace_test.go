package managedworker

import (
	"context"
	"fmt"
	"io"
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
	// Early command-resolution failure still removes its disposable review snapshot.
	worker.config.Repositories = map[string]config.Repository{"repo": {Path: repo}}
	worker.config.Executors = map[string]config.Executor{"codex-review": {ReadOnly: true}}
	worker.stderr = io.Discard
	reviewSpec := protocol.RunSpec{ID: "cleanup-review", JobID: "task1", Repository: "repo", Executor: "codex-review", Factory: &protocol.FactoryContext{Role: "review", HeadSHA: spec.Factory.HeadSHA, Branch: "codex/task1"}}
	if completion := worker.execute(context.Background(), reviewSpec); completion.State != "failed" {
		t.Fatal("invalid executor was accepted")
	}
	if _, err := os.Stat(filepath.Join(worker.config.DataDirectory, "workspaces", "task1-review-cleanup-review")); !os.IsNotExist(err) {
		t.Fatal("disposable review checkout leaked after failure")
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

func TestNativeRetryRecoversOnlyVerifiedPublishedBranch(t *testing.T) {
	sha := strings.Repeat("a", 40)
	list := `[{"html_url":"https://github.com/owner/project/pull/12","head":{"ref":"docs/agent-branch","sha":"` + sha + `"}}]`
	pr := `{"state":"open","head":{"ref":"docs/agent-branch","sha":"` + sha + `","repo":{"full_name":"owner/project"}},"base":{"repo":{"full_name":"owner/project"}}}`
	base := map[string]string{
		"git status --porcelain": "", "git symbolic-ref --short HEAD": "docs/agent-branch",
		"git ls-remote --heads origin refs/heads/codex/task": "",
		"git remote": "origin", "git config --get remote.origin.url": "https://github.com/owner/project.git",
		"gh api repos/owner/project/pulls?state=open&head=owner%3Adocs%2Fagent-branch&per_page=100": list,
		"git rev-parse HEAD": sha,
		"git ls-remote --heads origin refs/heads/docs/agent-branch": sha + " refs/heads/docs/agent-branch",
		"gh api repos/owner/project/pulls/12":                       pr,
		"git fetch origin refs/heads/docs/agent-branch":             "",
		"git rev-parse FETCH_HEAD":                                  sha,
	}
	for _, test := range []struct {
		name, key, value string
		fail             bool
	}{
		{name: "published native branch"},
		{"uncommitted changes", "git status --porcelain", " M file", true},
		{"assigned branch also published", "git ls-remote --heads origin refs/heads/codex/task", sha + " refs/heads/codex/task", true},
		{"no PR", "gh api repos/owner/project/pulls?state=open&head=owner%3Adocs%2Fagent-branch&per_page=100", "[]", true},
		{"multiple PRs", "gh api repos/owner/project/pulls?state=open&head=owner%3Adocs%2Fagent-branch&per_page=100", "[" + strings.Trim(list, "[]") + "," + strings.Trim(list, "[]") + "]", true},
		{"unpublished local commits", "git rev-parse HEAD", strings.Repeat("b", 40), true},
		{"PR closed", "gh api repos/owner/project/pulls/12", strings.Replace(pr, `"open"`, `"closed"`, 1), true},
		{"PR in another repository", "gh api repos/owner/project/pulls/12", strings.ReplaceAll(pr, "owner/project", "other/project"), true},
		{"remote moved", "git ls-remote --heads origin refs/heads/docs/agent-branch", strings.Repeat("b", 40) + " refs/heads/docs/agent-branch", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := &protocol.FactoryContext{Recipe: "native-v1", Role: "primary", Branch: "codex/task"}
			command := func(program string, args ...string) ([]byte, error) {
				key := program + " " + strings.Join(args, " ")
				value, ok := base[key]
				if key == test.key {
					value = test.value
				}
				if !ok {
					return nil, fmt.Errorf("unexpected command, must not discard scratch: %s", key)
				}
				return []byte(value), nil
			}
			err := reconcileFactoryWorkspace(command, "origin", factory, "task")
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
			if !test.fail && (factory.Branch != "docs/agent-branch" || factory.PRURL != "https://github.com/owner/project/pull/12" || factory.HeadSHA != sha) {
				t.Fatalf("recovery context lost: %+v", factory)
			}
			if test.fail && factory.PRURL != "" {
				t.Fatalf("unverified recovery persisted: %+v", factory)
			}
		})
	}
}
