package runner

import (
	"context"
	"fmt"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWorkflowResultContract(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires sh")
	}
	for _, test := range []struct {
		name, script, outcome string
		fail                  bool
	}{
		{"complete", `input=$(cat); test "$input" = 'issue URL' || exit 9; test "$MACHINIST_JOB_ID" = job_test || exit 8; printf '%s' '{"outcome":"complete","summary":"PR created"}' > "$MACHINIST_STEP_RESULT_PATH"`, "complete", false},
		{"blocked", `cat >/dev/null; printf '%s' '{"outcome":"blocked","summary":"Need requirements"}' > "$MACHINIST_STEP_RESULT_PATH"`, "blocked", false},
		{"missing", `cat >/dev/null`, "", true},
		{"invalid", `cat >/dev/null; printf '%s' '{"outcome":"approved","summary":"ok"}' > "$MACHINIST_STEP_RESULT_PATH"`, "", true},
		{"nonzero overrides result", `cat >/dev/null; printf '%s' '{"outcome":"complete","summary":"ok"}' > "$MACHINIST_STEP_RESULT_PATH"; exit 1`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(t.Context(), Options{Workflow: true, JobID: "job_test", Command: config.ResolvedCommand{Name: "build", Executor: "script", Command: []string{shell, "-c", test.script}, Prompt: "issue URL", Timeout: time.Second}, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
			if (err != nil) != test.fail {
				t.Fatalf("result %#v error %v", result, err)
			}
			if !test.fail && (result.StepResult == nil || result.StepResult.Outcome != test.outcome) {
				t.Fatalf("result %#v", result)
			}
		})
	}
}

func TestFactoryPromptCarriesAuthoritativeContext(t *testing.T) {
	prompt := factoryPrompt(&protocol.FactoryContext{Role: "review", HeadSHA: "abc", PRURL: "https://github.com/a/b/pull/1", Branch: "codex/task"}, "/task/workspace")
	for _, want := range []string{"review", "abc", "https://github.com/a/b/pull/1", "codex/task", "/task/workspace", "Never merge", "reports, documents, HTML pages", "delivered through the task PR", "takes precedence over an earlier handoff proposing artifact-only delivery", "no requested file or code change"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestReadOnlyReviewReturnsResultWithoutWritingFile(t *testing.T) {
	const output = `{"type":"item.completed","item":{"type":"agent_message","text":"{\"outcome\":\"complete\",\"summary\":\"Reviewed\",\"factory\":{\"version\":1,\"role\":\"review\",\"verdict\":\"accepted\"}}"}}` + "\n"
	agent := codexJSONAgentCommand(t, "codex-review", "codex", false, `input=$(cat); case "$input" in *'{{task.unknown}}'*) ;; *) exit 7;; esac; printf '%s' '`+output+`'`, 5*time.Second)
	result, err := Execute(t.Context(), Options{Factory: &protocol.FactoryContext{Role: "review"}, PrepareInputs: func(context.Context, string) (map[string]string, error) {
		return nil, fmt.Errorf("factory must not load local artifact handoffs")
	}, ReviewEvidence: "literal diff {{task.unknown}}", Task: &protocol.Task{Spec: "Review this change"}, Workflow: true, JobID: "task", Command: agent, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.StepResult == nil || result.StepResult.Factory == nil || result.StepResult.Factory.Verdict != "accepted" {
		t.Fatalf("result %#v", result.StepResult)
	}
}
