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

func TestWorkerRunExecutesNamedPromptWithoutTaskArgument(t *testing.T) {
	repository := newCLIRepository(t)
	workerConfig := writeCLIConfig(t, "success")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
		"--repo=" + repository,
		"--config=" + workerConfig,
	}, strings.NewReader("ignored"), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr.String())
	}
	if stdout.String() != "configured plan prompt\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "succeeded; events:") {
		t.Fatalf("stderr = %q", stderr.String())
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
				"--repo=" + repository,
				"--config=" + workerConfig,
			}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 0 || stdout.String() != test.want {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkerRunRejectsPositionalTask(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{"worker", "run", "old task", "--agent=plan"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test")
	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown command") && !strings.Contains(stderr.String(), "accepts 0 arg") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestWorkerRunReturnsAgentExitStatus(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
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
		"definition_file = \"factory.toml\"\n"
	if err := os.WriteFile(workerConfig, []byte(workerBody), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	exitCode := Execute(t.Context(), []string{
		"worker", "run",
		"--agent=plan",
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
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("configured plan prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "review.md"), []byte("configured review prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(directory, "factory.toml")
	script := "cat"
	if mode == "fail" {
		script = "exit 9"
	}
	definitionBody := "[agents.plan]\n" +
		"command = [\"/bin/sh\", \"-c\", " + strconv.Quote(script) + "]\n" +
		"prompt_file = \"plan.md\"\n" +
		"timeout = \"5s\"\n\n" +
		"[agents.review]\n" +
		"command = [\"/bin/sh\", \"-c\", " + strconv.Quote(script) + "]\n" +
		"prompt_file = \"review.md\"\n" +
		"timeout = \"5s\"\n"
	if err := os.WriteFile(definition, []byte(definitionBody), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(directory, "worker.toml")
	workerBody := "data_directory = \"" + filepath.ToSlash(filepath.Join(directory, "data")) + "\"\n" +
		"definition_file = \"factory.toml\"\n"
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
