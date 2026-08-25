package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerResolvesRelativePathsFromConfig(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\ndefinition_file = \"factory.toml\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if worker.DataDirectory != filepath.Join(directory, "state") {
		t.Fatalf("data directory = %q", worker.DataDirectory)
	}
	definition, err := worker.ResolveDefinition("")
	if err != nil {
		t.Fatal(err)
	}
	if definition != filepath.Join(directory, "factory.toml") {
		t.Fatalf("definition = %q", definition)
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

func TestLoadAgentResolvesPromptAndHashesDefinition(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Inspect the repository for {{factory.task}}.\n")
	definition := filepath.Join(directory, "factory.toml")
	writeTestFile(t, definition, `[agents.plan]
command = ["agent", "run"]
prompt_file = "plan.md"
timeout = "45s"
`)

	agent, err := LoadAgent(definition, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "plan" || agent.Prompt != "Inspect the repository for {{factory.task}}.\n" {
		t.Fatalf("unexpected agent: %#v", agent)
	}
	if agent.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s", agent.Timeout)
	}
	if len(agent.Command) != 2 || agent.Command[1] != "run" {
		t.Fatalf("command = %#v", agent.Command)
	}
	if len(agent.Hash) != 64 {
		t.Fatalf("hash = %q", agent.Hash)
	}
}

func TestRenderTaskReplacesEveryTaskParameterWithoutReevaluation(t *testing.T) {
	agent := ResolvedAgent{Prompt: "Before {{factory.task}} between {{factory.task}} after"}
	task := "fix {{factory.task}} and $(touch never)"
	rendered, err := RenderTask(agent, task)
	if err != nil {
		t.Fatal(err)
	}
	want := "Before " + task + " between " + task + " after"
	if rendered.Prompt != want {
		t.Fatalf("prompt = %q, want %q", rendered.Prompt, want)
	}
}

func TestRenderTaskRejectsEmptyAndOversizedTasks(t *testing.T) {
	agent := ResolvedAgent{Prompt: taskParameter}
	if _, err := RenderTask(agent, " \n\t"); err == nil || !strings.Contains(err.Error(), "task is required") {
		t.Fatalf("expected empty-task error, got %v", err)
	}
	if _, err := RenderTask(agent, strings.Repeat("x", maxTaskBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected task-size error, got %v", err)
	}
}

func TestRenderTaskRejectsOversizedRenderedPromptBeforeReplacement(t *testing.T) {
	agent := ResolvedAgent{
		Name:   "plan",
		Prompt: strings.Repeat(taskParameter, maxPromptBytes/len(taskParameter)),
	}
	if _, err := RenderTask(agent, strings.Repeat("x", maxTaskBytes)); err == nil || !strings.Contains(err.Error(), "rendered agent prompt exceeds") {
		t.Fatalf("expected rendered-size error, got %v", err)
	}
}

func TestLoadAgentRequiresTaskParameterAndRejectsUnsupportedFactoryParameter(t *testing.T) {
	for _, test := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "missing task", prompt: "Plan this ticket.\n", want: "must include {{factory.task}}"},
		{name: "unsupported parameter", prompt: "Plan {{factory.task}} in {{factory.repository}}.\n", want: "unsupported Factory parameter"},
		{name: "empty parameter", prompt: "Plan {{factory.task}} with {{factory.}}.\n", want: "unsupported Factory parameter"},
		{name: "unclosed parameter", prompt: "Plan {{factory.task}} with {{factory.repository.\n", want: "malformed Factory parameter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "plan.md"), test.prompt)
			definition := filepath.Join(directory, "factory.toml")
			writeTestFile(t, definition, "[agents.plan]\ncommand = [\"agent\"]\nprompt_file = \"plan.md\"\n")
			if _, err := LoadAgent(definition, "plan"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestLoadAgentRejectsMissingAndInvalidDefinitions(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan.\n")
	definition := filepath.Join(directory, "factory.toml")
	writeTestFile(t, definition, `[agents.plan]
command = []
prompt_file = "plan.md"
`)

	if _, err := LoadAgent(definition, "missing"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("expected missing-agent error, got %v", err)
	}
	if _, err := LoadAgent(definition, "plan"); err == nil || !strings.Contains(err.Error(), "non-empty command") {
		t.Fatalf("expected command error, got %v", err)
	}
}

func TestLoadPipelineResolvesAgentsInOrder(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{factory.task}}.\n")
	writeTestFile(t, filepath.Join(directory, "build.md"), "Build {{factory.task}}.\n")
	definition := filepath.Join(directory, "factory.toml")
	writeTestFile(t, definition, `[agents.plan]
command = ["agent", "plan"]
prompt_file = "plan.md"

[agents.build]
command = ["agent", "build"]
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
			definition := filepath.Join(t.TempDir(), "factory.toml")
			writeTestFile(t, definition, test.body)
			if _, err := LoadPipeline(definition, "code"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestExampleAgentDefinitionsLoad(t *testing.T) {
	definition := filepath.Join("..", "..", "examples", "factory.toml")
	for _, name := range []string{"plan", "build", "verify"} {
		t.Run(name, func(t *testing.T) {
			agent, err := LoadAgent(definition, name)
			if err != nil {
				t.Fatal(err)
			}
			if agent.Name != name {
				t.Fatalf("agent name = %q, want %q", agent.Name, name)
			}
			if !strings.Contains(agent.Prompt, taskParameter) {
				t.Fatalf("agent prompt does not contain %s", taskParameter)
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
