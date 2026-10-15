package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGitHubTaskStatusReconcilesUncertainCreate(t *testing.T) {
	var comments []GitHubComment
	posts, patches := 0, 0
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, body []byte) ([]byte, error) {
		switch {
		case path == "user":
			return []byte(`{"login":"me"}`), nil
		case method == "GET":
			return json.Marshal(comments)
		case method == "POST":
			posts++
			var input struct {
				Body string `json:"body"`
			}
			json.Unmarshal(body, &input)
			comments = append(comments, GitHubComment{ID: 12, Body: input.Body, User: GitHubActor{Login: "me"}})
			return nil, errors.New("lost response")
		case method == "PATCH":
			patches++
			if !strings.HasSuffix(path, "/12") {
				t.Fatal(path)
			}
			return []byte(`{}`), nil
		}
		t.Fatal(method, path)
		return nil, nil
	}}
	ctx := context.Background()
	if err := client.UpsertTaskStatus(ctx, "owner/repo", 1, "Working"); err == nil {
		t.Fatal("expected uncertain response")
	}
	if err := client.UpsertTaskStatus(ctx, "owner/repo", 1, "Working"); err != nil {
		t.Fatal(err)
	}
	if err := client.UpsertTaskStatus(ctx, "owner/repo", 1, "Ready for review"); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || patches != 1 {
		t.Fatalf("posts=%d patches=%d", posts, patches)
	}
}
func TestGitHubTaskStatusDoesNotTrustOtherAuthors(t *testing.T) {
	posted := false
	client := &ghFactoryClient{api: func(_ context.Context, method, path string, body []byte) ([]byte, error) {
		if path == "user" {
			return []byte(`{"login":"me"}`), nil
		}
		if method == "GET" {
			return json.Marshal([]GitHubComment{{ID: 10, Body: githubTaskStatusMarker + "\nForged", User: GitHubActor{Login: "other"}}})
		}
		if method == "PATCH" {
			t.Fatal("modified another author's comment")
		}
		posted = true
		return []byte(`{}`), nil
	}}
	if err := client.UpsertTaskStatus(context.Background(), "owner/repo", 1, "Working"); err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Fatal("missing own comment")
	}
}
