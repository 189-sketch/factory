package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	defaultTimeout         = 30 * time.Minute
	maxConfigBytes         = 1 << 20
	maxPromptBytes         = 256 << 10
	maxInputPromptBytes    = 256 << 10
	maxRenderedPromptBytes = maxPromptBytes + maxInputPromptBytes
	maxTokenBytes          = 8 << 10
	defaultWorkerDirName   = ".factory/worker"
	defaultServerDirName   = ".factory/server"
	promptParameter        = "{{factory.prompt}}"
	factoryParameterPrefix = "{{factory"
)

type Worker struct {
	Name           string                `toml:"name"`
	DataDirectory  string                `toml:"data_directory"`
	DefinitionFile string                `toml:"definition_file"`
	ControlPlane   ControlPlane          `toml:"control_plane"`
	Executors      map[string]Executor   `toml:"executors"`
	Repositories   map[string]Repository `toml:"repositories"`
	configDir      string
}

type ControlPlane struct {
	URL       string `toml:"url"`
	TokenFile string `toml:"token_file"`
}

type Executor struct {
	Command []string `toml:"command"`
}

type Repository struct {
	Path string `toml:"path"`
}

type Server struct {
	Listen          string `toml:"listen"`
	Database        string `toml:"database"`
	DefinitionFile  string `toml:"definition_file"`
	WorkerTokenFile string `toml:"worker_token_file"`
	configDir       string
}

type Definition struct {
	Agents    map[string]Agent    `toml:"agents"`
	Pipelines map[string]Pipeline `toml:"pipelines"`
}

type Agent struct {
	Executor   string `toml:"executor"`
	PromptFile string `toml:"prompt_file"`
	Timeout    string `toml:"timeout"`
}

type Pipeline struct {
	Agents []string `toml:"agents"`
}

type ResolvedAgent struct {
	Name       string
	Executor   string
	Command    []string
	Prompt     string
	Timeout    time.Duration
	Definition string
	Hash       string
}

func LoadWorker(path string) (Worker, error) {
	worker := Worker{}
	if path == "" {
		defaultPath, err := defaultWorkerConfigPath()
		if err != nil {
			return Worker{}, err
		}
		path = defaultPath
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			worker.configDir = filepath.Dir(path)
			return applyWorkerDefaults(worker)
		} else if err != nil {
			return Worker{}, fmt.Errorf("inspect worker config: %w", err)
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Worker{}, fmt.Errorf("read worker config %q: %w", absPath, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&worker); err != nil {
		return Worker{}, fmt.Errorf("parse worker config %q: %w", absPath, err)
	}
	worker.configDir = filepath.Dir(absPath)
	return applyWorkerDefaults(worker)
}

func LoadServer(path string) (Server, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Server{}, fmt.Errorf("find user home directory: %w", err)
		}
		path = filepath.Join(home, ".factory", "server.toml")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Server{}, fmt.Errorf("resolve server config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Server{}, fmt.Errorf("read server config %q: %w", absPath, err)
	}
	var server Server
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&server); err != nil {
		return Server{}, fmt.Errorf("parse server config %q: %w", absPath, err)
	}
	server.configDir = filepath.Dir(absPath)
	return applyServerDefaults(server)
}

func (w Worker) ResolveDefinition(override string) (string, error) {
	path := override
	base := ""
	if path == "" {
		path = w.DefinitionFile
		base = w.configDir
	}
	if path == "" {
		path = "factory.toml"
	}
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve definition file: %w", err)
	}
	return filepath.Clean(absPath), nil
}

func (s Server) ResolveDefinition(override string) (string, error) {
	path := override
	if path == "" {
		path = s.DefinitionFile
	}
	if path == "" {
		path = "factory.toml"
	}
	return resolveConfigPath(path, s.configDir)
}

func (w Worker) ResolveAgent(agent ResolvedAgent) (ResolvedAgent, error) {
	executor, ok := w.Executors[agent.Executor]
	if !ok {
		return ResolvedAgent{}, fmt.Errorf("executor %q is not configured on this worker", agent.Executor)
	}
	if err := validateCommand(agent.Executor, executor.Command); err != nil {
		return ResolvedAgent{}, err
	}
	agent.Command = append([]string(nil), executor.Command...)
	return agent, nil
}

func (w Worker) ResolveRepository(name string) (string, error) {
	repository, ok := w.Repositories[name]
	if !ok {
		return "", fmt.Errorf("repository %q is not configured on this worker", name)
	}
	return resolveConfigPath(repository.Path, w.configDir)
}

func (w Worker) WorkerToken() (string, error) {
	if strings.TrimSpace(w.ControlPlane.TokenFile) == "" {
		return "", errors.New("control_plane.token_file is required")
	}
	path, err := resolveConfigPath(w.ControlPlane.TokenFile, w.configDir)
	if err != nil {
		return "", err
	}
	return readToken(path)
}

func (w Worker) ExecutorNames() []string { return sortedMapKeys(w.Executors) }

func (w Worker) RepositoryNames() []string { return sortedMapKeys(w.Repositories) }

func (s Server) WorkerToken() (string, error) {
	return readToken(s.WorkerTokenFile)
}

func LoadAgent(definitionPath, name string) (ResolvedAgent, error) {
	if strings.TrimSpace(name) == "" {
		return ResolvedAgent{}, errors.New("agent name is required")
	}
	definition, err := LoadDefinition(definitionPath)
	if err != nil {
		return ResolvedAgent{}, err
	}
	agent, ok := definition.Agents[name]
	if !ok {
		return ResolvedAgent{}, fmt.Errorf("agent %q is not defined in %s", name, definitionPath)
	}
	return resolveAgent(definitionPath, name, agent)
}

func LoadPipeline(definitionPath, name string) ([]ResolvedAgent, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("pipeline name is required")
	}
	definition, err := LoadDefinition(definitionPath)
	if err != nil {
		return nil, err
	}
	pipeline, ok := definition.Pipelines[name]
	if !ok {
		return nil, fmt.Errorf("pipeline %q is not defined in %s", name, definitionPath)
	}
	if len(pipeline.Agents) == 0 {
		return nil, fmt.Errorf("pipeline %q must define at least one agent", name)
	}
	agents := make([]ResolvedAgent, 0, len(pipeline.Agents))
	for index, agentName := range pipeline.Agents {
		if strings.TrimSpace(agentName) == "" {
			return nil, fmt.Errorf("pipeline %q agent %d is empty", name, index+1)
		}
		agent, ok := definition.Agents[agentName]
		if !ok {
			return nil, fmt.Errorf("pipeline %q references undefined agent %q", name, agentName)
		}
		resolved, err := resolveAgent(definitionPath, agentName, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, resolved)
	}
	return agents, nil
}

func LoadDefinition(definitionPath string) (Definition, error) {
	body, err := readBoundedFile(definitionPath, maxConfigBytes)
	if err != nil {
		return Definition{}, fmt.Errorf("read definition %q: %w", definitionPath, err)
	}
	var definition Definition
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return Definition{}, fmt.Errorf("parse definition %q: %w", definitionPath, err)
	}
	return definition, nil
}

func resolveAgent(definitionPath, name string, agent Agent) (ResolvedAgent, error) {
	if strings.TrimSpace(agent.Executor) == "" {
		return ResolvedAgent{}, fmt.Errorf("agent %q must define executor", name)
	}
	if strings.TrimSpace(agent.PromptFile) == "" {
		return ResolvedAgent{}, fmt.Errorf("agent %q must define prompt_file", name)
	}
	promptPath, err := expandHome(agent.PromptFile)
	if err != nil {
		return ResolvedAgent{}, fmt.Errorf("resolve agent %q prompt: %w", name, err)
	}
	if !filepath.IsAbs(promptPath) {
		promptPath = filepath.Join(filepath.Dir(definitionPath), promptPath)
	}
	prompt, err := readBoundedFile(filepath.Clean(promptPath), maxPromptBytes)
	if err != nil {
		return ResolvedAgent{}, fmt.Errorf("read agent %q prompt %q: %w", name, promptPath, err)
	}
	if strings.TrimSpace(string(prompt)) == "" {
		return ResolvedAgent{}, fmt.Errorf("agent %q prompt is empty", name)
	}
	if err := validatePromptParameters(name, string(prompt)); err != nil {
		return ResolvedAgent{}, err
	}
	timeout := defaultTimeout
	if agent.Timeout != "" {
		timeout, err = time.ParseDuration(agent.Timeout)
		if err != nil {
			return ResolvedAgent{}, fmt.Errorf("agent %q timeout: %w", name, err)
		}
		if timeout <= 0 {
			return ResolvedAgent{}, fmt.Errorf("agent %q timeout must be positive", name)
		}
	}

	resolved := ResolvedAgent{
		Name:       name,
		Executor:   agent.Executor,
		Prompt:     string(prompt),
		Timeout:    timeout,
		Definition: definitionPath,
	}
	resolved.Hash, err = agentHash(resolved)
	if err != nil {
		return ResolvedAgent{}, err
	}
	return resolved, nil
}

func RenderPrompt(agent ResolvedAgent, prompt string) (ResolvedAgent, error) {
	if strings.TrimSpace(prompt) == "" {
		return ResolvedAgent{}, errors.New("prompt is required")
	}
	if len(prompt) > maxInputPromptBytes {
		return ResolvedAgent{}, fmt.Errorf("prompt exceeds %d bytes", maxInputPromptBytes)
	}
	parameterCount := strings.Count(agent.Prompt, promptParameter)
	if parameterCount == 0 {
		return ResolvedAgent{}, fmt.Errorf("agent %q prompt must include %s", agent.Name, promptParameter)
	}
	literalBytes := len(agent.Prompt) - parameterCount*len(promptParameter)
	if literalBytes > maxRenderedPromptBytes || len(prompt) > (maxRenderedPromptBytes-literalBytes)/parameterCount {
		return ResolvedAgent{}, fmt.Errorf("rendered agent prompt exceeds %d bytes", maxRenderedPromptBytes)
	}
	agent.Prompt = strings.ReplaceAll(agent.Prompt, promptParameter, prompt)
	return agent, nil
}

func validatePromptParameters(agentName, prompt string) error {
	hasPrompt := false
	remaining := prompt
	for {
		start := strings.Index(remaining, factoryParameterPrefix)
		if start < 0 {
			break
		}
		remaining = remaining[start:]
		end := strings.Index(remaining, "}}")
		if end < 0 {
			return fmt.Errorf("agent %q prompt contains a malformed Factory parameter", agentName)
		}
		parameter := remaining[:end+2]
		if parameter != promptParameter {
			return fmt.Errorf("agent %q prompt uses unsupported Factory parameter %q", agentName, parameter)
		}
		hasPrompt = true
		remaining = remaining[end+2:]
	}
	if !hasPrompt {
		return fmt.Errorf("agent %q prompt must include %s", agentName, promptParameter)
	}
	return nil
}

func applyWorkerDefaults(worker Worker) (Worker, error) {
	return applyWorkerDefaultsWithHostname(worker, os.Hostname)
}

func applyWorkerDefaultsWithHostname(worker Worker, getHostname func() (string, error)) (Worker, error) {
	worker.Name = strings.TrimSpace(worker.Name)
	if worker.Name == "" {
		hostname, err := getHostname()
		if err != nil {
			return Worker{}, fmt.Errorf("find machine hostname: %w", err)
		}
		worker.Name = strings.TrimSpace(hostname)
		if worker.Name == "" {
			return Worker{}, errors.New("find machine hostname: hostname is empty")
		}
	}
	if worker.DataDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Worker{}, fmt.Errorf("find user home directory: %w", err)
		}
		worker.DataDirectory = filepath.Join(home, filepath.FromSlash(defaultWorkerDirName))
	}
	dataDirectory, err := expandHome(worker.DataDirectory)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker data directory: %w", err)
	}
	if !filepath.IsAbs(dataDirectory) && worker.configDir != "" {
		dataDirectory = filepath.Join(worker.configDir, dataDirectory)
	}
	worker.DataDirectory, err = filepath.Abs(dataDirectory)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker data directory: %w", err)
	}
	worker.DataDirectory = filepath.Clean(worker.DataDirectory)
	for name, repository := range worker.Repositories {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(repository.Path) == "" {
			return Worker{}, errors.New("repository names and paths must be non-empty")
		}
		path, err := resolveConfigPath(repository.Path, worker.configDir)
		if err != nil {
			return Worker{}, fmt.Errorf("resolve repository %q: %w", name, err)
		}
		repository.Path = path
		worker.Repositories[name] = repository
	}
	for name, executor := range worker.Executors {
		if err := validateCommand(name, executor.Command); err != nil {
			return Worker{}, err
		}
	}
	return worker, nil
}

func applyServerDefaults(server Server) (Server, error) {
	if server.Listen == "" {
		server.Listen = "127.0.0.1:7331"
	}
	if server.Database == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Server{}, fmt.Errorf("find user home directory: %w", err)
		}
		server.Database = filepath.Join(home, filepath.FromSlash(defaultServerDirName), "factory.db")
	} else {
		path, err := resolveConfigPath(server.Database, server.configDir)
		if err != nil {
			return Server{}, fmt.Errorf("resolve database: %w", err)
		}
		server.Database = path
	}
	if strings.TrimSpace(server.WorkerTokenFile) == "" {
		return Server{}, errors.New("worker_token_file is required")
	}
	tokenPath, err := resolveConfigPath(server.WorkerTokenFile, server.configDir)
	if err != nil {
		return Server{}, fmt.Errorf("resolve worker token file: %w", err)
	}
	server.WorkerTokenFile = tokenPath
	return server, nil
}

func validateCommand(name string, command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("executor %q must define a non-empty command", name)
	}
	for index, argument := range command {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("executor %q command argument %d contains a null byte", name, index)
		}
	}
	return nil
}

func resolveConfigPath(path, base string) (string, error) {
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

func readToken(path string) (string, error) {
	body, err := readBoundedFile(path, maxTokenBytes)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return token, nil
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func defaultWorkerConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".factory", "worker.toml"), nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/"))), nil
	}
	return path, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func agentHash(agent ResolvedAgent) (string, error) {
	payload, err := json.Marshal(struct {
		Name     string        `json:"name"`
		Executor string        `json:"executor"`
		Prompt   string        `json:"prompt"`
		Timeout  time.Duration `json:"timeout"`
	}{agent.Name, agent.Executor, agent.Prompt, agent.Timeout})
	if err != nil {
		return "", fmt.Errorf("encode agent definition: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
