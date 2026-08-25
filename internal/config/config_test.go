package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerResolvesRelativePathsFromConfig(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if worker.DataDirectory != filepath.Join(directory, "state") {
		t.Fatalf("data directory = %q", worker.DataDirectory)
	}
	definition, err := worker.ResolveFactoryConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if definition != filepath.Join(directory, "config.toml") {
		t.Fatalf("definition = %q", definition)
	}
}

func TestLoadWorkerDefaultsNameToHostname(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != hostname {
		t.Fatalf("worker name = %q, want hostname %q", worker.Name, hostname)
	}
}

func TestWorkerNameDefaultsReportHostnameFailure(t *testing.T) {
	want := errors.New("hostname unavailable")
	_, err := applyWorkerDefaultsWithHostname(Worker{DataDirectory: t.TempDir()}, func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "find machine hostname") {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkerExplicitNameDoesNotReadHostname(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{Name: " configured-worker ", DataDirectory: t.TempDir()}, func() (string, error) {
		t.Fatal("hostname lookup called for explicit worker name")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != "configured-worker" {
		t.Fatalf("worker name = %q", worker.Name)
	}
}

func TestLoadWorkerRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, "mystery = true\n")

	_, err := LoadWorker(path)
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadManagedWorkerResolvesMachineConfiguration(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "token"), "secret\n")
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, `name = "local"
data_directory = "state"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "token"

[executors.test]
command = ["agent", "run"]

[repositories.factory]
path = "repository"
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if repository, err := worker.ResolveRepository("factory"); err != nil || repository != filepath.Join(directory, "repository") {
		t.Fatalf("repository = %q, %v", repository, err)
	}
	if token, err := worker.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
	resolved, err := worker.ResolveAgent(ResolvedAgent{Executor: "test"})
	if err != nil || len(resolved.Command) != 2 || resolved.Command[1] != "run" {
		t.Fatalf("agent = %#v, %v", resolved, err)
	}
}

func TestLoadConfigCombinesServerAgentsAndPipelines(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "token"), "secret")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{factory.prompt}}.\n")
	writeTestFile(t, path, `[server]
database = "state/factory.db"
worker_token_file = "token"

[agents.plan]
executor = "test"
prompt_file = "plan.md"

[pipelines.code]
agents = ["plan"]
`)
	factoryConfig, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := factoryConfig.Server
	if server.Listen != "127.0.0.1:7331" || server.Database != filepath.Join(directory, "state", "factory.db") {
		t.Fatalf("server = %#v", server)
	}
	if factoryConfig.Path() != path || len(factoryConfig.Agents) != 1 || len(factoryConfig.Pipelines) != 1 {
		t.Fatalf("Factory config = %#v, path = %q", factoryConfig, factoryConfig.Path())
	}
	if token, err := server.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestLoadAgentResolvesPromptAndHashesDefinition(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Inspect the repository for {{factory.prompt}}.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[agents.plan]
executor = "test"
prompt_file = "plan.md"
timeout = "45s"
`)

	agent, err := LoadAgent(definition, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "plan" || agent.Prompt != "Inspect the repository for {{factory.prompt}}.\n" {
		t.Fatalf("unexpected agent: %#v", agent)
	}
	if agent.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s", agent.Timeout)
	}
	resolved, err := (Worker{Executors: map[string]Executor{"test": {Command: []string{"agent", "run"}}}}).ResolveAgent(agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Command) != 2 || resolved.Command[1] != "run" {
		t.Fatalf("command = %#v", resolved.Command)
	}
	if len(agent.Hash) != 64 {
		t.Fatalf("hash = %q", agent.Hash)
	}
}

func TestRenderPromptReplacesEveryPromptParameterWithoutReevaluation(t *testing.T) {
	agent := ResolvedAgent{Prompt: "Before {{factory.prompt}} between {{factory.prompt}} after"}
	prompt := "fix {{factory.prompt}} and $(touch never)"
	rendered, err := RenderPrompt(agent, prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := "Before " + prompt + " between " + prompt + " after"
	if rendered.Prompt != want {
		t.Fatalf("prompt = %q, want %q", rendered.Prompt, want)
	}
}

func TestRenderPromptRejectsEmptyAndOversizedPrompts(t *testing.T) {
	agent := ResolvedAgent{Prompt: promptParameter}
	if _, err := RenderPrompt(agent, " \n\t"); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("expected empty-prompt error, got %v", err)
	}
	if _, err := RenderPrompt(agent, strings.Repeat("x", maxInputPromptBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected prompt-size error, got %v", err)
	}
}

func TestRenderPromptRejectsOversizedRenderedPromptBeforeReplacement(t *testing.T) {
	agent := ResolvedAgent{
		Name:   "plan",
		Prompt: strings.Repeat(promptParameter, maxPromptBytes/len(promptParameter)),
	}
	if _, err := RenderPrompt(agent, strings.Repeat("x", maxInputPromptBytes)); err == nil || !strings.Contains(err.Error(), "rendered agent prompt exceeds") {
		t.Fatalf("expected rendered-size error, got %v", err)
	}
}

func TestLoadAgentRequiresPromptParameterAndRejectsUnsupportedFactoryParameter(t *testing.T) {
	for _, test := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "missing prompt", prompt: "Plan this ticket.\n", want: "must include {{factory.prompt}}"},
		{name: "legacy task parameter", prompt: "Plan {{factory.task}}.\n", want: "unsupported Factory parameter"},
		{name: "unsupported parameter", prompt: "Plan {{factory.prompt}} in {{factory.repository}}.\n", want: "unsupported Factory parameter"},
		{name: "empty parameter", prompt: "Plan {{factory.prompt}} with {{factory.}}.\n", want: "unsupported Factory parameter"},
		{name: "unclosed parameter", prompt: "Plan {{factory.prompt}} with {{factory.repository.\n", want: "malformed Factory parameter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "plan.md"), test.prompt)
			definition := filepath.Join(directory, "config.toml")
			writeTestFile(t, definition, "[agents.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\n")
			if _, err := LoadAgent(definition, "plan"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestLoadAgentRejectsMissingAndInvalidDefinitions(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[agents.plan]
prompt_file = "plan.md"
`)

	if _, err := LoadAgent(definition, "missing"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("expected missing-agent error, got %v", err)
	}
	if _, err := LoadAgent(definition, "plan"); err == nil || !strings.Contains(err.Error(), "must define executor") {
		t.Fatalf("expected executor error, got %v", err)
	}
}

func TestLoadPipelineResolvesAgentsInOrder(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{factory.prompt}}.\n")
	writeTestFile(t, filepath.Join(directory, "build.md"), "Build {{factory.prompt}}.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[agents.plan]
executor = "plan"
prompt_file = "plan.md"

[agents.build]
executor = "build"
prompt_file = "build.md"

[pipelines.code]
agents = ["plan", "build"]
`)

	agents, err := LoadPipeline(definition, "code")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "plan" || agents[1].Name != "build" {
		t.Fatalf("pipeline agents = %#v", agents)
	}
}

func TestLoadPipelineRejectsMissingEmptyAndUndefinedAgents(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing", body: "", want: `pipeline "code" is not defined`},
		{name: "empty", body: "[pipelines.code]\nagents = []\n", want: "must define at least one agent"},
		{name: "undefined agent", body: "[pipelines.code]\nagents = [\"missing\"]\n", want: `references undefined agent "missing"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := filepath.Join(t.TempDir(), "config.toml")
			writeTestFile(t, definition, test.body)
			if _, err := LoadPipeline(definition, "code"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestExampleAgentDefinitionsLoad(t *testing.T) {
	definition := filepath.Join("..", "..", "examples", "config.toml")
	for _, name := range []string{"plan", "build", "verify"} {
		t.Run(name, func(t *testing.T) {
			agent, err := LoadAgent(definition, name)
			if err != nil {
				t.Fatal(err)
			}
			if agent.Name != name {
				t.Fatalf("agent name = %q, want %q", agent.Name, name)
			}
			if !strings.Contains(agent.Prompt, promptParameter) {
				t.Fatalf("agent prompt does not contain %s", promptParameter)
			}
		})
	}
	agents, err := LoadPipeline(definition, "code")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 3 {
		t.Fatalf("example pipeline agents = %d, want 3", len(agents))
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
