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
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Inspect the repository.\n")
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
	if agent.Name != "plan" || agent.Prompt != "Inspect the repository.\n" {
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

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
