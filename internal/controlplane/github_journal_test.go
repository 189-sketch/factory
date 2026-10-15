package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestGitHubJournalPaginationAndProvenance(t *testing.T) {
	calls := 0
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, input []byte) ([]byte, error) {
		calls++
		if method != "GET" || input != nil {
			t.Fatal("unexpected write")
		}
		if strings.Contains(path, "page=2") {
			return []byte(`[{"number":101,"title":"last"}]`), nil
		}
		issues := make([]GitHubIssue, 100)
		for i := range issues {
			issues[i].Number = i + 1
		}
		issues[0].PullRequest = json.RawMessage(`{"url":"pull"}`)
		return json.Marshal(issues)
	}}
	issues, err := client.ListTasks(t.Context(), "org/repo")
	if err != nil || len(issues) != 100 || calls != 2 || issues[99].Number != 101 {
		t.Fatalf("issues %d calls %d: %v", len(issues), calls, err)
	}
	comments := []GitHubComment{
		{ID: 1, Body: GitHubJournalMarker + `{}`, User: GitHubActor{Login: "owner"}},
		{ID: 2, Body: GitHubJournalMarker + `{}`, User: GitHubActor{Login: "stranger"}},
		{ID: 3, Body: "ordinary comment", User: GitHubActor{Login: "owner"}},
	}
	trusted := TrustedJournalComments(comments, "OWNER")
	if len(trusted) != 1 || trusted[0].ID != 1 {
		t.Fatalf("trusted %#v", trusted)
	}
	if len(TrustedJournalComments(comments, "")) != 0 {
		t.Fatal("empty actor trusted")
	}
}

func TestGitHubPromptReadAndSaveCAS(t *testing.T) {
	writes := 0
	existing := true
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, input []byte) ([]byte, error) {
		if !strings.HasSuffix(path, "/.machinist/agents/triage.md") {
			t.Fatalf("path %s", path)
		}
		if method == "GET" {
			if !existing {
				return nil, errors.New("gh: Not Found (HTTP 404)")
			}
			return []byte(fmt.Sprintf(`{"sha":"old","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("Prompt {{machinist.prompt}}")))), nil
		}
		writes++
		var body map[string]string
		if err := json.Unmarshal(input, &body); err != nil {
			t.Fatal(err)
		}
		if body["sha"] != "old" {
			t.Fatalf("missing expected sha: %#v", body)
		}
		prompt, _ := base64.StdEncoding.DecodeString(body["content"])
		if string(prompt) != "New {{machinist.prompt}}" {
			t.Fatalf("wrong prompt %q", prompt)
		}
		return []byte(`{"content":{"sha":"new"}}`), nil
	}}
	prompt, sha, err := client.AgentPrompt(t.Context(), "org/repo", "triage")
	if err != nil || sha != "old" || prompt != "Prompt {{machinist.prompt}}" {
		t.Fatalf("%q %q %v", prompt, sha, err)
	}
	if _, err = client.SaveAgentPrompt(t.Context(), "org/repo", "triage", "New {{machinist.prompt}}", ""); !errors.Is(err, errAgentConflict) {
		t.Fatalf("stale save %v", err)
	}
	saved, err := client.SaveAgentPrompt(t.Context(), "org/repo", "triage", "New {{machinist.prompt}}", "old")
	if err != nil || saved != "new" || writes != 1 {
		t.Fatalf("saved %q writes %d err %v", saved, writes, err)
	}
	existing = false
	prompt, sha, err = client.AgentPrompt(t.Context(), "org/repo", "triage")
	if err != nil || sha != "" || !strings.Contains(prompt, "{{machinist.prompt}}") {
		t.Fatalf("fallback %q %q %v", prompt, sha, err)
	}
}

func TestGitHubPromptReadFailureNeverFallsBack(t *testing.T) {
	client := &ghFactoryClient{api: func(context.Context, string, string, []byte) ([]byte, error) {
		return nil, errors.New("HTTP 403 forbidden")
	}}
	if _, _, err := client.AgentPrompt(t.Context(), "org/repo", "triage"); err == nil {
		t.Fatal("permission failure hidden")
	}
	if _, err := client.SaveAgentPrompt(t.Context(), "org/repo", "triage", "New {{machinist.prompt}}", ""); err == nil {
		t.Fatal("read failure allowed write")
	}
	if _, _, err := client.AgentPrompt(t.Context(), "org/repo", "../escape"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestGitHubCommentsPaginationAndWritePayload(t *testing.T) {
	calls := 0
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, input []byte) ([]byte, error) {
		calls++
		if method == "POST" {
			var body map[string]string
			if err := json.Unmarshal(input, &body); err != nil {
				t.Fatal(err)
			}
			if body["body"] != GitHubJournalMarker+`{"kind":"task"}` {
				t.Fatalf("body %#v", body)
			}
			return []byte(`{"id":200,"user":{"login":"owner"}}`), nil
		}
		if strings.Contains(path, "page=2") {
			return []byte(`[]`), nil
		}
		return json.Marshal(make([]GitHubComment, 100))
	}}
	comments, err := client.Comments(t.Context(), "org/repo", 12)
	if err != nil || len(comments) != 100 || calls != 2 {
		t.Fatalf("comments %d calls %d err %v", len(comments), calls, err)
	}
	comment, err := client.PostComment(t.Context(), "org/repo", 12, GitHubJournalMarker+`{"kind":"task"}`)
	if err != nil || comment.ID != 200 {
		t.Fatalf("comment %#v err %v", comment, err)
	}
}

func TestGitHubCreateTaskEnsuresLabel(t *testing.T) {
	calls := []string{}
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, input []byte) ([]byte, error) {
		calls = append(calls, method+" "+path)
		switch {
		case method == "GET":
			return nil, errors.New("Not Found (HTTP 404)")
		case strings.HasSuffix(path, "/labels"):
			var label map[string]string
			if err := json.Unmarshal(input, &label); err != nil {
				t.Fatal(err)
			}
			if label["name"] != "machinist:task" {
				t.Fatalf("label %#v", label)
			}
			return []byte(`{}`), nil
		default:
			var issue struct {
				Title  string
				Body   string
				Labels []string
			}
			if err := json.Unmarshal(input, &issue); err != nil {
				t.Fatal(err)
			}
			if issue.Title != "Task" || issue.Body != "Body" || len(issue.Labels) != 1 {
				t.Fatalf("issue %#v", issue)
			}
			return []byte(`{"number":23,"title":"Task","html_url":"https://github.com/org/repo/issues/23"}`), nil
		}
	}}
	issue, err := client.CreateTask(t.Context(), "org/repo", "Task", "Body")
	if err != nil || issue.Number != 23 || len(calls) != 3 {
		t.Fatalf("issue %#v calls %#v err %v", issue, calls, err)
	}
}

func TestGitHubPromptCreateDoesNotSendSHA(t *testing.T) {
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, input []byte) ([]byte, error) {
		if method == "GET" {
			return nil, errors.New("HTTP 404")
		}
		var payload map[string]string
		if err := json.Unmarshal(input, &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["sha"]; ok {
			t.Fatal("create sent SHA")
		}
		return []byte(`{"content":{"sha":"created"}}`), nil
	}}
	sha, err := client.SaveAgentPrompt(t.Context(), "org/repo", "triage", "Task {{machinist.prompt}}", "")
	if err != nil || sha != "created" {
		t.Fatalf("%q %v", sha, err)
	}
}

func TestGitHubOutputBound(t *testing.T) {
	var output limitedGitHubBuffer
	if _, err := output.Write(make([]byte, 8<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("x")); err == nil {
		t.Fatal("response limit not enforced")
	}
}

func TestGitHubAdoptExistingTask(t *testing.T) {
	calls := []string{}
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, input []byte) ([]byte, error) {
		calls = append(calls, method+" "+path)
		if strings.HasSuffix(path, "/issues/42") {
			return []byte(`{"number":42,"state":"open","title":"Existing ticket"}`), nil
		}
		if method == "POST" {
			if !strings.HasSuffix(path, "/issues/42/labels") {
				t.Fatalf("unexpected mutation %s", path)
			}
			var labels struct{ Labels []string }
			if err := json.Unmarshal(input, &labels); err != nil {
				t.Fatal(err)
			}
			if len(labels.Labels) != 1 || labels.Labels[0] != "machinist:task" {
				t.Fatalf("labels %#v", labels)
			}
		}
		return []byte(`{}`), nil
	}}
	issue, err := client.GetTask(t.Context(), "org/repo", 42)
	if err != nil || issue.Number != 42 {
		t.Fatalf("%#v %v", issue, err)
	}
	if err = client.AdoptTask(t.Context(), "org/repo", 42); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls %#v", calls)
	}
	client.api = func(context.Context, string, string, []byte) ([]byte, error) {
		return []byte(`{"number":42,"pull_request":{"url":"pull"}}`), nil
	}
	if _, err = client.GetTask(t.Context(), "org/repo", 42); err == nil {
		t.Fatal("PR adopted as issue")
	}
}
