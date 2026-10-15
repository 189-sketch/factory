package managedworker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestCandidatePRDescriptionKeepsInternalHandoffPrivate(t *testing.T) {
	result := &protocol.StepResult{
		Summary: "Added the Kubernetes delivery guide; documentation checks passed.",
		Factory: &protocol.FactoryResult{Report: "Private handoff: internal stage state, prompt context and unpublished findings"},
	}
	body := candidatePRBody(result)
	if body != result.Summary || strings.Contains(body, "Private handoff") {
		t.Fatalf("internal handoff leaked into public PR description: %q", body)
	}
	result.Factory.PRDescription = "## Why\nOperators need a runner guide.\n## What\nDescribe Kubernetes delivery.\n## Verification\nDocumentation checks passed."
	if candidatePRBody(result) != result.Factory.PRDescription {
		t.Fatal("structured public PR description was lost")
	}
	called := false
	command := func(program string, args ...string) ([]byte, error) {
		called = true
		if program != "gh" || strings.Join(args[:5], " ") != "api --method PATCH repos/owner/repo/pulls/12 --input" {
			t.Fatalf("unexpected publication target: %s %v", program, args)
		}
		data, err := os.ReadFile(args[5])
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]string
		if err = json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["body"] != result.Factory.PRDescription || strings.Contains(string(data), "Private handoff") {
			t.Fatal("public metadata update lost the description or leaked private handoff")
		}
		return nil, nil
	}
	if err := updateCandidateDescription(command, t.TempDir(), "owner/repo", "https://github.com/owner/repo/pull/12", candidatePRBody(result)); err != nil || !called {
		t.Fatalf("existing PR description not updated: %v", err)
	}
	called = false
	if err := updateCandidateDescription(command, t.TempDir(), "owner/repo", "https://github.com/another/repo/pull/12", "body"); err == nil || called {
		t.Fatal("description update crossed repository boundary")
	}
}

func TestRecoverPublicationDoesNotForgetLostPRResponse(t *testing.T) {
	base := "0123456789012345678901234567890123456789"
	branch := "codex/task-test"
	got, err := recoverPublication(base+"\trefs/heads/"+branch+"\n", base, branch, "")
	if err != nil || !got {
		t.Fatalf("lost-response publication not recovered: %v", err)
	}
	if _, err = recoverPublication(base+"\trefs/heads/"+branch, base, branch, "https://github.com/example/repo/pull/1"); err == nil {
		t.Fatal("no-change repair accepted")
	}
	if got, err = recoverPublication("", base, branch, ""); err != nil || got {
		t.Fatal("operational task became a PR")
	}
	if _, err = recoverPublication(base+"\trefs/heads/another", base, branch, ""); err == nil {
		t.Fatal("another branch accepted")
	}
}
