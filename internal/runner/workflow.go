package runner

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"io"
	"os"
	"strings"
)

//go:embed prompts/sandbox.md
var sandboxPrompt string

//go:embed prompts/single.md
var singlePrompt string

//go:embed prompts/answer.md
var answerPrompt string

//go:embed prompts/triage.md
var triagePrompt string

//go:embed prompts/factory.md
var factoryContract string

//go:embed prompts/pull-request.md
var pullRequestPrompt string

func readStepResult(path string) (*protocol.StepResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow result: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workflow result must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow result: %w", err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return nil, err
	}
	result, err := protocol.ParseStepResult(body)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow result: %w", err)
	}
	return result, nil
}

func factoryPrompt(factory *protocol.FactoryContext, workspace string) string {
	if factory.Recipe == "native-v1" {
		return "\n\nRun this task as the primary agent. Use repository-defined native agents when useful. Children are activities within this run. Finish with one JSON object: {\"outcome\":\"complete|blocked|failed\",\"summary\":\"nonempty explanation\",\"delivery\":{\"pr_url\":\"https://github.com/owner/repo/pull/123\",\"head_sha\":\"full SHA\",\"branch\":\"branch\"}}. Omit delivery for an answer without file changes. Verify changes, push and open a PR when delivering code. Never merge. A child review is advisory; this mode has no enforced spec approval or isolated review checkout. Respect external publication holds.\nTask context:\n" + factory.Context
	}
	prompt := factoryStagePrompt(factory, workspace)
	if factory.Role == "implement" || factory.Role == "spec" {
		prompt += "\n\n" + pullRequestPrompt
	}
	return prompt
}

func factoryStagePrompt(factory *protocol.FactoryContext, workspace string) string {
	if factory.CommitOnly && (factory.Role == "implement" || factory.Role == "spec") {
		body, _ := json.Marshal(factory)
		return fmt.Sprintf(sandboxPrompt, factory.Role, body, workspace)
	}
	if factory.Recipe == "single" {
		body, _ := json.Marshal(factory)
		return fmt.Sprintf(singlePrompt, body, workspace)
	}
	if factory.Recipe == "answer" {
		return fmt.Sprintf(answerPrompt, factory.Context)
	}
	body, _ := json.Marshal(factory)
	if factory.Role == "triage" {
		return fmt.Sprintf(triagePrompt, body)
	}
	return fmt.Sprintf(factoryContract, body, workspace)
}

// Native CLIs sometimes wrap a valid final object in one Markdown code block.
// Normalize that presentation only; never search prose for an embedded object.
func parseFactoryMessage(message string) (*protocol.StepResult, error) {
	if len(message) > 16384 {
		return nil, fmt.Errorf("step result exceeds 16 KiB")
	}
	text := strings.TrimSpace(message)
	if strings.HasPrefix(text, "```json\n") {
		text = strings.TrimPrefix(text, "```json\n")
	} else if strings.HasPrefix(text, "```\n") {
		text = strings.TrimPrefix(text, "```\n")
	} else {
		return protocol.ParseStepResult([]byte(text))
	}
	if !strings.HasSuffix(text, "\n```") {
		return nil, fmt.Errorf("final JSON code block is not closed")
	}
	return protocol.ParseStepResult([]byte(strings.TrimSuffix(text, "\n```")))
}
