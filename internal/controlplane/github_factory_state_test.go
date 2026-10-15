package controlplane

import (
	"encoding/json"
	"github.com/owainlewis/machinist/internal/protocol"
	"strings"
	"testing"
)

type journalFixture struct {
	t        *testing.T
	issue    GitHubIssue
	comments []GitHubComment
	state    *GitHubFactoryState
}

func newJournal(t *testing.T) *journalFixture {
	t.Helper()
	j := &journalFixture{t: t, issue: GitHubIssue{HTMLURL: "https://github.com/owner/repo/issues/1", Title: "Task", Body: "Deliver it"}}
	j.add(GitHubFactoryEvent{Kind: "created", Role: "triage"})
	return j
}
func (j *journalFixture) add(e GitHubFactoryEvent) {
	j.t.Helper()
	e.Version = 1
	if j.state != nil {
		e.Previous = j.state.LastEventID
		if e.RunID == "" {
			e.RunID = j.state.Job.Runs[len(j.state.Job.Runs)-1].ID
		}
	}
	b, _ := json.Marshal(e)
	j.comments = append(j.comments, GitHubComment{ID: int64(len(j.comments) + 1), User: GitHubActor{Login: "bot"}, Body: GitHubFactoryMarker + string(b)})
	s, err := ReduceGitHubFactory(j.issue, j.comments, "bot", "repo")
	if err != nil {
		j.t.Fatal(err)
	}
	j.state = s
}
func (j *journalFixture) finish(f protocol.FactoryResult) {
	j.t.Helper()
	role := j.state.Role
	j.add(GitHubFactoryEvent{Kind: "started", Role: role, Prompt: "Exact prompt", PromptHash: "hash"})
	f.Report = "Detailed report for " + role
	f.Role = role
	f.Version = 1
	j.add(GitHubFactoryEvent{Kind: "completed", Role: role, Result: &protocol.StepResult{Outcome: "complete", Summary: "Finished " + role, Factory: &f}})
}
func TestGitHubFactorySpecApprovalAndAcceptedReview(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{NeedsSpec: true})
	j.finish(specFixtureResult())
	if j.state.Job.State != "awaiting_approval" || j.state.Role != "implement" {
		t.Fatal(j.state)
	}
	j.add(GitHubFactoryEvent{Kind: "approve"})
	j.finish(protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", Branch: "task-1", HeadSHA: strings.Repeat("b", 40)})
	j.finish(protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("b", 40), Verdict: "accepted"})
	if j.state.Job.State != "awaiting_merge" {
		t.Fatal(j.state.Job.State)
	}
	j.add(GitHubFactoryEvent{Kind: "complete_merge"})
	if j.state.Job.State != "succeeded" {
		t.Fatal(j.state.Job.State)
	}
}
func TestGitHubFactoryTrustAndBrokenChains(t *testing.T) {
	j := newJournal(t)
	j.comments = append(j.comments, GitHubComment{ID: 20, User: GitHubActor{Login: "attacker"}, Body: GitHubFactoryMarker + `{"version":1,"kind":"cancel"}`}, GitHubComment{ID: 21, User: GitHubActor{Login: "bot"}, Body: "ordinary conversation"})
	if _, err := ReduceGitHubFactory(j.issue, j.comments, "bot", "repo"); err != nil {
		t.Fatal(err)
	}
	j.comments = append(j.comments, GitHubComment{ID: 22, User: GitHubActor{Login: "bot"}, Body: GitHubFactoryMarker + `{"version":1,"previous":0,"kind":"cancel"}`})
	if _, err := ReduceGitHubFactory(j.issue, j.comments, "bot", "repo"); err == nil {
		t.Fatal("accepted fork")
	}
}
func TestGitHubFactoryStaleAttemptRejected(t *testing.T) {
	j := newJournal(t)
	e := GitHubFactoryEvent{Version: 1, Previous: j.state.LastEventID, Kind: "started", Role: "triage", RunID: "stale", Prompt: "p", PromptHash: "h"}
	b, _ := json.Marshal(e)
	j.comments = append(j.comments, GitHubComment{ID: 2, User: GitHubActor{Login: "bot"}, Body: GitHubFactoryMarker + string(b)})
	if _, err := ReduceGitHubFactory(j.issue, j.comments, "bot", "repo"); err == nil {
		t.Fatal("accepted stale attempt")
	}
}
func TestGitHubFactoryInterruptedNeverReplays(t *testing.T) {
	j := newJournal(t)
	j.add(GitHubFactoryEvent{Kind: "started", Role: "triage", Prompt: "p", PromptHash: "h"})
	if j.state.Job.State != "running" {
		t.Fatal(j.state.Job.State)
	}
	j.add(GitHubFactoryEvent{Kind: "interrupted", Feedback: "Process state unknown"})
	j.add(GitHubFactoryEvent{Kind: "retry", Stopped: true})
	if j.state.Job.State != "queued" || len(j.state.Job.Runs) != 2 {
		t.Fatal(j.state)
	}
}
func TestGitHubFactoryRepairBoundSurvivesReconstruction(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{})
	pr := "https://github.com/owner/repo/pull/2"
	finding := protocol.Finding{Severity: "p1", File: "main.go", Line: 1, Problem: "Bug", Impact: "Breaks", Correction: "Fix"}
	for i := 0; i < 4; i++ {
		sha := strings.Repeat(string(rune('a'+i)), 40)
		j.finish(protocol.FactoryResult{PRURL: pr, Branch: "task-1", HeadSHA: sha})
		j.finish(protocol.FactoryResult{PRURL: pr, HeadSHA: sha, Verdict: "revision_needed", Findings: []protocol.Finding{finding}})
	}
	if j.state.Job.State != "blocked" || j.state.Job.Workflow.Factory.RepairRound != 3 {
		t.Fatal(j.state)
	}
}
func TestGitHubFactoryStaleReviewBlocked(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{})
	pr := "https://github.com/owner/repo/pull/2"
	j.finish(protocol.FactoryResult{PRURL: pr, Branch: "task-1", HeadSHA: strings.Repeat("a", 40)})
	j.finish(protocol.FactoryResult{PRURL: pr, HeadSHA: strings.Repeat("b", 40), Verdict: "accepted"})
	if j.state.Job.State != "blocked" {
		t.Fatal(j.state.Job.State)
	}
}

func TestGitHubFactoryUntrackedIssue(t *testing.T) {
	state, err := ReduceGitHubFactory(GitHubIssue{HTMLURL: "https://github.com/o/r/issues/1"}, nil, "bot", "repo")
	if err != nil || state.LastEventID != 0 || state.Job.State != "untracked" || len(state.Job.Runs) != 0 || state.Job.StateBackend != "github" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
func TestGitHubFactoryApprovalBindsReport(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{NeedsSpec: true})
	j.finish(specFixtureResult())
	runs := j.state.Job.Runs
	if runs[len(runs)-1].ReviewedRunID != runs[len(runs)-2].ID || runs[len(runs)-2].Report != "Detailed report for spec" || runs[len(runs)-2].GitHubURL == "" {
		t.Fatal(runs)
	}
}

func TestGitHubFactoryRejectsMissingReportAndWrongRole(t *testing.T) {
	for _, test := range []struct{ name, outcome, role, report string }{{"missing report", "complete", "triage", ""}, {"failed wrong role", "failed", "review", "failure"}} {
		t.Run(test.name, func(t *testing.T) {
			j := newJournal(t)
			j.add(GitHubFactoryEvent{Kind: "started", Role: "triage", Prompt: "p", PromptHash: "h"})
			e := GitHubFactoryEvent{Version: 1, Previous: j.state.LastEventID, Kind: "completed", RunID: j.state.Job.Runs[len(j.state.Job.Runs)-1].ID, Role: "triage", Result: &protocol.StepResult{Outcome: test.outcome, Summary: "Summary", Factory: &protocol.FactoryResult{Version: 1, Role: test.role, Report: test.report}}}
			b, _ := json.Marshal(e)
			j.comments = append(j.comments, GitHubComment{ID: 3, User: GitHubActor{Login: "bot"}, Body: GitHubFactoryMarker + string(b)})
			if _, err := ReduceGitHubFactory(j.issue, j.comments, "bot", "repo"); err == nil {
				t.Fatal("accepted invalid completion")
			}
		})
	}
}

func specFixtureResult() protocol.FactoryResult {
	return protocol.FactoryResult{SpecURL: "https://github.com/owner/repo/blob/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/.machinist/specs/task-1.md", PRURL: "https://github.com/owner/repo/pull/2", Branch: "task-1", HeadSHA: strings.Repeat("a", 40)}
}
func TestGitHubFactorySpecPersistsPublishedReferences(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{NeedsSpec: true})
	spec := specFixtureResult()
	j.finish(spec)
	f := j.state.Job.Workflow.Factory
	if f.SpecURL != spec.SpecURL || f.PRURL != spec.PRURL || f.HeadSHA != spec.HeadSHA || f.Branch != spec.Branch {
		t.Fatal(f)
	}
	j.add(GitHubFactoryEvent{Kind: "approve"})
	j.finish(protocol.FactoryResult{PRURL: spec.PRURL, Branch: "different", HeadSHA: strings.Repeat("b", 40)})
	if j.state.Job.State != "blocked" {
		t.Fatal("implementation discarded specification branch")
	}
}
func TestGitHubFactorySpecMissingPublishedReferenceBlocks(t *testing.T) {
	for _, missing := range []string{"spec_url", "pr_url", "head_sha", "branch"} {
		t.Run(missing, func(t *testing.T) {
			j := newJournal(t)
			j.finish(protocol.FactoryResult{NeedsSpec: true})
			f := specFixtureResult()
			switch missing {
			case "spec_url":
				f.SpecURL = ""
			case "pr_url":
				f.PRURL = ""
			case "head_sha":
				f.HeadSHA = ""
			case "branch":
				f.Branch = ""
			}
			j.finish(f)
			if j.state.Job.State != "blocked" || j.state.Role != "spec" {
				t.Fatal(j.state)
			}
		})
	}
}

func TestGitHubFactorySpecRevisionReturnsToSpec(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{NeedsSpec: true})
	j.finish(specFixtureResult())
	j.add(GitHubFactoryEvent{Kind: "request_changes", Feedback: "Clarify acceptance"})
	if j.state.Role != "spec" || j.state.Job.State != "queued" || j.state.Job.Workflow.Factory.RepairRound != 0 {
		t.Fatal(j.state)
	}
	f := specFixtureResult()
	f.HeadSHA = strings.Repeat("b", 40)
	j.finish(f)
	if j.state.Job.State != "awaiting_approval" {
		t.Fatal(j.state.Job.State)
	}
}

func TestGitHubFactoryReviewWithoutReport(t *testing.T) {
	for _, verdict := range []string{"accepted", "revision_needed", "human_decision_needed"} {
		t.Run(verdict, func(t *testing.T) {
			j := newJournal(t)
			j.finish(protocol.FactoryResult{})
			pr := "https://github.com/owner/repo/pull/2"
			sha := strings.Repeat("a", 40)
			j.finish(protocol.FactoryResult{PRURL: pr, HeadSHA: sha, Branch: "task-1"})
			j.add(GitHubFactoryEvent{Kind: "started", Role: "review", Prompt: "Review prompt", PromptHash: "hash"})
			f := &protocol.FactoryResult{Version: 1, Role: "review", PRURL: pr, HeadSHA: sha, Verdict: verdict}
			if verdict != "accepted" {
				f.Findings = []protocol.Finding{{Severity: "p1", File: "main.go", Line: 1, Problem: "Bug", Impact: "Breaks", Correction: "Fix"}}
			}
			j.add(GitHubFactoryEvent{Kind: "completed", Role: "review", Result: &protocol.StepResult{Outcome: "complete", Summary: "Review evidence summary", Factory: f}})
			want := map[string]string{"accepted": "awaiting_merge", "revision_needed": "queued", "human_decision_needed": "blocked"}[verdict]
			if j.state.Job.State != want || j.state.Reports["review"] != "Review evidence summary" {
				t.Fatal(j.state)
			}
		})
	}
}

func TestCancelledSpecificationStillRequiresApproval(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{NeedsSpec: true})
	j.finish(specFixtureResult())
	j.add(GitHubFactoryEvent{Kind: "cancel"})
	j.add(GitHubFactoryEvent{Kind: "retry", Stopped: true})
	if j.state.Job.State != "awaiting_approval" {
		t.Fatal("retry bypassed specification approval")
	}
	j.add(GitHubFactoryEvent{Kind: "approve"})
	if j.state.Job.State != "queued" {
		t.Fatal("explicit approval did not dispatch")
	}
}
func TestSpecificationRevisionRequiresNewCommit(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{NeedsSpec: true})
	j.finish(specFixtureResult())
	j.add(GitHubFactoryEvent{Kind: "request_changes", Feedback: "Revise the design"})
	j.finish(specFixtureResult())
	if j.state.Job.State != "blocked" || !strings.Contains(j.state.Job.Runs[len(j.state.Job.Runs)-1].Summary, "no new commit") {
		t.Fatal("unchanged specification revision accepted")
	}
}
func TestCancelPreservesAcceptedReviewHistory(t *testing.T) {
	j := newJournal(t)
	j.finish(protocol.FactoryResult{})
	j.finish(protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", Branch: "task-1", HeadSHA: strings.Repeat("b", 40)})
	j.finish(protocol.FactoryResult{PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("b", 40), Verdict: "accepted"})
	j.add(GitHubFactoryEvent{Kind: "cancel"})
	if j.state.Job.State != "cancelled" || j.state.Job.Runs[len(j.state.Job.Runs)-1].State != "succeeded" {
		t.Fatal("cancellation overwrote completed review")
	}
}
