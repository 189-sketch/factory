package controlplane

import (
	"context"
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
