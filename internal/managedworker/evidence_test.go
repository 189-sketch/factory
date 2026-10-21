package managedworker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestImplementationEvidenceRejectsUnverifiedClaims(t *testing.T) {
	sha := strings.Repeat("a", 40)
	base := map[string]string{
		"git rev-parse HEAD":                                 sha,
		"git symbolic-ref --short HEAD":                      "codex/task",
		"git status --porcelain":                             "",
		"git remote":                                         "origin",
		"git config --get remote.origin.url":                 "git@github.com:owner/project.git",
		"git ls-remote --heads origin refs/heads/codex/task": sha + "\trefs/heads/codex/task",
		"gh api repos/owner/project/pulls/12":                `{"state":"open","head":{"ref":"codex/task","sha":"` + sha + `","repo":{"full_name":"owner/project"}},"base":{"repo":{"full_name":"owner/project"}}}`,
	}
	for _, test := range []struct {
		name, key, value string
		fail             bool
	}{
		{name: "verified"},
		{name: "wrong readiness", fail: true},
		{"wrong local head", "git rev-parse HEAD", strings.Repeat("b", 40), true},
		{"wrong local branch", "git symbolic-ref --short HEAD", "main", true},
		{"uncommitted work", "git status --porcelain", " M file", true},
		{"stale pushed head", "git ls-remote --heads origin refs/heads/codex/task", strings.Repeat("b", 40) + "\trefs/heads/codex/task", true},
		{"missing remote branch", "git ls-remote --heads origin refs/heads/codex/task", "", true},
		{"wrong PR head", "gh api repos/owner/project/pulls/12", `{"state":"open","head":{"ref":"other","sha":"` + sha + `"}}`, true},
		{"non GitHub remote", "git config --get remote.origin.url", "https://example.com/owner/project", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			activeRole := "implement"
			command := func(program string, args ...string) ([]byte, error) {
				key := program + " " + strings.Join(args, " ")
				if key == "git cat-file -t "+sha+":docs/spec.md" {
					return []byte("blob"), nil
				}
				value, ok := base[key]
				if key == test.key {
					value = test.value
				}
				if !ok {
					return nil, fmt.Errorf("unexpected command %s", key)
				}
				if key == "gh api repos/owner/project/pulls/12" {
					var metadata map[string]any
					if err := json.Unmarshal([]byte(value), &metadata); err != nil {
						return nil, err
					}
					metadata["draft"] = activeRole == "spec"
					if test.name == "wrong readiness" {
						metadata["draft"] = activeRole != "spec"
					}
					body, err := json.Marshal(metadata)
					return body, err
				}
				return []byte(value), nil
			}
			result := &protocol.FactoryResult{Version: 1, Role: "implement", PRURL: "https://github.com/owner/project/pull/12", HeadSHA: sha, Branch: "codex/task"}
			result.Report = "Delivery evidence verified."
			for _, role := range []string{"implement", "primary", "spec"} {
				result.Role = role
				activeRole = role
				if role == "spec" {
					if err := json.Unmarshal([]byte(`{"spec_url":"https://github.com/owner/project/blob/`+sha+`/docs/spec.md"}`), result); err != nil {
						t.Fatal(err)
					}
				}
				err := validateImplementationEvidence(command, result)
				if (err != nil) != test.fail {
					t.Fatalf("role=%s error=%v", role, err)
				}
			}
		})
	}
}

func TestGitHubRepositoryRemoteFormats(t *testing.T) {
	for _, remote := range []string{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git", "ssh://git@github.com/owner/repo.git"} {
		got, err := githubRepository(remote)
		if err != nil || got != "owner/repo" {
			t.Fatalf("%s: %s %v", remote, got, err)
		}
	}
}

func TestSourceRepositoryMatchesCheckout(t *testing.T) {
	command := func(program string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "remote":
			return []byte("origin"), nil
		case "config --get remote.origin.url":
			return []byte("https://github.com/owner/project.git"), nil
		default:
			return nil, fmt.Errorf("unexpected command")
		}
	}
	for _, test := range []struct {
		url  string
		fail bool
	}{
		{"https://github.com/owner/project/issues/12", false},
		{"https://github.com/OWNER/PROJECT/pull/3", false},
		{"https://github.com/other/project/issues/12", true},
		{"https://github.com/other/project/pull/3", true},
		{"https://docs.example.com/guide", false},
		{"https://github.com/other/project/blob/main/README.md", false},
	} {
		if err := validateSourceRepository(command, test.url); (err != nil) != test.fail {
			t.Errorf("%s: %v", test.url, err)
		}
	}
}

func TestReviewEvidenceContainsVerifiedDiffAndChecks(t *testing.T) {
	sha := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	factory := &protocol.FactoryContext{Role: "review", PRURL: "https://github.com/owner/project/pull/12", Branch: "codex/task", HeadSHA: sha}
	for _, test := range []struct {
		name, diff, remoteSHA string
		fail                  bool
	}{
		{"complete", "+fixed bug", sha, false},
		{"oversized diff", strings.Repeat("x", 129<<10), sha, true},
		{"stale head", "+fixed bug", base, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := func(program string, args ...string) ([]byte, error) {
				key := program + " " + strings.Join(args, " ")
				switch key {
				case "git remote":
					return []byte("origin"), nil
				case "git config --get remote.origin.url":
					return []byte("git@github.com:owner/project.git"), nil
				case "git ls-remote --heads origin refs/heads/codex/task":
					return []byte(test.remoteSHA + " refs/heads/codex/task"), nil
				case "gh api repos/owner/project/pulls/12":
					return []byte(`{"state":"open","head":{"sha":"` + sha + `","ref":"codex/task","repo":{"full_name":"owner/project"}},"base":{"sha":"` + base + `","ref":"main","repo":{"full_name":"owner/project"}}}`), nil
				case "git cat-file -e " + base + "^{commit}":
					return nil, nil
				case "git diff --no-ext-diff --no-textconv --find-renames " + base + "..." + sha + " --":
					return []byte(test.diff), nil
				case "gh api --paginate --slurp repos/owner/project/commits/" + sha + "/check-runs?per_page=100":
					return []byte(`[{"check_runs":[{"name":"tests","conclusion":"success"}]}]`), nil
				case "gh api repos/owner/project/commits/" + sha + "/status":
					return []byte(`{"state":"success"}`), nil
				default:
					return nil, fmt.Errorf("unexpected evidence command %s", key)
				}
			}
			evidence, err := collectReviewEvidence(command, factory)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
			if !test.fail {
				for _, want := range []string{sha, base, "+fixed bug", "tests", "success", "untrusted evidence"} {
					if !strings.Contains(evidence, want) {
						t.Errorf("missing %s", want)
					}
				}
			}
		})
	}
}

func TestSpecEvidenceRequiresActualDocumentAtPushedHead(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, test := range []struct {
		name, url, kind string
		missing, fail   bool
	}{
		{"valid", "https://github.com/owner/project/blob/" + sha + "/docs/spec.md", "blob", false, false},
		{"wrong repository", "https://github.com/other/project/blob/" + sha + "/docs/spec.md", "blob", false, true},
		{"wrong head", "https://github.com/owner/project/blob/" + strings.Repeat("b", 40) + "/docs/spec.md", "blob", false, true},
		{"branch link", "https://github.com/owner/project/blob/main/docs/spec.md", "blob", false, true},
		{"pull request link", "https://github.com/owner/project/pull/12", "blob", false, true},
		{"missing path", "https://github.com/owner/project/blob/" + sha + "/docs/missing.md", "", true, true},
		{"directory", "https://github.com/owner/project/blob/" + sha + "/docs", "tree", false, true},
		{"traversal", "https://github.com/owner/project/blob/" + sha + "/docs/../spec.md", "blob", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := func(program string, args ...string) ([]byte, error) {
				key := program + " " + strings.Join(args, " ")
				switch key {
				case "git rev-parse HEAD":
					return []byte(sha), nil
				case "git symbolic-ref --short HEAD":
					return []byte("codex/task"), nil
				case "git status --porcelain":
					return nil, nil
				case "git remote":
					return []byte("origin"), nil
				case "git config --get remote.origin.url":
					return []byte("git@github.com:owner/project.git"), nil
				case "git ls-remote --heads origin refs/heads/codex/task":
					return []byte(sha + " refs/heads/codex/task"), nil
				case "gh api repos/owner/project/pulls/12":
					return []byte(`{"state":"open","draft":true,"head":{"sha":"` + sha + `","ref":"codex/task","repo":{"full_name":"owner/project"}},"base":{"repo":{"full_name":"owner/project"}}}`), nil
				}
				if strings.HasPrefix(key, "git cat-file -t "+sha+":") {
					if test.missing {
						return nil, fmt.Errorf("missing blob")
					}
					return []byte(test.kind), nil
				}
				return nil, fmt.Errorf("unexpected command %s", key)
			}
			result := &protocol.FactoryResult{Version: 1, Role: "spec", Report: "Specification", SpecURL: test.url, PRURL: "https://github.com/owner/project/pull/12", HeadSHA: sha, Branch: "codex/task"}
			err := validateImplementationEvidence(command, result)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAnswerWorkspaceRejectsFilesAndReportsHead(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		command := func(program string, args ...string) ([]byte, error) {
			if args[0] == "rev-parse" {
				return []byte("base-head\n"), nil
			}
			if dirty {
				return []byte("?? requested-report.html\n"), nil
			}
			return nil, nil
		}
		head, err := answerWorkspaceState(command)
		if dirty && err == nil {
			t.Fatal("Answer accepted a requested file")
		}
		if !dirty && (err != nil || head != "base-head") {
			t.Fatalf("clean answer: %q %v", head, err)
		}
	}
}
