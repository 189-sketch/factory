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
	definition, err := worker.ResolveMachinistConfig("")
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

[repositories.machinist]
path = "repository"
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if repository, err := worker.ResolveRepository("machinist"); err != nil || repository != filepath.Join(directory, "repository") {
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
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{machinist.prompt}}.\n")
	writeTestFile(t, path, `[server]
database = "state/machinist.db"
worker_token_file = "token"

[agents.plan]
executor = "test"
prompt_file = "plan.md"

[pipelines.code]
agents = ["plan"]
`)
	machinistConfig, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := machinistConfig.Server
	if server.Listen != "127.0.0.1:7331" || server.Database != filepath.Join(directory, "state", "machinist.db") {
		t.Fatalf("server = %#v", server)
	}
	if machinistConfig.Path() != path || len(machinistConfig.Agents) != 1 || len(machinistConfig.Pipelines) != 1 {
		t.Fatalf("Machinist config = %#v, path = %q", machinistConfig, machinistConfig.Path())
	}
	if token, err := server.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestLoadAgentResolvesPromptAndHashesDefinition(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Inspect the repository for {{machinist.prompt}}.\n")
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
	if agent.Name != "plan" || agent.Prompt != "Inspect the repository for {{machinist.prompt}}.\n" {
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

func TestResolveAgentModelUsesAliasAndLeavesDefaultOptional(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{"codex": {
		Command: []string{"codex", "exec", "--model=" + modelParameter, "-"},
		Models:  map[string]string{"luna": "gpt-5.6-luna"},
	}}}
	agent := ResolvedAgent{Executor: "codex"}

	resolved, err := worker.ResolveAgentModel(agent, "luna")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "gpt-5.6-luna" || strings.Join(resolved.Command, " ") != "codex exec --model=gpt-5.6-luna -" {
		t.Fatalf("resolved = %#v", resolved)
	}
	defaulted, err := worker.ResolveAgent(agent)
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.Model != "" || strings.Join(defaulted.Command, " ") != "codex exec -" {
		t.Fatalf("defaulted = %#v", defaulted)
	}
}

func TestResolveAgentModelRejectsUnsupportedSelection(t *testing.T) {
	for name, executor := range map[string]Executor{
		"missing placeholder": {Command: []string{"agent", "run"}},
		"unknown alias":       {Command: []string{"agent", "--model=" + modelParameter}, Models: map[string]string{"fast": "fast-v1"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (Worker{Executors: map[string]Executor{"test": executor}}).ResolveAgentModel(ResolvedAgent{Executor: "test"}, "other")
			if err == nil {
				t.Fatal("expected model selection error")
			}
		})
	}
}

func TestWorkerModelCapabilitiesAndConfiguration(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors: map[string]Executor{
			"aliased": {Command: []string{"agent", "--model=" + modelParameter}, Models: map[string]string{"slow": "v2", "fast": "v1"}},
			"raw":     {Command: []string{"agent", "--model=" + modelParameter}},
			"fixed":   {Command: []string{"agent"}},
		},
	}, func() (string, error) { return "unused", nil })
	if err != nil {
		t.Fatal(err)
	}
	capabilities := worker.ModelCapabilities()
	if strings.Join(capabilities["aliased"], ",") != "fast,slow" || capabilities["raw"] == nil {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if _, ok := capabilities["fixed"]; ok {
		t.Fatalf("fixed executor advertised model support: %#v", capabilities)
	}

	_, err = applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent"}, Models: map[string]string{"fast": "v1"}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), modelParameter) {
		t.Fatalf("invalid model config error = %v", err)
	}
}

func TestValidateModelSelectionRejectsMixedExecutorPipeline(t *testing.T) {
	agents := []ResolvedAgent{{Name: "plan", Executor: "codex"}, {Name: "build", Executor: "claude"}}
	if err := ValidateModelSelection(agents, "fast"); err == nil {
		t.Fatal("expected mixed-executor model selection error")
	}
	if err := ValidateModelSelection(agents, ""); err != nil {
		t.Fatalf("model-less pipeline: %v", err)
	}
}

func TestLoadWorkerRejectsCompoundModelPlaceholderArgument(t *testing.T) {
	_, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent", "prefix-" + modelParameter}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), "complete optional") {
		t.Fatalf("compound placeholder error = %v", err)
	}
}

func TestLoadWorkerRejectsLegacyFactoryModelParameter(t *testing.T) {
	_, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent", "--model={{factory.model}}"}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), "legacy Factory parameter namespace") {
		t.Fatalf("legacy model parameter error = %v", err)
	}
}

func TestRenderPromptReplacesEveryPromptParameterWithoutReevaluation(t *testing.T) {
	agent := ResolvedAgent{Prompt: "Before {{machinist.prompt}} between {{machinist.prompt}} after"}
	prompt := "fix {{machinist.prompt}} and $(touch never)"
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

func TestLoadAgentRequiresPromptParameterAndRejectsUnsupportedMachinistParameter(t *testing.T) {
	for _, test := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "missing prompt", prompt: "Plan this ticket.\n", want: "must include {{machinist.prompt}}"},
		{name: "legacy Factory namespace", prompt: "Plan {{machinist.prompt}} with {{factory.prompt}}.\n", want: "legacy Factory parameter namespace"},
		{name: "legacy task parameter", prompt: "Plan {{machinist.task}}.\n", want: "unsupported Machinist parameter"},
		{name: "unsupported parameter", prompt: "Plan {{machinist.prompt}} in {{machinist.repository}}.\n", want: "unsupported Machinist parameter"},
		{name: "empty parameter", prompt: "Plan {{machinist.prompt}} with {{machinist.}}.\n", want: "unsupported Machinist parameter"},
		{name: "unclosed parameter", prompt: "Plan {{machinist.prompt}} with {{machinist.repository.\n", want: "malformed Machinist parameter"},
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
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{machinist.prompt}}.\n")
	writeTestFile(t, filepath.Join(directory, "build.md"), "Build {{machinist.prompt}}.\n")
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
	definitions, err := LoadDefinitions(definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Agents) != 2 {
		t.Fatalf("example agents = %#v, want only foreman and audit", definitions.Agents)
	}
	if _, ok := definitions.Agents["foreman"]; !ok {
		t.Fatal("example foreman agent is missing")
	}
	if _, ok := definitions.Agents["audit"]; !ok {
		t.Fatal("example audit agent is missing")
	}
	if len(definitions.Pipelines) != 0 {
		t.Fatalf("example pipelines = %#v, want none", definitions.Pipelines)
	}

	for _, name := range []string{"foreman", "audit"} {
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
	if _, err := LoadPipeline(definition, "code"); err == nil || !strings.Contains(err.Error(), `pipeline "code" is not defined`) {
		t.Fatalf("default code pipeline unexpectedly loads: %v", err)
	}
	foreman, err := LoadAgent(definition, "foreman")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"Never plan the",
		"Perform this discovery at the start of every run",
		"CI failure:",
		"Review feedback:",
		"Open pull request:",
		"Existing implementation:",
		"Completed planning:",
		"New issue:",
		"A resumed run still has at most two total repair attempts",
		"Existing work must reuse its branch, absolute worktree, and pull request",
		"create a second pull request for the issue",
		"Treat a verified `machinist:ready-for-review` label",
		"Never let stale remote CI or review state",
		"recreate the recorded isolated worktree",
		"do not select one",
		"Every subagent prompt must require its final response to be a concise Markdown handoff",
		"## Planning handoff",
		"## Build handoff",
		"## Review handoff",
		"## Repair handoff",
		"Never print a complete diff",
		"inspect the branch, HEAD,",
		"never replace it on the old immutable head",
		"issue URL, acceptance criteria, worktree, branch, base",
		"Never inline the",
		"non-draft pull request linked to the issue",
		"Use this one loop for defects from local review, CI",
		"After every code change",
		"An approval applies only to the reviewed SHA",
		"push the immutable",
		"and persist the branch, head, check evidence",
		"only workflows applicable to this pull request's event",
		"exactly match the applicable expected inventory",
		"an expected item with no result remains pending",
		"Poll no more often than",
		"at most 20 minutes",
		"set `machinist:blocked`",
		"Resolve only threads whose",
		"Compare each finding with the current remote head and diff",
		"one `<!-- machinist:foreman-pr -->` marker",
		"persist the new head, locally approved SHA",
		"Never merge",
		"Keep the open-pull-request",
	} {
		if !strings.Contains(foreman.Prompt, rule) {
			t.Fatalf("foreman prompt does not contain %q", rule)
		}
	}
	for _, forbidden := range []string{
		"open one draft pull request",
		"mark the pull request ready for human review",
		"branch, complete diff",
		"SUBAGENT role=<role>",
	} {
		if strings.Contains(foreman.Prompt, forbidden) {
			t.Fatalf("foreman prompt still contains %q", forbidden)
		}
	}
	for _, heading := range []string{
		"# Ordered state entry\n",
		"## Local review\n",
		"## Open pull request: automation gate\n",
		"# Shared repair loop\n",
	} {
		if count := strings.Count(foreman.Prompt, heading); count != 1 {
			t.Fatalf("foreman prompt contains %q %d times, want once", heading, count)
		}
	}
	if existing, open := strings.Index(foreman.Prompt, "- **Existing implementation:**"), strings.Index(foreman.Prompt, "- **Open pull request:**"); existing < 0 || open < 0 || existing > open {
		t.Fatalf("foreman prompt must classify unpublished implementation before open pull request: existing=%d open=%d", existing, open)
	}

	audit, err := LoadAgent(definition, "audit")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"fresh general-purpose subagents",
		"For every candidate",
		"separate fresh general-purpose",
		"Do not combine candidates in one verification task",
		"verifier does not confirm as a correctness bug",
		"current open GitHub issues",
		"no more than three issues",
		"affected files and",
		"observed behavior, expected",
		"Never edit, create, delete, move, or format",
		"Never create or switch branches, commit, push, or open a pull request",
		"Never fix a bug, create a",
	} {
		if !strings.Contains(audit.Prompt, rule) {
			t.Fatalf("audit prompt does not contain %q", rule)
		}
	}
}

func TestWorkflowExampleDefinitionsLoad(t *testing.T) {
	tests := []struct {
		name     string
		agents   []string
		pipeline string
		steps    []string
	}{
		{name: "issue-to-pr", agents: []string{"issue-to-pr"}},
		{name: "multi-review", agents: []string{"review-codex", "review-claude"}, pipeline: "multi-review", steps: []string{"review-codex", "review-claude"}},
		{name: "code-audit", agents: []string{"code-audit"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := filepath.Join("..", "..", "examples", "workflows", test.name, "config.toml")
			for _, name := range test.agents {
				agent, err := LoadAgent(definition, name)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(agent.Prompt, promptParameter) {
					t.Fatalf("agent %q prompt does not contain %s", name, promptParameter)
				}
				for _, section := range []string{"# Role", "# Input", "# Required result", "# Procedure", "# Boundaries"} {
					if !strings.Contains(agent.Prompt, section) {
						t.Fatalf("agent %q prompt does not contain %q", name, section)
					}
				}
			}
			if test.pipeline == "" {
				return
			}
			agents, err := LoadPipeline(definition, test.pipeline)
			if err != nil {
				t.Fatal(err)
			}
			if len(agents) != len(test.steps) {
				t.Fatalf("pipeline has %d agents, want %d", len(agents), len(test.steps))
			}
			for index, agent := range agents {
				if agent.Name != test.steps[index] {
					t.Fatalf("pipeline agent %d = %q, want %q", index+1, agent.Name, test.steps[index])
				}
			}
		})
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
