package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkflowConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, steps string
		valid       bool
	}{
		{"one", `["build"]`, true},
		{"sequence", `["triage", {command="build", approval="before"}]`, true},
		{"empty", `[]`, false},
		{"unknown", `["missing"]`, false},
		{"bad approval", `[{command="build",approval="after"}]`, false},
		{"typo", `[{command="build",approvel="before"}]`, false},
		{"invalid type", `[42]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			text := "[commands.triage]\nexecutor='codex'\n[commands.build]\nexecutor='claude'\n[workflows.deliver]\nsteps=" + test.steps + "\n"
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadDefinitions(path)
			if !test.valid {
				if err == nil {
					t.Fatal("expected invalid workflow")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			steps, err := c.ResolveTaskWorkflow("deliver", "")
			if err != nil {
				t.Fatal(err)
			}
			if !steps[0].SharedOutputs {
				t.Fatalf("prompt %q", steps[0].Command.Prompt)
			}
			if test.name == "sequence" && !steps[1].Approval {
				t.Fatal("missing approval")
			}
		})
	}
}

func TestRiskDeliveryExampleUsesSharedFolder(t *testing.T) {
	cfg, err := LoadDefinitions("../../examples/workflows/risk_delivery/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := cfg.ResolveTaskWorkflow("classify_then_merge", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("steps: %d", len(steps))
	}
	for _, step := range steps {
		if !step.SharedOutputs || len(step.Inputs) != 0 {
			t.Fatalf("not shared: %+v", step)
		}
	}
}

func TestFactoryWorkflowRolesAndModels(t *testing.T) {
	cfg := Config{Commands: map[string]Command{}, Workflows: map[string]Workflow{}}
	roles := []string{"triage", "spec", "implement", "review"}
	var steps []any
	for _, role := range roles {
		name := "custom-" + role
		cfg.Commands[name] = Command{Executor: "codex", Model: "stage-model"}
		steps = append(steps, map[string]any{"id": role, "command": name})
	}
	cfg.Workflows["factory"] = Workflow{Mode: "factory", Steps: steps}
	for _, model := range []string{"", "override"} {
		resolved, err := cfg.ResolveTaskWorkflow("factory", model)
		if err != nil {
			t.Fatal(err)
		}
		for i, step := range resolved {
			want := model
			if want == "" {
				want = "stage-model"
			}
			if step.Role != roles[i] || step.Command.Model != want || step.Approval {
				t.Fatalf("step %d: %+v", i, step)
			}
		}
	}
	cfg.Workflows["sequence"] = Workflow{Steps: []any{"custom-triage"}}
	sequence, err := cfg.ResolveTaskWorkflow("sequence", "")
	if err != nil || sequence[0].Role != "" || sequence[0].Command.Model != "stage-model" {
		t.Fatalf("legacy sequence: %+v, %v", sequence, err)
	}
	for _, tc := range []struct {
		name     string
		workflow Workflow
	}{
		{"unknown mode", Workflow{Mode: "graph", Steps: steps}},
		{"missing stage", Workflow{Mode: "factory", Steps: steps[:3]}},
		{"wrong order", Workflow{Mode: "factory", Steps: []any{steps[1], steps[0], steps[2], steps[3]}}},
		{"implicit command role", Workflow{Mode: "factory", Steps: []any{"custom-triage", steps[1], steps[2], steps[3]}}},
		{"fixed approval", Workflow{Mode: "factory", Steps: []any{map[string]any{"id": "triage", "command": "custom-triage", "approval": "before"}, steps[1], steps[2], steps[3]}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Workflows["invalid"] = tc.workflow
			if _, err := cfg.ResolveTaskWorkflow("invalid", ""); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestFactoryLeavesHandoffStorageToGitHub(t *testing.T) {
	cfg := Config{Commands: map[string]Command{}, Workflows: map[string]Workflow{}}
	roles := []string{"triage", "spec", "implement", "review"}
	steps := []any{}
	for _, role := range roles {
		cfg.Commands[role] = Command{Executor: "codex"}
		step := map[string]any{"id": role, "command": role}
		if role == "spec" {
			step["required_outputs"] = []any{"spec.md", "decisions.md"}
		}
		steps = append(steps, step)
	}
	cfg.Workflows["factory"] = Workflow{Mode: "factory", Steps: steps}
	resolved, err := cfg.ResolveTaskWorkflow("factory", "")
	if err != nil {
		t.Fatal(err)
	}
	expected := [][]string{nil, {"spec.md", "decisions.md"}, nil, nil}
	for i, step := range resolved {
		if !reflect.DeepEqual(step.RequiredOutputs, expected[i]) {
			t.Fatalf("%s outputs = %v, want %v", step.Role, step.RequiredOutputs, expected[i])
		}
	}
}
