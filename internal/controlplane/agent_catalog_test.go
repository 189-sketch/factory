package controlplane

import (
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredAgentsReadNativePromptsAndModels(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/agents/reviewer.md", "---\nname: reviewer\ndescription: >\n  Reviews changes\n  against requirements.\nmodel: 'sonnet'\ntools: [Read, Grep]\n---\n# Review\n\nCheck the tests.\n")
	write(".claude/agents/planner.md", "---\nname: planner\nmodel: inherit\n---\nPlan the work.")
	write(".claude/agents/broken.md", "---\nname: [\n---\nInvalid")
	write(".codex/agents/builder.toml", "name='builder'\nmodel='account-model'\ndeveloper_instructions='''Implement and verify.\nReturn evidence.'''\n")
	agents, err := readConfiguredAgents(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 3 {
		t.Fatalf("agents: %+v", agents)
	}
	var reviewer configuredAgent
	for _, a := range agents {
		if a.Name == "reviewer" {
			reviewer = a
		}
	}
	if reviewer.Model != "sonnet" || reviewer.Prompt != "# Review\n\nCheck the tests." || !strings.Contains(reviewer.Description, "Reviews changes against requirements.") {
		t.Fatalf("incorrect YAML parsing: %+v", reviewer)
	}
	if agents[0].Error == "" {
		t.Fatal("invalid YAML was silently accepted")
	}
	codex, err := readConfiguredAgents(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 1 || codex[0].Model != "account-model" || codex[0].Prompt != "Implement and verify.\nReturn evidence." {
		t.Fatalf("incorrect TOML: %+v", codex)
	}
}

func TestConfiguredAgentsDoNotReadOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("private credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".claude", "agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, ".claude", "agents", "reader.md")); err != nil {
		t.Fatal(err)
	}
	agents, err := readConfiguredAgents(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Error == "" || agents[0].Prompt != "" {
		t.Fatalf("external file exposed: %+v", agents)
	}
	// Directory links are confined by os.Root as well.
	root = t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfiguredAgents(root, "claude"); err == nil {
		t.Fatal("external agent directory accepted")
	}
}

func TestAgentCatalogMatchesSelectedHarnessAndRepository(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	o, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	s.setup = o
	root := t.TempDir()
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: root, Agent: "claude", Host: "local"}
	s.repositories = map[string]string{"owner/repo": "owner/repo"}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.definitionPath), "primary.md"), []byte("Coordinate {{machinist.prompt}}"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.definitionPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[commands.primary]\nexecutor='codex-native'\nprompt_file='primary.md'\nmodel='chosen-model'\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	raw := factoryHTTP(t, s, "GET", "/api/v1/agents", nil, 200)
	var result agentCatalogResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Agents) != 1 || result.Harness != "claude" || result.Agents[0].Model != "chosen-model" || result.Agents[0].Prompt != "Coordinate {{machinist.prompt}}" {
		t.Fatalf("catalog: %s", raw)
	}
	factoryHTTP(t, s, "GET", "/api/v1/agents?repository=unregistered", nil, 400)
	o.settings.Selection.Host = "remote"
	raw = factoryHTTP(t, s, "GET", "/api/v1/agents", nil, 200)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Notice == "" || len(result.Agents) != 1 {
		t.Fatalf("remote catalog claims local definitions: %s", raw)
	}
}

func TestConfiguredAgentsRejectNamedPipes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "stuck.md"), 0600); err != nil {
		t.Fatal(err)
	}
	agents, err := readConfiguredAgents(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Error == "" {
		t.Fatal("special file accepted")
	}
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, ".claude", "agents"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfiguredAgents(root, "claude"); err == nil {
		t.Fatal("agent directory pipe accepted")
	}
}
