package controlplane

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

const maxAgentDefinitionBytes = 256 << 10

type configuredAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Harness     string `json:"harness"`
	Model       string `json:"model"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
	Source      string `json:"source"`
	Error       string `json:"error,omitempty"`
}

type agentCatalogResponse struct {
	Repository   string            `json:"repository"`
	Repositories []string          `json:"repositories"`
	Harness      string            `json:"harness"`
	Agents       []configuredAgent `json:"agents"`
	Notice       string            `json:"notice,omitempty"`
}

func (s *GitHubFactoryServer) agentCatalog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	repositories := s.repositoryNames()
	s.mu.Unlock()
	response := agentCatalogResponse{Repositories: repositories, Agents: []configuredAgent{}}
	if s.setup == nil {
		writeError(w, http.StatusConflict, errors.New("configure a repository and runner first"))
		return
	}
	settings := s.setup.snapshot()
	repo := r.URL.Query().Get("repository")
	if repo == "" {
		repo = settings.Selection.Repository
	}
	paths := settings.projectPaths()
	path, ok := paths[repo]
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("choose a configured repository"))
		return
	}
	response.Repository = repo
	command, err := s.jobCommand(r.Context(), nil, "primary")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response.Harness = settings.Selection.Agent
	source := definition.Commands["primary"].PromptFile
	response.Agents = append(response.Agents, configuredAgent{ID: "primary", Name: "Primary agent", Kind: "primary", Harness: response.Harness, Model: command.Model, Description: "Coordinates the task and delegates work to native subagents.", Prompt: command.Prompt, Source: source})
	w.Header().Set("Cache-Control", "no-store")
	if settings.Selection.Host != "local" {
		response.Notice = "The primary prompt is configured here. Repository agent definitions are on the execution host and are not available from this control plane."
		writeJSON(w, http.StatusOK, response)
		return
	}
	agents, err := readConfiguredAgents(path, response.Harness)
	if err != nil {
		response.Notice = "Could not read repository agent definitions: " + err.Error()
	} else {
		response.Agents = append(response.Agents, agents...)
	}
	writeJSON(w, http.StatusOK, response)
}

func readConfiguredAgents(repository, harness string) ([]configuredAgent, error) {
	agents := []configuredAgent{}
	directory, suffix := ".claude/agents", ".md"
	if harness == "codex" {
		directory, suffix = ".codex/agents", ".toml"
	} else if harness != "claude" {
		return agents, fmt.Errorf("no native definition reader for %q", harness)
	}
	root, err := os.OpenRoot(repository)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	folder, err := root.OpenFile(directory, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return agents, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := folder.ReadDir(-1)
	folder.Close()
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	total := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		if len(agents) >= 64 {
			return agents, errors.New("more than 64 agent definitions; narrow the configured agent directory")
		}
		relative := filepath.Join(directory, entry.Name())
		agent := configuredAgent{ID: relative, Name: strings.TrimSuffix(entry.Name(), suffix), Kind: "subagent", Harness: harness, Source: filepath.ToSlash(relative)}
		if !entry.Type().IsRegular() {
			agent.Error = "Agent definition must be a regular file"
			agents = append(agents, agent)
			continue
		}
		file, err := root.OpenFile(relative, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			agent.Error = "Could not open agent definition"
			agents = append(agents, agent)
			continue
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			agent.Error = "Agent definition must be a regular file"
			agents = append(agents, agent)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(file, maxAgentDefinitionBytes+1))
		file.Close()
		total += len(body)
		if total > 4<<20 {
			return agents, errors.New("agent definitions exceed the 4 MiB catalog limit")
		}
		if err != nil || len(body) > maxAgentDefinitionBytes {
			agent.Error = "Could not read agent definition within the 256 KiB limit"
			agents = append(agents, agent)
			continue
		}
		if harness == "claude" {
			err = parseClaudeDefinition(body, &agent)
		} else {
			err = parseCodexDefinition(body, &agent)
		}
		if err != nil {
			agent.Error = "Invalid agent definition: " + err.Error()
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

func parseClaudeDefinition(body []byte, agent *configuredAgent) error {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return errors.New("missing YAML front matter")
	}
	end := 1
	for end < len(lines) && strings.TrimSpace(lines[end]) != "---" {
		end++
	}
	if end == len(lines) {
		return errors.New("unclosed YAML front matter")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Model       string `yaml:"model"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return err
	}
	if metadata.Name != "" {
		agent.Name = metadata.Name
	}
	agent.Description = metadata.Description
	agent.Model = metadata.Model
	agent.Prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return nil
}

func parseCodexDefinition(body []byte, agent *configuredAgent) error {
	var definition struct {
		Name         string `toml:"name"`
		Description  string `toml:"description"`
		Model        string `toml:"model"`
		Instructions string `toml:"developer_instructions"`
	}
	if err := toml.Unmarshal(body, &definition); err != nil {
		return err
	}
	if definition.Name != "" {
		agent.Name = definition.Name
	}
	agent.Description = definition.Description
	agent.Model = definition.Model
	agent.Prompt = definition.Instructions
	if strings.TrimSpace(agent.Prompt) == "" {
		return errors.New("missing developer_instructions")
	}
	return nil
}
