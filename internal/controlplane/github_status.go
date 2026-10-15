package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const githubTaskStatusMarker = "<!-- machinist:status -->"

// Serialize list-before-create in this foreman. GitHub's comment API does not
// provide an atomic create-if-absent operation across independent foremen.
var githubTaskStatusMu sync.Mutex

func (g *ghFactoryClient) UpsertTaskStatus(ctx context.Context, repo string, number int, body string) error {
	githubTaskStatusMu.Lock()
	defer githubTaskStatusMu.Unlock()
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return err
	}
	if number <= 0 {
		return errors.New("issue number must be positive")
	}
	if len(body) > 16000 {
		return errors.New("task status exceeds readable comment limit")
	}
	if strings.Contains(body, "<!-- machinist:") {
		return errors.New("task status contains an internal marker")
	}
	actor, err := g.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if actor == "" {
		return errors.New("GitHub account unavailable")
	}
	var existing *GitHubComment
	for page := 1; ; page++ {
		if page > 100 {
			return errors.New("too many issue comments to safely find task status")
		}
		raw, err := g.api(ctx, "GET", fmt.Sprintf("%s/issues/%d/comments?per_page=100&page=%d", root, number, page), nil)
		if err != nil {
			return err
		}
		var batch []GitHubComment
		if err = json.Unmarshal(raw, &batch); err != nil {
			return err
		}
		for _, comment := range batch {
			if comment.User.Login == actor && strings.HasPrefix(comment.Body, githubTaskStatusMarker+"\n") && comment.ID > 0 && (existing == nil || comment.ID < existing.ID) {
				copy := comment
				existing = &copy
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	text := githubTaskStatusMarker + "\n" + strings.TrimSpace(body)
	if existing != nil && existing.Body == text {
		return nil
	}
	input, _ := json.Marshal(map[string]string{"body": text})
	if existing != nil {
		_, err = g.api(ctx, "PATCH", fmt.Sprintf("%s/issues/comments/%d", root, existing.ID), input)
	} else {
		_, err = g.api(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", root, number), input)
	}
	// Never retry an uncertain POST here. The next attempt lists comments again,
	// so a successful write with a lost response is reconciled instead of copied.
	return err
}
