package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

type followupFake struct {
	*memoryFactoryGitHub
	pr         githubFollowupPR
	err        error
	reads      int
	duringRead func()
}

func (f *followupFake) PullRequestFollowup(context.Context, string, string) (githubFollowupPR, error) {
	f.reads++
	if f.duringRead != nil {
		f.duringRead()
	}
	return f.pr, f.err
}
func followupFixture(t *testing.T, checks string) githubFollowupPR {
	t.Helper()
	raw := `{"url":"https://github.com/owner/repo/pull/2","state":"OPEN","headRefOid":"` + strings.Repeat("a", 40) + `","headRepository":{"nameWithOwner":"owner/repo"},"commits":{"nodes":[{"commit":{"oid":"` + strings.Repeat("a", 40) + `","statusCheckRollup":{"contexts":{"nodes":` + checks + `}}}}]}}`
	var pr githubFollowupPR
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		t.Fatal(err)
	}
	return pr
}
func readyFollowupTask(t *testing.T) (*GitHubFactoryServer, *followupFake, string) {
	t.Helper()
	f := &followupFake{memoryFactoryGitHub: newMemoryFactoryGitHub(), pr: followupFixture(t, `[]`)}
	s := githubFactoryTestServer(t, f)
	id := factorySubmit(t, s)
	factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{})
	metadata := protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: f.pr.HeadRefOID, Branch: "codex/task"}
	factoryFinish(t, s, factoryPoll(t, s), metadata)
	metadata.Verdict = "accepted"
	factoryFinish(t, s, factoryPoll(t, s), metadata)
	if factoryJob(t, s, id).State != "awaiting_merge" {
		t.Fatal("fixture not ready")
	}
	return s, f, id
}
func tickFollowups(t *testing.T, s *GitHubFactoryServer) {
	t.Helper()
	if err := s.tickGitHubFollowups(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubFollowupsRepairSamePRAndDeduplicateAcrossRestart(t *testing.T) {
	s, f, id := readyFollowupTask(t)
	f.pr = followupFixture(t, `[{"__typename":"CheckRun","id":"check-1","name":"Tests","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/owner/repo/actions/runs/1"}]`)
	tickFollowups(t, s)
	job := factoryJob(t, s, id)
	if job.State != "queued" || job.Workflow.Factory.RepairRound != 1 || len(job.Runs) != 4 {
		t.Fatalf("not repaired: %+v", job)
	}
	run := factoryPoll(t, s)
	if run.Factory.PRURL != f.pr.URL || run.Factory.HeadSHA != f.pr.HeadRefOID || run.Factory.Role != "implement" || !strings.Contains(run.Factory.Context, "CI failure: Tests") || !strings.Contains(run.Factory.Context, "untrusted evidence") {
		t.Fatal("repair lost PR or evidence")
	}
	// Complete with a new reviewed head. The same old review feedback must never
	// create a duplicate repair, including after replay from the journal.
	head := strings.Repeat("b", 40)
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: head, Branch: "codex/task"})
	factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: head, Branch: "codex/task", Verdict: "accepted"})
	f.pr = followupFixture(t, `[]`)
	f.pr.HeadRefOID = head
	f.pr.Commits.Nodes[0].Commit.OID = head
	fresh := restartFactoryTestServer(t, s, f)
	tickFollowups(t, fresh)
	if got := factoryJob(t, fresh, id); got.State != "awaiting_merge" || got.Workflow.Factory.RepairRound != 1 {
		t.Fatal("replayed repair")
	}
	if !fresh.tasks[id].state.GitHubHandled[strings.Repeat("a", 40)+":check-1"] {
		t.Fatal("lost durable deduplication")
	}
}
func TestGitHubFollowupsMergeAndFailClosed(t *testing.T) {
	for _, scenario := range []string{"merged", "missing merge commit", "changed head", "closed", "pending", "cancelled check", "draft", "private", "cancelled task", "offline", "race hold", "incomplete checks", "fork", "wrong PR"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, id := readyFollowupTask(t)
			before := factoryJob(t, s, id)
			want := "awaiting_merge"
			switch scenario {
			case "merged":
				f.pr.State = "MERGED"
				f.pr.MergeCommit = &struct{ OID string }{strings.Repeat("c", 40)}
				want = "succeeded"
			case "missing merge commit":
				f.pr.State = "MERGED"
			case "changed head":
				f.pr.HeadRefOID = strings.Repeat("b", 40)
				want = "blocked"
			case "closed":
				f.pr.State = "CLOSED"
				want = "blocked"
			case "pending":
				f.pr = followupFixture(t, `[{"__typename":"CheckRun","id":"check-1","status":"IN_PROGRESS","conclusion":""}]`)
			case "cancelled check":
				f.pr = followupFixture(t, `[{"__typename":"CheckRun","id":"check-1","status":"COMPLETED","conclusion":"CANCELLED"}]`)
			case "draft":
				f.pr.IsDraft = true
			case "private":
				factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]string{"feedback": "Keep private"}, 200)
			case "cancelled task":
				factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": before.Runs[len(before.Runs)-1].ID}, 200)
				want = "cancelled"
			case "offline":
				f.err = errors.New("network down")
			case "race hold":
				f.duringRead = func() {
					factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/publication_hold", map[string]string{"feedback": "Keep private"}, 200)
				}
			case "incomplete checks":
				f.pr.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.PageInfo.HasNextPage = true
			case "fork":
				f.pr.HeadRepository.NameWithOwner = "other/repo"
			case "wrong PR":
				f.pr.URL = "https://github.com/other/repo/pull/2"
			}
			tickFollowups(t, s)
			job := factoryJob(t, s, id)
			if job.State != want || job.Workflow.Factory.RepairRound != 0 {
				t.Fatalf("state %s rounds %d", job.State, job.Workflow.Factory.RepairRound)
			}
			if (scenario == "private" || scenario == "race hold") && job.PublicationHold != "Keep private" {
				t.Fatal("private hold lost")
			}
			if scenario == "private" || scenario == "cancelled task" {
				if f.reads != 0 {
					t.Fatal("read excluded work")
				}
			}
			if scenario == "offline" {
				if job.GitHubSyncError == "" {
					t.Fatal("outage invisible")
				}
				f.err = nil
				tickFollowups(t, s)
				if factoryJob(t, s, id).GitHubSyncError != "" {
					t.Fatal("outage did not recover")
				}
			}
		})
	}
}
func TestGitHubFollowupsReviewFeedbackAndRepairLimit(t *testing.T) {
	s, f, id := readyFollowupTask(t)
	review := githubFollowupComment{ID: "review-1", Body: "Handle the empty input", URL: f.pr.URL + "#pullrequestreview-1", AuthorAssociation: "COLLABORATOR", State: "CHANGES_REQUESTED", Author: &struct{ Login string }{"reviewer"}, Commit: &struct{ OID string }{f.pr.HeadRefOID}}
	f.pr.Reviews.Nodes = []githubFollowupComment{review}
	// Untrusted reviewers and outdated reviews do not trigger code execution.
	for _, association := range []string{"NONE", "CONTRIBUTOR", "FIRST_TIMER"} {
		f.pr.Reviews.Nodes[0].AuthorAssociation = association
		tickFollowups(t, s)
		if factoryJob(t, s, id).State != "awaiting_merge" {
			t.Fatal("untrusted feedback dispatched")
		}
	}
	f.pr.Reviews.Nodes[0] = review
	tickFollowups(t, s)
	if factoryJob(t, s, id).Workflow.Factory.RepairRound != 1 {
		t.Fatal("review not routed")
	}
	// Exercise the existing bounded repair policy with distinct CI failures.
	for round := 1; round <= 3; round++ {
		job := factoryJob(t, s, id)
		run := factoryPoll(t, s)
		if run == nil {
			t.Fatal("repair missing")
		}
		head := strings.Repeat(string(rune('b'+round)), 40)
		factoryFinish(t, s, run, protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: head, Branch: "codex/task"})
		factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: head, Branch: "codex/task", Verdict: "accepted"})
		f.pr = followupFixture(t, `[{"__typename":"StatusContext","id":"status","context":"CI","state":"FAILURE"}]`)
		f.pr.HeadRefOID = head
		f.pr.Commits.Nodes[0].Commit.OID = head
		tickFollowups(t, s)
		got := factoryJob(t, s, id)
		if round < 3 && got.Workflow.Factory.RepairRound != job.Workflow.Factory.RepairRound+1 {
			t.Fatal("missing repair")
		}
		if round == 3 && (got.State != "blocked" || got.Workflow.Factory.RepairRound != 3) {
			t.Fatal("repair limit bypassed")
		}
	}
}
func TestGitHubFollowupReadBoundary(t *testing.T) {
	requests := 0
	c := testGitHubConnection(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			Query     string
			Variables struct {
				Owner, Name string
				Number      int
			}
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Query != githubFollowupQuery || input.Variables.Owner != "owner" || input.Variables.Name != "repo" || input.Variables.Number != 2 {
			t.Fatal("unscoped query")
		}
		w.Write([]byte(`{"data":{"repository":{"pullRequest":{"url":"https://github.com/owner/repo/pull/2","state":"OPEN"}}}}`))
	})
	c.allowed["owner/repo"] = true
	c.credentials.AccessToken = "test"
	g := &ghFactoryClient{api: c.API}
	if _, err := g.PullRequestFollowup(t.Context(), "owner/repo", "https://github.com/owner/repo/pull/2"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"other/repo", "https://github.com/other/repo/pull/2"}, {"owner/repo", "https://github.com/other/repo/pull/2"}, {"owner/repo", "https://github.com/owner/repo/pull/2?next=evil"}} {
		if _, err := g.PullRequestFollowup(t.Context(), pair[0], pair[1]); err == nil {
			t.Fatal("accepted unscoped PR")
		}
	}
	if requests != 1 {
		t.Fatal("unscoped request reached GitHub")
	}
}

func TestGitHubFollowupThreadsOnlyActOnNewReviewerFeedback(t *testing.T) {
	s, f, id := readyFollowupTask(t)
	raw := `{"reviewThreads":{"nodes":[{"id":"thread-1","path":"main.go","line":12,"comments":{"nodes":[{"id":"comment-1","body":"Handle the error","authorAssociation":"COLLABORATOR","author":{"login":"reviewer"}},{"id":"reply-1","body":"Fixed","authorAssociation":"OWNER","author":{"login":"implementer"}}]}}]}}`
	if err := json.Unmarshal([]byte(raw), &f.pr); err != nil {
		t.Fatal(err)
	}
	thread := &f.pr.ReviewThreads.Nodes[0]
	thread.IsResolved = true
	tickFollowups(t, s)
	if factoryJob(t, s, id).State != "awaiting_merge" {
		t.Fatal("resolved thread dispatched")
	}
	thread.IsResolved = false
	thread.IsOutdated = true
	tickFollowups(t, s)
	if factoryJob(t, s, id).State != "awaiting_merge" {
		t.Fatal("outdated thread dispatched")
	}
	thread.IsOutdated = false
	tickFollowups(t, s)
	run := factoryPoll(t, s)
	if run == nil || !strings.Contains(run.Factory.Context, "Handle the error") || strings.Contains(run.Factory.Context, "Fixed") {
		t.Fatal("wrong reviewer context")
	}
	head := strings.Repeat("b", 40)
	factoryFinish(t, s, run, protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: head, Branch: "codex/task"})
	factoryFinish(t, s, factoryPoll(t, s), protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: head, Branch: "codex/task", Verdict: "accepted"})
	f.pr.HeadRefOID = head
	f.pr.Commits.Nodes[0].Commit.OID = head
	fresh := restartFactoryTestServer(t, s, f)
	tickFollowups(t, fresh)
	if factoryJob(t, fresh, id).State != "awaiting_merge" {
		t.Fatal("consumed unresolved thread dispatched twice")
	}
	f.pr.ReviewThreads.Nodes[0].Comments.Nodes[0].Body = "There is still an error on this path"
	tickFollowups(t, fresh)
	if factoryJob(t, fresh, id).Workflow.Factory.RepairRound != 2 {
		t.Fatal("edited reviewer feedback not routed")
	}
}

func TestGitHubFollowupCommentDoesNotClearChangeRequest(t *testing.T) {
	for _, decision := range []string{"COMMENTED", "APPROVED", "DISMISSED"} {
		t.Run(decision, func(t *testing.T) {
			s, f, id := readyFollowupTask(t)
			review := githubFollowupComment{ID: "change", Body: "Fix error handling", State: "CHANGES_REQUESTED", AuthorAssociation: "COLLABORATOR", Author: &struct{ Login string }{"reviewer"}, Commit: &struct{ OID string }{f.pr.HeadRefOID}}
			second := review
			second.ID = "later"
			second.State = decision
			second.Body = "Additional context"
			f.pr.Reviews.Nodes = []githubFollowupComment{review, second}
			tickFollowups(t, s)
			want := "awaiting_merge"
			if decision == "COMMENTED" {
				want = "queued"
			}
			if factoryJob(t, s, id).State != want {
				t.Fatal("review decision changed incorrectly")
			}
		})
	}
}
func TestGitHubFollowupOutageNoticeStopsWithTask(t *testing.T) {
	s, f, id := readyFollowupTask(t)
	f.err = errors.New("offline")
	tickFollowups(t, s)
	job := factoryJob(t, s, id)
	if job.GitHubSyncError == "" {
		t.Fatal("outage notice missing")
	}
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", map[string]string{"run_id": job.Runs[len(job.Runs)-1].ID}, 200)
	if factoryJob(t, s, id).GitHubSyncError != "" {
		t.Fatal("cancelled task claims automatic retry")
	}
}

func TestGitHubFollowupSingleAgentKeepsItsPolicy(t *testing.T) {
	f := &followupFake{memoryFactoryGitHub: newMemoryFactoryGitHub(), pr: followupFixture(t, `[{"__typename":"CheckRun","id":"ci","name":"CI","status":"COMPLETED","conclusion":"FAILURE"}]`)}
	s := githubFactoryTestServer(t, f)
	var admitted struct{ ID string }
	json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "recipe": "single", "title": "Single task", "spec": "Fix the bug"}, 201), &admitted)
	first := factoryPoll(t, s)
	factoryFinish(t, s, first, protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: f.pr.HeadRefOID, Branch: "codex/task"})
	tickFollowups(t, s)
	repair := factoryPoll(t, s)
	if repair == nil || repair.Factory.Recipe != "single" || repair.Factory.Role != "implement" || repair.Factory.PRURL != f.pr.URL {
		t.Fatal("single-agent repair lost policy or PR")
	}
	factoryFinish(t, s, repair, protocol.FactoryResult{PRURL: f.pr.URL, HeadSHA: strings.Repeat("b", 40), Branch: "codex/task"})
	job := factoryJob(t, s, admitted.ID)
	if job.State != "awaiting_merge" || len(job.Runs) != 2 || factoryPoll(t, s) != nil {
		t.Fatal("single-agent mode gained an independent review")
	}
}
