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
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	defaultTimeout       = 30 * time.Minute
	maxConfigBytes       = 1 << 20
	maxPromptBytes       = 256 << 10
	defaultWorkerDirName = ".factory/worker"
)

type Worker struct {
	DataDirectory  string `toml:"data_directory"`
	DefinitionFile string `toml:"definition_file"`
	configDir      string
}

type Definition struct {
	Agents map[string]Agent `toml:"agents"`
}

type Agent struct {
	Command    []string `toml:"command"`
	PromptFile string   `toml:"prompt_file"`
	Timeout    string   `toml:"timeout"`
}

type ResolvedAgent struct {
	Name       string
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

func LoadAgent(definitionPath, name string) (ResolvedAgent, error) {
	if strings.TrimSpace(name) == "" {
		return ResolvedAgent{}, errors.New("agent name is required")
	}
	body, err := readBoundedFile(definitionPath, maxConfigBytes)
	if err != nil {
		return ResolvedAgent{}, fmt.Errorf("read definition %q: %w", definitionPath, err)
	}
	var definition Definition
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return ResolvedAgent{}, fmt.Errorf("parse definition %q: %w", definitionPath, err)
	}
	agent, ok := definition.Agents[name]
	if !ok {
		return ResolvedAgent{}, fmt.Errorf("agent %q is not defined in %s", name, definitionPath)
	}
	if len(agent.Command) == 0 || strings.TrimSpace(agent.Command[0]) == "" {
		return ResolvedAgent{}, fmt.Errorf("agent %q must define a non-empty command", name)
	}
	for index, argument := range agent.Command {
		if strings.ContainsRune(argument, '\x00') {
			return ResolvedAgent{}, fmt.Errorf("agent %q command argument %d contains a null byte", name, index)
		}
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
		Command:    append([]string(nil), agent.Command...),
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

func applyWorkerDefaults(worker Worker) (Worker, error) {
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
	return worker, nil
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
		Name    string        `json:"name"`
		Command []string      `json:"command"`
		Prompt  string        `json:"prompt"`
		Timeout time.Duration `json:"timeout"`
	}{agent.Name, agent.Command, agent.Prompt, agent.Timeout})
	if err != nil {
		return "", fmt.Errorf("encode agent definition: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
