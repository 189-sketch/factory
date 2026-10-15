package managedworker

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestGitHubCredentialProtocol(t *testing.T) {
	const token = "test-app-token-secret"
	env, err := GitHubEnvironment(token, "owner/repository")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_CONFIG_") && strings.Contains(entry, token) {
			t.Fatal("token embedded in config")
		}
	}
	ctx := context.WithValue(context.Background(), commandEnvironmentKey{}, env)
	for _, tc := range []struct {
		host, path string
		authorized bool
	}{
		{"github.com", "owner/repository", true}, {"github.com", "owner/repository.git", true},
		{"github.com", "owner/other", false}, {"example.com", "owner/repository", false},
	} {
		t.Run(tc.host+"/"+tc.path, func(t *testing.T) {
			cmd := exec.Command("git", "credential", "fill")
			cmd.Env = commandEnvironment(ctx)
			cmd.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\npath=" + tc.path + "\n\n")
			out, err := cmd.CombinedOutput()
			if tc.authorized {
				if err != nil || !strings.Contains(string(out), "password="+token) {
					t.Fatalf("credential fill failed: %v", err)
				}
			} else if err == nil || strings.Contains(string(out), token) {
				t.Fatal("credential released outside selected repository")
			}
		})
	}
	output, err := repositoryCommand(ctx, t.TempDir())("sh", "-c", `test "$GH_TOKEN" = test-app-token-secret && test "$GIT_TERMINAL_PROMPT" = 0`)
	if err != nil {
		t.Fatalf("host command environment not propagated: %v %s", err, output)
	}
}

func TestGitHubEnvironmentValidation(t *testing.T) {
	for _, repository := range []string{"owner", "owner/repo/extra", "../repo", "owner/repo\n"} {
		if _, err := GitHubEnvironment("token", repository); err == nil {
			t.Fatalf("accepted %q", repository)
		}
	}
	if _, err := GitHubEnvironment("token\nleak", "owner/repo"); err == nil {
		t.Fatal("accepted newline token")
	}
}

func TestConnectionCheckRejectsWrongRepositoryBeforeNetwork(t *testing.T) {
	calls := 0
	command := func(program string, args ...string) ([]byte, error) {
		calls++
		if program != "git" {
			t.Fatal("API called before checkout validation")
		}
		switch strings.Join(args, " ") {
		case "remote":
			return []byte("origin\n"), nil
		case "config --get remote.origin.url":
			return []byte("git@github.com:other/repository.git\n"), nil
		default:
			t.Fatal("network called before checkout validation")
		}
		return nil, nil
	}
	if err := validateGitHubConnection(command, "owner/repository"); err == nil {
		t.Fatal("wrong repository accepted")
	}
	if calls != 2 {
		t.Fatalf("unexpected calls: %d", calls)
	}
}

func TestConnectionCheckReadsGitAndGitHubWithoutWrites(t *testing.T) {
	calls := []string{}
	command := func(program string, args ...string) ([]byte, error) {
		call := program + " " + strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "git remote":
			return []byte("origin\n"), nil
		case "git config --get remote.origin.url":
			return []byte("git@github.com:owner/repository.git\n"), nil
		case "git ls-remote origin HEAD":
			return []byte("abc HEAD\n"), nil
		case "gh api repos/owner/repository --jq .permissions.push":
			return []byte("true\n"), nil
		default:
			t.Fatalf("unexpected operation %s", call)
		}
		return nil, nil
	}
	if err := validateGitHubConnection(command, "owner/repository"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("unexpected operations: %v", calls)
	}
}

func TestConnectionCheckRequiresWritePermission(t *testing.T) {
	for _, permission := range []string{"false", "null", "", "{}"} {
		command := func(program string, args ...string) ([]byte, error) {
			switch program + " " + strings.Join(args, " ") {
			case "git remote":
				return []byte("origin"), nil
			case "git config --get remote.origin.url":
				return []byte("https://github.com/owner/repository.git"), nil
			case "git ls-remote origin HEAD":
				return []byte("abc HEAD"), nil
			case "gh api repos/owner/repository --jq .permissions.push":
				return []byte(permission), nil
			}
			t.Fatal("unexpected operation")
			return nil, nil
		}
		if err := validateGitHubConnection(command, "owner/repository"); err == nil {
			t.Fatalf("accepted push permission %q", permission)
		}
	}
}
