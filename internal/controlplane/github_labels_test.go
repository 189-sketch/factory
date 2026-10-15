package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/owainlewis/machinist/internal/protocol"
	"reflect"
	"strings"
	"testing"
)

func TestTaskLabelsPreserveConventionsAndHumanLabels(t *testing.T) {
	current := []string{"help wanted", "machinist:task", "machinist:custom", "machinist:needs-spec"}
	catalog := []string{"Defect", "help wanted", "machinist:ready"}
	var added, removed []string
	api := func(_ context.Context, method, path string, body []byte) ([]byte, error) {
		if method == "GET" {
			names := catalog
			if strings.Contains(path, "/issues/") {
				names = current
			}
			rows := []map[string]string{}
			for _, name := range names {
				rows = append(rows, map[string]string{"name": name})
			}
			return json.Marshal(rows)
		}
		if method == "POST" && strings.Contains(path, "/issues/7/labels") {
			var in struct {
				Labels []string `json:"labels"`
			}
			json.Unmarshal(body, &in)
			added = in.Labels
			return []byte(`[]`), nil
		}
		if method == "DELETE" {
			removed = append(removed, path)
			return []byte(`{}`), nil
		}
		t.Fatalf("unexpected %s %s", method, path)
		return nil, nil
	}
	g := &ghFactoryClient{api: api}
	if err := g.SyncTaskLabels(context.Background(), "owner/repo", 7, []string{"defect", "Defect", "invented", "help wanted", "machinist:blocked", "machinist:custom"}, "machinist:ready"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(added, []string{"Defect", "machinist:ready"}) {
		t.Fatal(added)
	}
	if len(removed) != 1 || !strings.HasSuffix(removed[0], "machinist:needs-spec") {
		t.Fatal(removed)
	}
}
func TestTaskLabelsDoNotRemoveRouteAfterFailedAdd(t *testing.T) {
	g := &ghFactoryClient{api: func(_ context.Context, method, path string, body []byte) ([]byte, error) {
		if method == "GET" {
			if strings.Contains(path, "/issues/") {
				return []byte(`[{"name":"machinist:blocked"}]`), nil
			}
			return []byte(`[{"name":"machinist:ready"}]`), nil
		}
		if method == "DELETE" {
			t.Fatal("removed old route after failed addition")
		}
		return nil, errors.New("offline")
	}}
	if err := g.SyncTaskLabels(context.Background(), "owner/repo", 7, nil, "machinist:ready"); err == nil {
		t.Fatal("expected error")
	}
}
func TestTaskLabelsCreateOnlyOwnedRouteAndRetryIdempotently(t *testing.T) {
	catalog := []string{}
	current := []string{}
	creates, adds := 0, 0
	g := &ghFactoryClient{api: func(_ context.Context, method, path string, body []byte) ([]byte, error) {
		if method == "GET" {
			names := catalog
			if strings.Contains(path, "/issues/") {
				names = current
			}
			rows := []map[string]string{}
			for _, n := range names {
				rows = append(rows, map[string]string{"name": n})
			}
			return json.Marshal(rows)
		}
		if method == "POST" && path == "repos/owner/repo/labels" {
			creates++
			catalog = append(catalog, "machinist:ready")
			return nil, errors.New("lost response")
		}
		if method == "POST" {
			adds++
			current = append(current, "machinist:ready")
			return []byte(`[]`), nil
		}
		t.Fatal(method, path)
		return nil, nil
	}}
	for i := 0; i < 2; i++ {
		if err := g.SyncTaskLabels(context.Background(), "owner/repo", 7, []string{"invented"}, "machinist:ready"); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 1 || adds != 1 {
		t.Fatal(creates, adds)
	}
	if err := g.SyncTaskLabels(context.Background(), "owner/repo", 0, nil, ""); err == nil {
		t.Fatal("accepted local task")
	}
}
func TestFactoryRoutingLabels(t *testing.T) {
	for _, tc := range []struct{ state, role, want string }{{"queued", "spec", "machinist:needs-spec"}, {"running", "implement", "machinist:ready"}, {"blocked", "triage", "machinist:blocked"}, {"awaiting_approval", "implement", "machinist:blocked"}, {"succeeded", "review", ""}, {"awaiting_merge", "review", ""}, {"running", "triage", ""}} {
		s := &GitHubFactoryState{Job: Job{State: tc.state}, Role: tc.role}
		if got := factoryRoutingLabel(s); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

type labelFactoryFake struct {
	*memoryFactoryGitHub
	writes      int
	suggestions []string
	route       string
}

func (f *labelFactoryFake) RepositoryLabels(context.Context, string) ([]string, error) {
	return []string{"Defect"}, nil
}
func (f *labelFactoryFake) SyncTaskLabels(_ context.Context, _ string, _ int, labels []string, route string) error {
	f.writes++
	f.suggestions = append([]string(nil), labels...)
	f.route = route
	return errors.New("GitHub temporarily offline")
}
func TestForemanLabelsLinkedTasksWithoutBlockingProgress(t *testing.T) {
	base := newMemoryFactoryGitHub()
	base.issues[42] = GitHubIssue{Number: 42, Title: "Fix", Body: "Fix regression", HTMLURL: "https://github.com/owner/repo/issues/42", State: "open"}
	s := githubFactoryTestServer(t, base)
	client := &labelFactoryFake{memoryFactoryGitHub: base}
	s.client = client
	factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "workflow": "factory", "source_url": base.issues[42].HTMLURL}, 201)
	run := factoryPoll(t, s)
	if run == nil || !strings.Contains(run.Factory.Context, `"Defect"`) {
		t.Fatal("triage did not receive repository label catalog")
	}
	factoryFinish(t, s, run, protocol.FactoryResult{Labels: []string{"Defect"}})
	if client.writes == 0 || !reflect.DeepEqual(client.suggestions, []string{"Defect"}) || client.route != "machinist:ready" {
		t.Fatal(client)
	}
	next := factoryPoll(t, s)
	if next == nil || next.Factory.Role != "implement" {
		t.Fatal("label failure prevented implementation")
	}
}
func TestForemanNeverLabelsLocalTasks(t *testing.T) {
	base := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, base)
	client := &labelFactoryFake{memoryFactoryGitHub: base}
	s.client = client
	factorySubmit(t, s)
	run := factoryPoll(t, s)
	factoryFinish(t, s, run, protocol.FactoryResult{Resolved: true, Labels: []string{"Defect"}})
	if client.writes != 0 {
		t.Fatal("local task wrote labels")
	}
}
