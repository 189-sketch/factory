package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWorkerRunInjectsInputIntoNamedPrompt(t *testing.T) {
	repository := newCLIRepository(t)
	workerConfig := writeCLIConfig(t, "success")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
		"--prompt=fix issue 123",
		"--repo=" + repository,
		"--config=" + workerConfig,
	}, strings.NewReader("ignored"), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stdout.String() != "configured plan prompt\n\nPrompt:\nfix issue 123\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "succeeded; events:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunExecutesOneAgent(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"run",
		"--agent=plan",
		"--prompt=issue 42",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 || stdout.String() != "configured plan prompt\n\nPrompt:\nissue 42\n" {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunPipelineExecutesIndependentAgentsInOrder(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"run",
		"--pipeline=code",
		"--prompt=issue 42",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := "configured plan prompt\n\nPrompt:\nissue 42\n" +
		"configured build prompt\n\nPrompt:\nissue 42\n" +
		"configured verify prompt\n\nPrompt:\nissue 42\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	for _, message := range []string{"agent 1/3 plan", "agent 2/3 build", "agent 3/3 verify"} {
		if !strings.Contains(stderr.String(), message) {
			t.Fatalf("stderr does not contain %q: %q", message, stderr.String())
		}
	}
	if strings.Count(stderr.String(), "succeeded; events:") != 3 {
		t.Fatalf("pipeline did not persist three independent runs: %q", stderr.String())
	}
}

func TestRunPipelineStopsAfterFailedAgent(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"run",
		"--pipeline=code",
		"--prompt=issue 42",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "fail-build"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 9 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "configured plan prompt") || strings.Contains(stdout.String(), "configured verify prompt") {
		t.Fatalf("pipeline output = %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "agent 3/3 verify") {
		t.Fatalf("pipeline started verify after build failed: %q", stderr.String())
	}
}

func TestRunRequiresExactlyOneSelection(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--prompt=issue 42"},
		{"run", "--agent=plan", "--pipeline=code", "--prompt=issue 42"},
	} {
		var stderr bytes.Buffer
		if exitCode := Execute(t.Context(), args, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 {
			t.Fatalf("args = %#v, exit code = %d, stderr = %q", args, exitCode, stderr.String())
		}
	}
}

func TestWorkerRunSelectsDifferentAgentPrompts(t *testing.T) {
	repository := newCLIRepository(t)
	workerConfig := writeCLIConfig(t, "success")
	for _, test := range []struct {
		agent string
		want  string
	}{
		{agent: "plan", want: "configured plan prompt\n"},
		{agent: "review", want: "configured review prompt\n"},
	} {
		t.Run(test.agent, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{
				"worker", "run",
				"--agent=" + test.agent,
				"--prompt=check ticket",
				"--repo=" + repository,
				"--config=" + workerConfig,
			}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 0 || stdout.String() != test.want+"\nPrompt:\ncheck ticket\n" {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkerRunRejectsPositionalPrompt(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "old task", "--agent=plan", "--prompt=new task"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown command") && !strings.Contains(stderr.String(), "accepts 0 arg") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRequiresPrompt(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "--agent=plan"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "required flag(s) \"prompt\"") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRejectsLegacyTaskFlag(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "--agent=plan", "--task=issue 42"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown flag: --task") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunRejectsEmptyPrompt(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
		"--prompt=   ",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "prompt is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunDoesNotEvaluatePromptAsTemplateOrShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "not-run")
	prompt := "fix $(touch " + marker + ") and preserve {{factory.prompt}}\nsecond line"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
		"--prompt=" + prompt,
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "success"),
	}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	want := "configured plan prompt\n\nPrompt:\n" + prompt + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prompt text was evaluated as a shell command: %v", err)
	}
}

func TestWorkerRunReturnsAgentExitStatus(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
		"--prompt=fail this task",
		"--repo=" + newCLIRepository(t),
		"--config=" + writeCLIConfig(t, "fail"),
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 9 || !strings.Contains(stderr.String(), "status 9") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunReturnsRuntimeFailureStatus(t *testing.T) {
	workerConfig := writeCLIConfig(t, "success")
	blockedDataDirectory := filepath.Join(filepath.Dir(workerConfig), "blocked-data")
	if err := os.WriteFile(blockedDataDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerBody := "data_directory = " + strconv.Quote(blockedDataDirectory) + "\n" +
		"\n" +
		"[executors.default]\n" +
		"command = [\"/bin/sh\", \"-c\", \"cat\"]\n"
	if err := os.WriteFile(workerConfig, []byte(workerBody), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
		"--prompt=exercise runtime failure",
		"--repo=" + newCLIRepository(t),
		"--config=" + workerConfig,
	}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 1 || !strings.Contains(stderr.String(), "create run directory") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := Execute(t.Context(), []string{"version"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, "1.2.3")
	if exitCode != 0 || stdout.String() != "1.2.3\n" {
		t.Fatalf("exit code = %d, stdout = %q", exitCode, stdout.String())
	}
}

func writeCLIConfig(t *testing.T, mode string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("configured plan prompt\n\nPrompt:\n{{factory.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "review.md"), []byte("configured review prompt\n\nPrompt:\n{{factory.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "build.md"), []byte("configured build prompt\n\nPrompt:\n{{factory.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "verify.md"), []byte("configured verify prompt\n\nPrompt:\n{{factory.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(directory, "config.toml")
	script := "cat"
	if mode == "fail" {
		script = "exit 9"
	}
	buildScript := script
	if mode == "fail-build" {
		buildScript = "cat >/dev/null; exit 9"
	}
	definitionBody := "[agents.plan]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"plan.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[agents.review]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"review.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[agents.build]\n" +
		"executor = \"build\"\n" +
		"prompt_file = \"build.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[agents.verify]\n" +
		"executor = \"default\"\n" +
		"prompt_file = \"verify.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[pipelines.code]\n" +
		"agents = [\"plan\", \"build\", \"verify\"]\n"
	if err := os.WriteFile(definition, []byte(definitionBody), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(directory, "worker.toml")
	workerBody := "data_directory = \"" + filepath.ToSlash(filepath.Join(directory, "data")) + "\"\n" +
		"\n" +
		"[executors.default]\n" +
		"command = [\"/bin/sh\", \"-c\", " + strconv.Quote(script) + "]\n\n" +
		"[executors.build]\n" +
		"command = [\"/bin/sh\", \"-c\", " + strconv.Quote(buildScript) + "]\n"
	if err := os.WriteFile(worker, []byte(workerBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return worker
}

func newCLIRepository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	command := exec.Command("git", "init", "--quiet", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
