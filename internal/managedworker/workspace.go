package managedworker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// factoryWorkspace keeps a task's edits separate from the registered checkout.
func (w *Worker) factoryWorkspace(ctx context.Context, repository string, spec protocol.RunSpec) (string, error) {
	if spec.JobID == "" || filepath.Base(spec.JobID) != spec.JobID || spec.JobID == "." || spec.JobID == ".." {
		return "", fmt.Errorf("invalid factory task ID")
	}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	remote := "origin"
	remotes, err := git("remote")
	if err != nil {
		return "", err
	}
	names := strings.Fields(remotes)
	found := false
	for _, name := range names {
		if name == remote {
			found = true
		}
	}
	if !found {
		if len(names) != 1 {
			return "", fmt.Errorf("factory repository requires origin or one unambiguous remote")
		}
		remote = names[0]
	}
	root := filepath.Join(w.config.DataDirectory, "workspaces")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(root, spec.JobID)
	if spec.Factory.Role == "review" {
		if _, _, err := factoryRemote(repositoryCommand(ctx, repository)); err != nil {
			return "", err
		}
		if err := verifyRemoteHead(repositoryCommand(ctx, repository), remote, spec.Factory.Branch, spec.Factory.HeadSHA); err != nil {
			return "", err
		}
		if spec.Factory.HeadSHA == "" {
			return "", fmt.Errorf("factory review requires a head commit")
		}
		if spec.ID == "" || filepath.Base(spec.ID) != spec.ID || spec.ID == "." || spec.ID == ".." {
			return "", fmt.Errorf("invalid factory run ID")
		}
		path = filepath.Join(root, spec.JobID+"-review-"+spec.ID)
		sha, err := git("rev-parse", "--verify", spec.Factory.HeadSHA+"^{commit}")
		if err != nil && spec.Factory.Branch != "" {
			if _, fetchErr := git("fetch", remote, "refs/heads/"+spec.Factory.Branch); fetchErr != nil {
				return "", fetchErr
			}
			sha, err = git("rev-parse", "--verify", spec.Factory.HeadSHA+"^{commit}")
		}
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("review workspace already exists")
		}
		_, err = git("worktree", "add", "--detach", path, sha)
		return path, err
	}
	if _, err := os.Stat(path); err == nil {
		cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
		out, err := cmd.Output()
		expected, resolveErr := filepath.EvalSymlinks(path)
		if err != nil || resolveErr != nil || strings.TrimSpace(string(out)) != expected {
			return "", fmt.Errorf("factory workspace is not the expected worktree")
		}
		// A preexisting directory must belong to the registered repository.
		common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return "", err
		}
		actualCmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		actual, err := actualCmd.Output()
		expectedCommon, expectedErr := filepath.EvalSymlinks(common)
		actualCommon, actualErr := filepath.EvalSymlinks(strings.TrimSpace(string(actual)))
		if err != nil || expectedErr != nil || actualErr != nil || expectedCommon != actualCommon {
			return "", fmt.Errorf("factory workspace belongs to another repository")
		}
		if err := reconcileFactoryWorkspace(repositoryCommand(ctx, path), remote, spec.Factory, spec.JobID); err != nil {
			return "", err
		}
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if _, err := git("fetch", remote); err != nil {
		return "", err
	}
	base, err := git("symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", fmt.Errorf("repository must have a remote default branch: %w", err)
	}
	branch := spec.Factory.Branch
	if branch == "" {
		branch = "codex/" + spec.JobID
	}
	if _, err := git("check-ref-format", "--branch", branch); err != nil {
		return "", err
	}
	remoteHead, err := git("ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remoteHead) != "" {
		if spec.Factory.HeadSHA != "" {
			if err := verifyRemoteHead(repositoryCommand(ctx, repository), remote, branch, spec.Factory.HeadSHA); err != nil {
				return "", err
			}
		}
		if _, err := git("fetch", remote, "refs/heads/"+branch); err != nil {
			return "", err
		}
		base = "FETCH_HEAD"
		if _, err := git("show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			// GitHub owns recovery: refuse to silently reuse divergent local work.
			local, err := git("rev-parse", "refs/heads/"+branch)
			if err != nil {
				return "", err
			}
			fetched, err := git("rev-parse", "FETCH_HEAD")
			if err != nil {
				return "", err
			}
			if local != fetched {
				return "", fmt.Errorf("local task branch differs from GitHub; preserve or discard local scratch before recovery")
			}
			_, err = git("worktree", "add", path, branch)
			return path, err
		}
	} else if spec.Factory.PRURL != "" {
		return "", fmt.Errorf("recorded task PR branch is missing on GitHub")
	} else if local, localErr := git("rev-parse", "--verify", "refs/heads/"+branch); localErr == nil {
		initial, err := git("rev-parse", base)
		if err != nil {
			return "", err
		}
		if local != initial {
			return "", fmt.Errorf("unpublished local task commits cannot reconstruct GitHub state; preserve or publish scratch work before recovery")
		}
		_, err = git("worktree", "add", path, branch)
		return path, err
	}
	_, err = git("worktree", "add", "-b", branch, path, base)
	return path, err
}

// Reuse only clean scratch, and fast-forward it to the GitHub-owned task head.
// Never reset or discard unpublished work to repair a stale worker checkout.
func reconcileFactoryWorkspace(command evidenceCommand, remote string, factory *protocol.FactoryContext, jobID string) error {
	run := func(args ...string) (string, error) {
		out, err := command("git", args...)
		return strings.TrimSpace(string(out)), err
	}
	status, err := run("status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("task scratch has uncommitted changes; preserve or discard them before resuming from GitHub")
	}
	branch := factory.Branch
	if branch == "" {
		branch = "codex/" + jobID
	}
	current, err := run("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if current != branch {
		return fmt.Errorf("task scratch branch differs from assigned GitHub branch")
	}
	refs, err := run("ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	fields := strings.Fields(refs)
	if len(fields) == 0 {
		if factory.HeadSHA != "" || factory.PRURL != "" {
			return fmt.Errorf("assigned task branch is missing on GitHub")
		}
		if _, err := run("fetch", remote); err != nil {
			return err
		}
		base, err := run("symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
		if err != nil {
			return err
		}
		expected, err := run("rev-parse", base)
		if err != nil {
			return err
		}
		local, err := run("rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if expected != local {
			if _, err := run("merge-base", "--is-ancestor", local, expected); err != nil {
				return fmt.Errorf("task scratch contains unpublished commits; preserve or publish them before resuming")
			}
			_, err = run("merge", "--ff-only", expected)
			return err
		}
		return nil
	}
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
		return fmt.Errorf("unexpected remote task branch evidence")
	}
	expected := fields[0]
	if factory.HeadSHA != "" && expected != factory.HeadSHA {
		return fmt.Errorf("GitHub task head changed since dispatch; refresh task context")
	}
	if _, err := run("fetch", remote, "refs/heads/"+branch); err != nil {
		return err
	}
	fetched, err := run("rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if fetched != expected {
		return fmt.Errorf("GitHub task head changed during workspace reconciliation")
	}
	local, err := run("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if local == expected {
		return nil
	}
	if _, err := run("merge-base", "--is-ancestor", local, expected); err != nil {
		return fmt.Errorf("task scratch has divergent or unpublished commits; preserve them before resuming from GitHub")
	}
	_, err = run("merge", "--ff-only", expected)
	return err
}
