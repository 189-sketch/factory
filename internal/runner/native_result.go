package runner

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// Only the boundary translates to the historical storage envelope. The primary
// sees no stage routing fields, repair loops or synthetic review verdicts.
func parseNativeResult(message string) (*protocol.StepResult, error) {
	text := strings.TrimSpace(message)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	}
	var result struct {
		Outcome  string `json:"outcome"`
		Summary  string `json:"summary"`
		Delivery *struct {
			PRURL   string `json:"pr_url"`
			HeadSHA string `json:"head_sha"`
			Branch  string `json:"branch"`
		} `json:"delivery,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("primary result must contain one JSON object")
	}
	step := protocol.StepResult{Outcome: result.Outcome, Summary: result.Summary, Factory: &protocol.FactoryResult{Version: 1, Role: "primary", Report: result.Summary}}
	if result.Delivery != nil {
		step.Factory.PRURL = result.Delivery.PRURL
		step.Factory.HeadSHA = result.Delivery.HeadSHA
		step.Factory.Branch = result.Delivery.Branch
	}
	body, _ := json.Marshal(step)
	return protocol.ParseStepResult(body)
}
