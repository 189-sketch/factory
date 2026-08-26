package cli

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	factoryexamples "github.com/owainlewis/factory-v2/examples"
	"github.com/owainlewis/factory-v2/internal/config"
)

func TestInitInstallsCompleteEditableDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	directory := filepath.Join(home, ".factory")
	wantFiles := []string{
		"agents/audit.md",
		"agents/foreman.md",
		"config.toml",
		"server/worker.token",
		"worker.toml",
	}
	if got := regularFiles(t, directory); strings.Join(got, "\n") != strings.Join(wantFiles, "\n") {
		t.Fatalf("installed files = %#v, want %#v", got, wantFiles)
	}
	for _, name := range initialFiles {
		want, err := factoryexamples.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("installed %s does not match its default", name)
		}
	}
	tokenPath := filepath.Join(directory, "server", "worker.token")
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(token)))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("worker token is not 32 random bytes: %q, %v", token, err)
	}
	for _, path := range []string{filepath.Join(directory, "config.toml"), tokenPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode for %s = %o, want 600", path, info.Mode().Perm())
		}
	}

	definition := filepath.Join(directory, "config.toml")
	definitions, err := config.LoadDefinitions(definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Agents) != 2 || len(definitions.Pipelines) != 0 {
		t.Fatalf("installed definitions = agents %#v, pipelines %#v", definitions.Agents, definitions.Pipelines)
	}
	for _, name := range []string{"foreman", "audit"} {
		if _, err := config.LoadAgent(definition, name); err != nil {
			t.Fatalf("load installed agent %s: %v", name, err)
		}
	}
	if _, err := config.LoadWorker(filepath.Join(directory, "worker.toml")); err != nil {
		t.Fatalf("load installed worker: %v", err)
	}
	if !strings.Contains(stdout.String(), "created agents/audit.md") || !strings.Contains(stdout.String(), "Add repositories to worker.toml") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestInitKeepsExistingFilesAndRestoresMissingDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, "test"); exitCode != 0 {
		t.Fatalf("first init exit code = %d", exitCode)
	}
	directory := filepath.Join(home, ".factory")
	for name, body := range map[string]string{
		"config.toml":         "custom config\n",
		"worker.toml":         "custom worker\n",
		"agents/foreman.md":   "custom foreman\n",
		"agents/plan.md":      "old plan\n",
		"agents/build.md":     "old build\n",
		"agents/verify.md":    "old verify\n",
		"agents/custom.md":    "custom agent\n",
		"server/worker.token": "custom token\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auditPath := filepath.Join(directory, "agents", "audit.md")
	if err := os.Remove(auditPath); err != nil {
		t.Fatal(err)
	}
	preserved := regularFileContents(t, directory)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test"); exitCode != 0 {
		t.Fatalf("second init exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for name, want := range preserved {
		got, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read preserved %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("init changed existing file %s", name)
		}
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	wantAudit, err := factoryexamples.Files.ReadFile("agents/audit.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(audit, wantAudit) {
		t.Fatal("init failed to restore the missing audit default")
	}
	if !strings.Contains(stdout.String(), "kept agents/foreman.md") || !strings.Contains(stdout.String(), "created agents/audit.md") || !strings.Contains(stdout.String(), "kept server/worker.token") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func regularFileContents(t *testing.T, root string) map[string][]byte {
	t.Helper()
	contents := make(map[string][]byte)
	for _, name := range regularFiles(t, root) {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		contents[name] = body
	}
	return contents
}

func TestInitRejectsExistingNonFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(target, []byte("custom\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "broken symlink", setup: func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			directory := filepath.Join(home, ".factory")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, filepath.Join(directory, "config.toml"))

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "config.toml already exists and is not a regular file") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "configuration is ready") {
				t.Fatalf("init reported success for an unusable configuration: %q", stdout.String())
			}
		})
	}
}

func TestInitRejectsSymlinkedSetupDirectories(t *testing.T) {
	for _, name := range []string{".factory", ".factory/agents", ".factory/server"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			if err := os.Symlink(external, path); err != nil {
				t.Fatal(err)
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Execute(t.Context(), []string{"init"}, strings.NewReader(""), &stdout, &stderr, "test")
			if exitCode != 2 || !strings.Contains(stderr.String(), "already exists and is not a directory") {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "configuration is ready") {
				t.Fatalf("init reported success with symlinked setup directory: %q", stdout.String())
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("init wrote outside its setup directory: %#v", entries)
			}
		})
	}
}

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
