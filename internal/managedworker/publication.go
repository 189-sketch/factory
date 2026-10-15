package managedworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

// Keep one trusted publication receipt per task. A push and a GitHub API update
// cannot be atomic; a retry must finish the same candidate before starting a
// fresh sandbox. The receipt stays until a later stage/head supersedes it, so a
// lost completion response or host restart can also replay publication safely.
type candidatePublication struct {
	Repository string                  `json:"repository"`
	Workspace  string                  `json:"workspace"`
	Base       string                  `json:"base"`
	Bundle     string                  `json:"bundle"`
	Assignment protocol.FactoryContext `json:"assignment"`
	Result     runner.Result           `json:"result"`
}

func (w *Worker) publicationPath(jobID string) (string, error) {
	if jobID == "" || jobID == "." || jobID == ".." || filepath.Base(jobID) != jobID {
		return "", fmt.Errorf("invalid publication task ID")
	}
	return filepath.Join(w.config.DataDirectory, "publications", jobID+".json"), nil
}

func (w *Worker) savePublication(spec protocol.RunSpec, repository, workspace, base, bundle string, result runner.Result) error {
	path, err := w.publicationPath(spec.JobID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(candidatePublication{Repository: repository, Workspace: workspace, Base: base, Bundle: bundle, Assignment: *spec.Factory, Result: result})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".publication-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func publicationFeedback(context string) string {
	_, feedback, _ := strings.Cut(context, "\nRequested changes or answer:\n")
	return feedback
}

// Called only for Kubernetes delivery stages, after the registered repository
// and its current credential have been checked, but before workspace reconciliation.
func (w *Worker) resumePublication(ctx context.Context, registered string, spec protocol.RunSpec) (protocol.Completion, bool) {
	completion := protocol.Completion{InstanceID: w.instanceID, LeaseToken: spec.LeaseToken, State: "failed", ExitCode: 1}
	path, err := w.publicationPath(spec.JobID)
	if err != nil {
		completion.Error = err.Error()
		return completion, true
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return completion, false
	}
	var pending candidatePublication
	if err == nil {
		err = json.Unmarshal(data, &pending)
	}
	if err != nil {
		completion.Error = "could not read pending publication: " + err.Error()
		return completion, true
	}
	previous, assigned := pending.Assignment, spec.Factory
	if previous.Role != assigned.Role || previous.Recipe != assigned.Recipe || previous.Branch != assigned.Branch || previous.HeadSHA != assigned.HeadSHA || previous.PRURL != assigned.PRURL {
		return completion, false // The controller has moved to another stage/head.
	}
	if publicationFeedback(previous.Context) != publicationFeedback(assigned.Context) {
		completion.Error = "finish pending PR publication by retrying without new feedback, then request changes on the published result"
		return completion, true
	}
	_, repository, err := factoryRemote(repositoryCommand(ctx, registered))
	if err == nil && (repository != pending.Repository || (spec.GitHubRepository != "" && !strings.EqualFold(repository, spec.GitHubRepository))) {
		err = fmt.Errorf("pending publication belongs to another repository")
	}
	if err == nil && pending.Workspace != filepath.Join(w.config.DataDirectory, "workspaces", spec.JobID) {
		err = fmt.Errorf("pending publication workspace differs from assigned task")
	}
	if err == nil {
		var head []byte
		head, err = repositoryCommand(ctx, pending.Workspace)("git", "rev-parse", "HEAD")
		if err == nil && strings.TrimSpace(string(head)) != pending.Base {
			err = fmt.Errorf("pending publication base changed locally")
		}
	}
	if err == nil {
		factory := *assigned
		factory.CommitOnly, factory.SpecPath = true, previous.SpecPath
		spec.Factory = &factory
		setRunProgress(ctx, "publishing")
		err = w.publishCandidate(ctx, pending.Workspace, pending.Bundle, spec, &pending.Result)
	}
	if err != nil {
		completion.Error = err.Error()
		return completion, true
	}
	// Replaying publication consumes no additional agent tokens or execution time.
	pending.Result.TokenUsage = nil
	pending.Result.DurationMillis = 0
	pending.Result.ID = spec.ID
	pending.Result.State, pending.Result.ExitCode = runner.StateSucceeded, 0
	completion.State, completion.ExitCode = "succeeded", 0
	completion.Result, _ = json.Marshal(pending.Result)
	return completion, true
}
