package managedworker

import (
	"context"
	"fmt"
	"github.com/owainlewis/machinist/internal/githubauth"
	"os"
	"regexp"
	"strings"
)

type commandEnvironmentKey struct{}

// GitHubEnvironment supplies process-scoped GitHub App credentials.
func GitHubEnvironment(token, repository string) ([]string, error) {
	return githubauth.GitHubEnvironment(token, repository)
}

func commandEnvironment(ctx context.Context) []string {
	overrides, _ := ctx.Value(commandEnvironmentKey{}).([]string)
	if len(overrides) == 0 {
		return nil
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_COMMON_DIR" || name == "GIT_INDEX_FILE" || name == "GIT_OBJECT_DIRECTORY" || name == "GIT_ALTERNATE_OBJECT_DIRECTORIES" || name == "GIT_CONFIG_PARAMETERS" || name == "GIT_CONFIG_COUNT" || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		replaced := false
		for _, override := range overrides {
			key, _, _ := strings.Cut(override, "=")
			if key == name {
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, entry)
		}
	}
	return append(env, overrides...)
}

// SetGitHubTokenProvider configures the in-process worker before Run starts.
// Separate workers can retain their own host credentials by leaving it unset.
func (w *Worker) SetGitHubTokenProvider(provider func(context.Context, string) (string, error)) {
	w.githubTokenProvider = provider
}

// validateGitHubConnection runs read-only checks before starting the agent.
func validateGitHubConnection(command evidenceCommand, repository string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) {
		return fmt.Errorf("invalid expected GitHub repository")
	}
	remote, actual, err := factoryRemote(command)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, repository) {
		return fmt.Errorf("registered checkout belongs to a different GitHub repository")
	}
	if _, err := command("git", "ls-remote", remote, "HEAD"); err != nil {
		return fmt.Errorf("GitHub Git connection check failed: %w", err)
	}
	permissions, err := command("gh", "api", "repos/"+repository, "--jq", ".permissions.push")
	if err != nil {
		return fmt.Errorf("GitHub API connection check failed: %w", err)
	}
	if strings.TrimSpace(string(permissions)) != "true" {
		return fmt.Errorf("GitHub credentials require write access to the selected repository")
	}
	return nil
}
