package githubauth

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GitHubEnvironment supplies process-scoped App credentials. No credentials are
// written to Git configuration, command arguments, or remote URLs.
func GitHubEnvironment(token, repository string) ([]string, error) {
	if token == "" || strings.ContainsAny(token, "\r\n\x00") || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) {
		return nil, fmt.Errorf("GitHub credentials require a token and owner/repository")
	}
	for _, part := range strings.Split(repository, "/") {
		if part == "." || part == ".." {
			return nil, fmt.Errorf("invalid GitHub repository")
		}
	}
	// A reset entry prevents falling back to broader credentials from user config.
	helper := `!f() { test "$1" = get || exit 0; protocol= host= path=; while IFS= read -r line; do case "$line" in protocol=*) protocol=${line#protocol=} ;; host=*) host=${line#host=} ;; path=*) path=${line#path=} ;; esac; done; test "$protocol" = https && test "$host" = github.com && { test "$path" = "$MACHINIST_GITHUB_REPOSITORY" || test "$path" = "$MACHINIST_GITHUB_REPOSITORY.git"; } || exit 1; printf 'username=x-access-token\npassword=%s\n' "$GH_TOKEN"; }; f`
	settings := [][2]string{
		{"credential.helper", ""}, {"credential.helper", helper}, {"credential.https://github.com.helper", ""}, {"credential.https://github.com.helper", helper}, {"credential.useHttpPath", "true"},
		{"http.extraHeader", ""}, {"http.https://github.com/.extraHeader", ""},
		{"url.https://github.com/.insteadOf", "git@github.com:"},
		{"url.https://github.com/.insteadOf", "ssh://git@github.com/"},
	}
	env := []string{"GH_TOKEN=" + token, "GITHUB_TOKEN=" + token, "GH_HOST=github.com", "MACHINIST_GITHUB_REPOSITORY=" + repository, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_CONFIG_COUNT=" + strconv.Itoa(len(settings))}
	for i, pair := range settings {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, pair[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, pair[1]))
	}
	return env, nil
}
