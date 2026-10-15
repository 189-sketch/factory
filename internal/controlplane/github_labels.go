package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

type factoryLabelClient interface {
	RepositoryLabels(context.Context, string) ([]string, error)
	SyncTaskLabels(context.Context, string, int, []string, string) error
}

var githubLabelsMu sync.Mutex
var routingLabels = map[string]string{
	"machinist:needs-spec": "Design and approval needed before implementation",
	"machinist:ready":      "Ready for or undergoing implementation and review",
	"machinist:blocked":    "Machinist needs input or a prerequisite",
}

func factoryRoutingLabel(s *GitHubFactoryState) string {
	switch s.Job.State {
	case "blocked", "awaiting_approval", "interrupted":
		return "machinist:blocked"
	case "succeeded", "failed", "cancelled", "awaiting_merge":
		return ""
	}
	if s.Role == "spec" {
		return "machinist:needs-spec"
	}
	if s.Role == "implement" || s.Role == "review" {
		return "machinist:ready"
	}
	return ""
}
func (g *ghFactoryClient) labelNames(ctx context.Context, path string) ([]string, error) {
	names := []string{}
	for page := 1; page <= 100; page++ {
		raw, err := g.api(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil)
		if err != nil {
			return nil, err
		}
		var batch []struct {
			Name string `json:"name"`
		}
		if err = json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		for _, label := range batch {
			names = append(names, label.Name)
		}
		if len(batch) < 100 {
			return names, nil
		}
	}
	return nil, errors.New("too many labels to reconcile safely")
}
func (g *ghFactoryClient) RepositoryLabels(ctx context.Context, repo string) ([]string, error) {
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return nil, err
	}
	return g.labelNames(ctx, root+"/labels")
}

// Only the three explicit routing labels are owned by Machinist. All other
// labels, including other machinist:* names, are preserved. Never replace the
// entire issue label set: humans may edit it while reconciliation runs.
func (g *ghFactoryClient) SyncTaskLabels(ctx context.Context, repo string, number int, suggestions []string, route string) error {
	if number <= 0 {
		return errors.New("issue number must be positive")
	}
	if route != "" {
		if _, ok := routingLabels[route]; !ok {
			return errors.New("invalid routing label")
		}
	}
	githubLabelsMu.Lock()
	defer githubLabelsMu.Unlock()
	root, err := githubRepositoryPath(repo)
	if err != nil {
		return err
	}
	catalog, err := g.RepositoryLabels(ctx, repo)
	if err != nil {
		return err
	}
	available := map[string]string{}
	for _, name := range catalog {
		available[strings.ToLower(name)] = name
	}
	current, err := g.labelNames(ctx, fmt.Sprintf("%s/issues/%d/labels", root, number))
	if err != nil {
		return err
	}
	present := map[string]string{}
	for _, name := range current {
		present[strings.ToLower(name)] = name
	}
	add := []string{}
	seen := map[string]bool{}
	for _, name := range suggestions {
		key := strings.ToLower(name)
		canonical, ok := available[key]
		if !ok || strings.HasPrefix(key, "machinist:") || seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := present[key]; !ok {
			add = append(add, canonical)
		}
	}
	if route != "" {
		canonical, ok := available[route]
		if !ok {
			body, _ := json.Marshal(map[string]string{"name": route, "color": "7057ff", "description": routingLabels[route]})
			if _, err = g.api(ctx, "POST", root+"/labels", body); err != nil {
				// A concurrent foreman may have created it, or a response may have been lost.
				catalog, checkErr := g.RepositoryLabels(ctx, repo)
				found := false
				for _, name := range catalog {
					if strings.EqualFold(name, route) {
						canonical = name
						found = true
					}
				}
				if checkErr != nil || !found {
					return err
				}
			}
			if canonical == "" {
				canonical = route
			}
		}
		if _, ok := present[route]; !ok {
			add = append(add, canonical)
		}
	}
	issuePath := fmt.Sprintf("%s/issues/%d/labels", root, number)
	if len(add) > 0 {
		body, _ := json.Marshal(map[string]any{"labels": add})
		if _, err = g.api(ctx, "POST", issuePath, body); err != nil {
			return err
		}
	}
	for _, name := range current {
		key := strings.ToLower(name)
		if _, owned := routingLabels[key]; owned && key != route {
			if _, err = g.api(ctx, "DELETE", issuePath+"/"+url.PathEscape(name), nil); err != nil {
				return err
			}
		}
	}
	return nil
}
