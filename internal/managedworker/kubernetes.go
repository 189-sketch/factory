package managedworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

// publishCandidate imports objects into a disposable bare repository. It never
// checks out sandbox content or executes its hooks, filters, builds or tests.
func (w *Worker) publishCandidate(ctx context.Context, workspace, bundle string, spec protocol.RunSpec, result *runner.Result) error {
	if result.StepResult == nil || result.StepResult.Factory == nil {
		return fmt.Errorf("sandbox result is missing its factory report")
	}
	f := result.StepResult.Factory
	if result.StepResult.Outcome != "complete" {
		return nil
	}
	branch := "codex/" + spec.JobID
	if spec.Factory.Branch != "" {
		branch = spec.Factory.Branch
	}
	if bundle != "" && (f.Branch != branch || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(f.HeadSHA)) {
		return fmt.Errorf("sandbox reported a different branch or invalid commit")
	}
	remote, repository, err := factoryRemote(repositoryCommand(ctx, workspace))
	if err != nil {
		return err
	}
	base, err := repositoryCommand(ctx, workspace)("git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	baseSHA := strings.TrimSpace(string(base))
	directory := filepath.Dir(result.EventsPath)
	root := filepath.Join(directory, "publish.git")
	if _, err = repositoryCommand(ctx, directory)("git", "init", "--bare", root); err != nil {
		return err
	}
	command := repositoryCommand(ctx, root)
	git := func(args ...string) ([]byte, error) {
		return command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	}
	if _, err = git("fetch", "--no-tags", filepath.Join(directory, "input.bundle"), "HEAD"); err != nil {
		return err
	}
	if bundle != "" {
		if _, err = git("bundle", "verify", bundle); err != nil {
			return fmt.Errorf("invalid candidate bundle: %w", err)
		}
		heads, err := git("bundle", "list-heads", bundle)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(heads)) != f.HeadSHA+" HEAD" {
			return fmt.Errorf("candidate must contain exactly the reported HEAD")
		}
		if _, err = git("fetch", "--no-tags", bundle, "HEAD"); err != nil {
			return err
		}
		if _, err = git("fsck", "--strict", "--no-dangling", f.HeadSHA); err != nil {
			return err
		}
		if _, err = git("merge-base", "--is-ancestor", baseSHA, f.HeadSHA); err != nil {
			return fmt.Errorf("candidate does not descend from assigned base")
		}
		if f.HeadSHA == baseSHA {
			return fmt.Errorf("candidate has no new commit")
		}
	} else {
		refs, e := repositoryCommand(ctx, workspace)("git", "ls-remote", "--heads", remote, "refs/heads/"+branch)
		if e != nil {
			return e
		}
		recovered, e := recoverPublication(string(refs), baseSHA, branch, spec.Factory.PRURL)
		if e != nil {
			return e
		}
		if !recovered {
			if spec.Factory.Recipe != "single" {
				return fmt.Errorf("requested delivery contains no committed changes")
			}
			f.HeadSHA = ""
			f.Branch = ""
			f.PRURL = ""
			return nil
		}
		f.HeadSHA = baseSHA
		f.Branch = branch
	}
	remoteURL := "https://github.com/" + repository + ".git"
	current, err := git("ls-remote", "--heads", remoteURL, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(current))
	if len(fields) > 0 && (len(fields) != 2 || (fields[0] != baseSHA && fields[0] != f.HeadSHA) || fields[1] != "refs/heads/"+branch) {
		return fmt.Errorf("task branch changed remotely; refusing publication")
	}
	if len(fields) == 0 && spec.Factory.PRURL != "" {
		return fmt.Errorf("recorded PR branch is missing")
	}
	if spec.Factory.Role == "spec" {
		entry, e := git("ls-tree", f.HeadSHA, "--", spec.Factory.SpecPath)
		if e != nil {
			return e
		}
		if !strings.HasPrefix(string(entry), "100644 blob ") && !strings.HasPrefix(string(entry), "100755 blob ") {
			return fmt.Errorf("candidate is missing the required specification document")
		}
		f.SpecURL = "https://github.com/" + repository + "/blob/" + f.HeadSHA + "/" + spec.Factory.SpecPath
	}
	// A successful heartbeat is required immediately before publication. Old
	// attempts fail closed after a control-plane restart or operator cancel.
	if err = w.heartbeat(ctx, spec); err != nil {
		return fmt.Errorf("publication lease is no longer active: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	intent, _ := json.Marshal(map[string]string{"repository": repository, "branch": branch, "base": baseSHA, "head": f.HeadSHA})
	if err = os.WriteFile(filepath.Join(directory, "publication.json"), intent, 0600); err != nil {
		return err
	}
	if bundle != "" {
		if _, err = git("push", remoteURL, f.HeadSHA+":refs/heads/"+branch); err != nil {
			return err
		}
	}
	// Find before create, including after a lost response on an earlier attempt.
	query := "repos/" + repository + "/pulls?state=open&head=" + url.QueryEscape(strings.Split(repository, "/")[0]+":"+branch)
	prs, err := command("gh", "api", query)
	if err != nil {
		return err
	}
	var existing []struct {
		URL string `json:"html_url"`
	}
	if err = json.Unmarshal(prs, &existing); err != nil {
		return err
	}
	if len(existing) > 1 {
		return fmt.Errorf("multiple PRs use the assigned branch")
	}
	if len(existing) == 1 {
		f.PRURL = existing[0].URL
	} else {
		defaultRef, e := repositoryCommand(ctx, workspace)("git", "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
		if e != nil {
			return e
		}
		title := "Machinist task"
		if spec.Task != nil && spec.Task.Title != "" {
			title = spec.Task.Title
		}
		body := f.Report
		if len(body) > 12000 {
			body = body[:12000]
		}
		request, _ := json.Marshal(map[string]any{"title": title, "head": branch, "base": strings.TrimPrefix(strings.TrimSpace(string(defaultRef)), remote+"/"), "body": body, "draft": spec.Factory.Role == "spec"})
		requestPath := filepath.Join(directory, "pull-request.json")
		if err = os.WriteFile(requestPath, request, 0600); err != nil {
			return err
		}
		created, e := command("gh", "api", "--method", "POST", "repos/"+repository+"/pulls", "--input", requestPath)
		if e != nil {
			return e
		}
		var pr struct {
			URL string `json:"html_url"`
		}
		if e = json.Unmarshal(created, &pr); e != nil {
			return e
		}
		f.PRURL = pr.URL
	}
	if spec.Factory.PRURL != "" && f.PRURL != spec.Factory.PRURL {
		return fmt.Errorf("publication changed the assigned PR")
	}
	if spec.Factory.Role == "implement" {
		if _, err = command("gh", "pr", "ready", f.PRURL); err != nil {
			// gh returns nonzero for an already-ready PR. Read current state.
			check, e := command("gh", "pr", "view", f.PRURL, "--json", "isDraft")
			if e != nil {
				return err
			}
			var pr struct {
				Draft bool `json:"isDraft"`
			}
			if json.Unmarshal(check, &pr) != nil || pr.Draft {
				return err
			}
		}
	}
	return f.Validate()
}

// Recover an already-published first attempt, including a lost PR response.
// A repair with a recorded PR still needs a new commit.
func recoverPublication(refs, base, branch, recordedPR string) (bool, error) {
	fields := strings.Fields(refs)
	if len(fields) == 0 {
		return false, nil
	}
	if len(fields) != 2 || fields[0] != base || fields[1] != "refs/heads/"+branch {
		return false, fmt.Errorf("assigned remote branch differs from the recovered candidate")
	}
	if recordedPR != "" {
		return false, fmt.Errorf("repair contains no new commit")
	}
	return true, nil
}
