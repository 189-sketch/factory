package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/owainlewis/machinist/internal/protocol"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFactoryLocalStorePersistsAndRejectsStaleAppend(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factory.db")
	s, err := openFactoryLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	issue := GitHubIssue{HTMLURL: "local:task", Title: "PING", Body: "Answer PONG", State: "open", CreatedAt: time.Now()}
	if err = s.create(ctx, localFactoryTask{issue: issue, repository: "owner/repo", alias: "repo"}); err != nil {
		t.Fatal(err)
	}
	event := GitHubFactoryEvent{Version: 1, Kind: "created", Role: "triage"}
	if err = s.append(ctx, issue.HTMLURL, event, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.append(ctx, issue.HTMLURL, event, nil); !errors.Is(err, ErrWorkflowAction) {
		t.Fatalf("stale append %v", err)
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openFactoryLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	tasks, err := s.tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].issue.Body != issue.Body {
		t.Fatalf("tasks %#v: %v", tasks, err)
	}
	comments, err := s.comments(ctx, issue.HTMLURL)
	if err != nil || len(comments) != 1 {
		t.Fatalf("events %#v: %v", comments, err)
	}
}
func TestFactoryLocalPromptsUseVersionCAS(t *testing.T) {
	s, err := openFactoryLocalStore(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	ctx := context.Background()
	hash, err := s.savePrompt(ctx, "owner/repo", "triage", "First prompt", "base", "base")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.savePrompt(ctx, "owner/repo", "triage", "Stale prompt", "base", "base"); !errors.Is(err, errAgentConflict) {
		t.Fatalf("stale write %v", err)
	}
	prompt, current, err := s.prompt(ctx, "owner/repo", "triage")
	if err != nil || prompt != "First prompt" || current != hash {
		t.Fatalf("prompt %s %s %v", prompt, current, err)
	}
}

type readableStatusClient struct {
	*memoryFactoryGitHub
	statuses []string
}

func (c *readableStatusClient) UpsertTaskStatus(_ context.Context, _ string, _ int, body string) error {
	c.statuses = append(c.statuses, body)
	return nil
}
func TestLinkedIssueOnlyReceivesReadableStatus(t *testing.T) {
	client := newMemoryFactoryGitHub()
	client.issues[42] = GitHubIssue{Number: 42, HTMLURL: "https://github.com/owner/repo/issues/42", Title: "PING", Body: "PONG please", State: "open", CreatedAt: time.Now()}
	s := githubFactoryTestServer(t, client)
	writer := &readableStatusClient{memoryFactoryGitHub: client}
	s.client = writer
	factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "workflow": "factory", "source_url": client.issues[42].HTMLURL}, 201)
	run := factoryPoll(t, s)
	if run == nil {
		t.Fatal("missing run")
	}
	factoryFinish(t, s, run, protocol.FactoryResult{Resolved: true, Report: "PONG"})
	if len(writer.statuses) != 3 {
		t.Fatalf("statuses %#v", writer.statuses)
	}
	for _, body := range writer.statuses {
		if strings.Contains(body, GitHubFactoryMarker) || strings.Contains(body, "prompt") || strings.Contains(body, "\"version\"") {
			t.Fatalf("leaked internal data %s", body)
		}
	}
	if len(client.comments) != 0 || client.creates != 0 || client.adopts != 0 {
		t.Fatal("journal or label written to GitHub")
	}
}

func TestSharedAgentPromptMigrationPreservesLegacyAndSharedEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	s, err := openFactoryLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for repo, prompt := range map[string]string{"owner/a": "First project prompt", "owner/b": "Other project prompt"} {
		if _, err = s.savePrompt(t.Context(), repo, "triage", prompt, "base", "base"); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Close()
	s, err = openFactoryLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	prompt, hash, err := s.prompt(t.Context(), "", "triage")
	if err != nil || prompt != "First project prompt" {
		t.Fatal("default prompt not promoted", err)
	}
	if _, err = s.savePrompt(t.Context(), "", "triage", "Shared edited prompt", hash, hash); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = openFactoryLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	prompt, _, err = s.prompt(t.Context(), "", "triage")
	if err != nil || prompt != "Shared edited prompt" {
		t.Fatal("shared edit overwritten")
	}
	prompt, _, err = s.prompt(t.Context(), "owner/b", "triage")
	if err != nil || prompt != "Other project prompt" {
		t.Fatal("legacy prompt lost")
	}
}
func TestSharedAgentDefinitionsNeedNoRepository(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories = map[string]string{}
	var definitions definitionsResponse
	if err := json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/definitions", nil, 200), &definitions); err != nil {
		t.Fatal(err)
	}
	original := definitions.Commands[0]
	factoryHTTP(t, s, "PUT", "/api/v1/factory/agents/triage", map[string]string{"prompt": "Shared instructions {{machinist.prompt}}", "expected_hash": original.Hash}, 200)
	s.repositories = map[string]string{"first": "owner/a", "second": "owner/b"}
	for _, repo := range []string{"first", "second"} {
		var got definitionsResponse
		json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/definitions?repository="+repo, nil, 200), &got)
		if got.Commands[0].Prompt != "Shared instructions {{machinist.prompt}}" {
			t.Fatal("repository affected shared agent")
		}
	}
	factoryHTTP(t, s, "PUT", "/api/v1/factory/agents/triage", map[string]string{"prompt": "Stale instructions {{machinist.prompt}}", "expected_hash": original.Hash}, 409)
}

func TestSharedAgentSaveKeepsRunningPromptAndUpdatesOtherProjects(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	s.repositories["other"] = "owner/other"
	id := factorySubmit(t, s)
	running := factoryPoll(t, s)
	var definitions definitionsResponse
	json.Unmarshal(factoryHTTP(t, s, "GET", "/api/v1/definitions", nil, 200), &definitions)
	factoryHTTP(t, s, "PUT", "/api/v1/factory/agents/triage", map[string]string{"prompt": "New shared instructions {{machinist.prompt}}", "expected_hash": definitions.Commands[0].Hash}, 200)
	if factoryJob(t, s, id).Runs[0].Prompt != running.RenderedPrompt {
		t.Fatal("running prompt snapshot changed")
	}
	factoryFinish(t, s, running, protocol.FactoryResult{Resolved: true})
	factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "other", "title": "Other project", "spec": "Research the issue"}, 201)
	request := protocol.PollRequest{SingleAgent: true, InstanceID: "worker-instance", Name: "worker", Executors: []string{"codex"}, ReadOnlyExecutors: []string{"codex"}, Repositories: []string{"other"}, Factory: true, Workflows: true, Reviews: true}
	var polled protocol.PollResponse
	json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/workers/poll", request, 200), &polled)
	if polled.Run == nil || !strings.Contains(polled.Run.RenderedPrompt, "New shared instructions") || polled.Run.GitHubRepository != "owner/other" {
		t.Fatal("shared agent not used for other project")
	}
}
