package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

const GitHubFactoryMarker = "<!-- machinist:v1 -->\n"

type GitHubFactoryEvent struct {
	Recipe     string               `json:"recipe,omitempty"`
	Version    int                  `json:"version"`
	Previous   int64                `json:"previous"`
	Kind       string               `json:"kind"`
	RunID      string               `json:"run_id,omitempty"`
	Role       string               `json:"role,omitempty"`
	Prompt     string               `json:"prompt,omitempty"`
	PromptHash string               `json:"prompt_hash,omitempty"`
	Executor   string               `json:"executor,omitempty"`
	Model      string               `json:"model,omitempty"`
	WorkerName string               `json:"worker_name,omitempty"`
	Result     *protocol.StepResult `json:"result,omitempty"`
	Feedback   string               `json:"feedback,omitempty"`
	Stopped    bool                 `json:"stopped,omitempty"`
}

type GitHubFactoryState struct {
	FixedRecipe     bool
	Job             Job
	LastEventID     int64
	Role            string
	SuggestedLabels []string
	Reports         map[string]string
	PromptSnapshot  string
	PromptHash      string
	Feedback        string
}

func GitHubFactoryRunID(issueURL string, lastEventID int64, role string, repairRound int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%s\n%d", issueURL, lastEventID, role, repairRound)))
	return "run_" + hex.EncodeToString(sum[:12])
}

// ReduceGitHubFactory rebuilds state exclusively from the trusted append-only journal.
// Unrelated comments are ignored; malformed or forked trusted events fail closed.
func ReduceGitHubFactory(issue GitHubIssue, comments []GitHubComment, actor, repository string) (*GitHubFactoryState, error) {
	sum := sha256.Sum256([]byte(issue.HTMLURL))
	s := &GitHubFactoryState{Job: Job{ID: "github_" + hex.EncodeToString(sum[:12]), Task: &protocol.Task{Title: issue.Title, SourceURL: issue.HTMLURL, Spec: issue.Body}, Prompt: issue.Body, Repository: repository, GitHubIssueTitle: issue.Title, TriggerSubject: issue.HTMLURL, Command: "factory", State: "untracked", StateBackend: "github", CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt, Workflow: &WorkflowProgress{Name: "factory", Steps: []string{"triage", "spec", "implement", "review"}, Factory: &FactoryProgress{}}}, Reports: map[string]string{}}
	ordered := append([]GitHubComment(nil), comments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, comment := range ordered {
		if !strings.EqualFold(comment.User.Login, actor) || !strings.HasPrefix(comment.Body, GitHubFactoryMarker) {
			continue
		}
		var event GitHubFactoryEvent
		dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(comment.Body, GitHubFactoryMarker)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&event); err != nil {
			return nil, fmt.Errorf("journal comment %d: %w", comment.ID, err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("journal comment %d contains trailing data", comment.ID)
		}
		if event.Version != 1 || event.Previous != s.LastEventID || comment.ID <= s.LastEventID {
			return nil, fmt.Errorf("journal comment %d has unsupported version or broken chain", comment.ID)
		}
		if event.Kind == "created" {
			recipe := event.Recipe
			if recipe == "" {
				recipe = "factory"
			}
			if recipe != "factory" && recipe != "direct" && recipe != "answer" && recipe != "single" {
				return nil, fmt.Errorf("invalid job recipe")
			}
			first := "triage"
			if recipe == "direct" || recipe == "single" {
				first = "implement"
			}
			if s.LastEventID != 0 || event.Role != first {
				return nil, fmt.Errorf("invalid factory creation")
			}
			s.LastEventID = comment.ID
			s.FixedRecipe = event.Recipe != ""
			s.Job.Recipe = recipe
			s.Job.Workflow.Name = recipe
			if recipe == "direct" || recipe == "single" {
				s.Job.Workflow.Steps = []string{"implement", "review"}
			}
			if recipe == "single" {
				s.Job.Workflow.Steps = []string{"implement"}
			}
			if recipe == "answer" {
				s.Job.Workflow.Steps = []string{"triage"}
			}
			s.queue(issue.HTMLURL, first, "queued")
			continue
		}
		if s.LastEventID == 0 || len(s.Job.Runs) == 0 {
			return nil, fmt.Errorf("factory journal has no creation")
		}
		if event.Kind == "clear_inbox" {
			s.Job.InboxCleared = true
			s.LastEventID = comment.ID
			continue
		}
		s.Job.InboxCleared = false
		if event.Kind == "publication_hold" || event.Kind == "release_publication" {
			if event.Kind == "publication_hold" {
				if strings.TrimSpace(event.Feedback) == "" {
					return nil, fmt.Errorf("hold reason required")
				}
				s.Job.PublicationHold = event.Feedback
				s.Job.HoldVersion = comment.ID
			} else {
				s.Job.PublicationHold = ""
				s.Job.HoldVersion = comment.ID
			}
			s.LastEventID = comment.ID
			s.Job.UpdatedAt = comment.CreatedAt
			continue
		}
		run := &s.Job.Runs[len(s.Job.Runs)-1]
		if event.RunID != run.ID {
			return nil, fmt.Errorf("journal comment %d references a stale attempt", comment.ID)
		}
		if event.Role != "" && event.Role != s.Role {
			return nil, fmt.Errorf("journal role does not match current stage")
		}
		s.LastEventID = comment.ID
		s.Job.UpdatedAt = comment.CreatedAt
		switch event.Kind {
		case "started":
			if s.Job.State != "queued" || event.Role != s.Role {
				return nil, fmt.Errorf("stage is not queued")
			}
			if strings.TrimSpace(event.Prompt) == "" || event.PromptHash == "" {
				return nil, fmt.Errorf("started stage requires its prompt snapshot and hash")
			}
			run.State = "running"
			run.StartedAt = comment.CreatedAt
			run.Executor = event.Executor
			run.Model = event.Model
			run.WorkerName = event.WorkerName
			s.Job.State = "running"
			s.PromptSnapshot = event.Prompt
			s.PromptHash = event.PromptHash
		case "completed", "completion":
			if s.Job.State != "running" || event.Result == nil {
				return nil, fmt.Errorf("completion requires an active attempt and result")
			}
			raw, err := json.Marshal(event.Result)
			if err != nil {
				return nil, err
			}
			result, err := protocol.ParseStepResult(raw)
			if err != nil {
				return nil, err
			}
			if result.Factory == nil || result.Factory.Role != s.Role {
				return nil, fmt.Errorf("completion requires matching factory role")
			}
			if result.Outcome == "complete" && s.Role != "review" && strings.TrimSpace(result.Factory.Report) == "" {
				return nil, fmt.Errorf("completed stage requires a report")
			}
			run.Report = result.Factory.Report
			run.GitHubURL = fmt.Sprintf("%s#issuecomment-%d", issue.HTMLURL, comment.ID)
			run.CompletedAt = comment.CreatedAt
			if !run.StartedAt.IsZero() {
				duration := max(int64(0), comment.CreatedAt.Sub(run.StartedAt).Milliseconds())
				run.DurationMillis = &duration
			}
			run.Outcome = result.Outcome
			run.Summary = result.Summary
			run.State = "succeeded"
			s.Reports[s.Role] = result.Factory.Report
			if s.Reports[s.Role] == "" {
				s.Reports[s.Role] = result.Summary
			}
			if result.Outcome != "complete" {
				run.State = result.Outcome
				s.Job.State = result.Outcome
				continue
			}
			if err = s.complete(issue.HTMLURL, result); err != nil {
				return nil, err
			}
		case "interrupted":
			if s.Job.State != "running" {
				return nil, fmt.Errorf("only running attempts can be interrupted")
			}
			run.State = "interrupted"
			run.Error = event.Feedback
			s.Job.State = "interrupted"
		case "approve":
			if s.Job.State != "awaiting_approval" {
				return nil, ErrWorkflowAction
			}
			run.State = "queued"
			s.Job.State = "queued"
		case "retry":
			if s.Job.State != "blocked" && s.Job.State != "failed" && s.Job.State != "interrupted" && s.Job.State != "cancelled" {
				return nil, ErrWorkflowAction
			}
			if (s.Job.State == "interrupted" || s.Job.State == "cancelled") && !event.Stopped {
				return nil, fmt.Errorf("confirm previous process stopped before retry")
			}
			s.Feedback = event.Feedback
			s.queue(issue.HTMLURL, s.Role, "queued")
		case "request_changes":
			if s.Job.Recipe == "answer" {
				return nil, fmt.Errorf("Answer jobs use retry for feedback and cannot route into delivery")
			}
			if s.Job.State != "awaiting_approval" && s.Job.State != "awaiting_merge" && s.Job.State != "blocked" && !(s.Job.State == "failed" && s.Role == "review" && s.Job.Workflow.Factory.PRURL != "") {
				return nil, ErrWorkflowAction
			}
			if strings.TrimSpace(event.Feedback) == "" {
				return nil, fmt.Errorf("revision feedback is required")
			}
			s.Feedback = event.Feedback
			if s.Role == "implement" && s.Job.State == "awaiting_approval" {
				s.queue(issue.HTMLURL, "spec", "queued")
			} else {
				s.repair(issue.HTMLURL)
			}
		case "cancel":
			if s.Job.State == "succeeded" || s.Job.State == "cancelled" {
				return nil, ErrWorkflowAction
			}
			run.State = "cancelled"
			s.Job.State = "cancelled"
		case "complete_merge":
			if s.Job.State != "awaiting_merge" {
				return nil, ErrWorkflowAction
			}
			s.Job.State = "succeeded"
		default:
			return nil, fmt.Errorf("unknown factory event %q", event.Kind)
		}
	}
	return s, nil
}

func (s *GitHubFactoryState) queue(url, role, state string) {
	s.Role = role
	index := 0
	for i, name := range s.Job.Workflow.Steps {
		if name == role {
			index = i
		}
	}
	s.Job.Workflow.CurrentStep = index
	s.Job.State = state
	s.PromptSnapshot = ""
	s.PromptHash = ""
	reviewed := ""
	if state == "awaiting_approval" && len(s.Job.Runs) > 0 {
		reviewed = s.Job.Runs[len(s.Job.Runs)-1].ID
	}
	s.Job.Runs = append(s.Job.Runs, Run{ReviewedRunID: reviewed, ID: GitHubFactoryRunID(url, s.LastEventID, role, s.Job.Workflow.Factory.RepairRound), Command: role, Step: &index, State: state})
}
func (s *GitHubFactoryState) block(reason string) {
	s.Job.State = "blocked"
	s.Job.Runs[len(s.Job.Runs)-1].Summary = reason
}
func (s *GitHubFactoryState) repair(url string) {
	if s.Job.Workflow.Factory.RepairRound >= 3 {
		s.block("Three repair rounds exhausted")
		return
	}
	s.Job.Workflow.Factory.RepairRound++
	s.queue(url, "implement", "queued")
}
func (s *GitHubFactoryState) complete(url string, result *protocol.StepResult) error {
	f := result.Factory
	if f == nil || f.Role != s.Role {
		return fmt.Errorf("completion requires matching factory role")
	}
	progress := s.Job.Workflow.Factory
	switch s.Role {
	case "triage":
		s.SuggestedLabels = append([]string(nil), f.Labels...)
		if s.Job.Recipe == "answer" && (!f.Resolved || f.NeedsSpec || f.PRURL != "" || f.SpecURL != "" || f.HeadSHA != "" || f.Branch != "") {
			s.block("Answer jobs cannot deliver files or commits. Start a delivery job instead.")
		} else if s.FixedRecipe && s.Job.Recipe != "answer" && f.Resolved {
			s.block("Delivery jobs require a pull request; use an Answer job for questions")
		} else if f.Resolved {
			s.Job.State = "succeeded"
		} else if f.NeedsSpec {
			s.queue(url, "spec", "queued")
		} else {
			s.queue(url, "implement", "queued")
		}
	case "spec":
		if f.SpecURL == "" || f.PRURL == "" || f.HeadSHA == "" || strings.TrimSpace(f.Branch) == "" {
			s.block("Specification must report its GitHub document URL, draft PR, head SHA and branch")
			return nil
		}
		if progress.PRURL != "" && (progress.PRURL != f.PRURL || progress.Branch != f.Branch) {
			s.block("Specification revision must retain the existing PR and branch")
			return nil
		}
		progress.SpecURL = f.SpecURL
		progress.PRURL = f.PRURL
		progress.HeadSHA = f.HeadSHA
		progress.Branch = f.Branch
		s.queue(url, "implement", "awaiting_approval")
	case "implement":
		if s.Job.Recipe == "single" && progress.PRURL == "" && f.PRURL == "" && f.HeadSHA == "" && f.Branch == "" {
			s.Job.State = "succeeded"
			return nil
		}
		if f.PRURL == "" || f.HeadSHA == "" || strings.TrimSpace(f.Branch) == "" {
			s.block("Implementation must report PR, head SHA and branch")
			return nil
		}
		if progress.PRURL != "" && (progress.PRURL != f.PRURL || progress.Branch != f.Branch) {
			s.block("Repair must retain the existing PR and branch")
			return nil
		}
		if progress.HeadSHA != "" && progress.HeadSHA == f.HeadSHA {
			s.block("Repair made no new commit")
			return nil
		}
		progress.PRURL = f.PRURL
		progress.HeadSHA = f.HeadSHA
		progress.Branch = f.Branch
		progress.Verdict = ""
		progress.Findings = nil
		if s.Job.Recipe == "single" {
			s.Job.State = "awaiting_merge"
		} else {
			s.queue(url, "review", "queued")
		}
	case "review":
		if f.PRURL != progress.PRURL || f.HeadSHA != progress.HeadSHA || f.Verdict == "" {
			s.block("Review must identify current PR and exact head")
			return nil
		}
		progress.Verdict = f.Verdict
		progress.Findings = f.Findings
		switch f.Verdict {
		case "accepted":
			s.Job.State = "awaiting_merge"
		case "human_decision_needed":
			s.block("Review needs human judgment")
		case "revision_needed":
			s.Feedback = formatFindings(f.Findings)
			s.repair(url)
		}
	}
	return nil
}
