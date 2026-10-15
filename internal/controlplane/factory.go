package controlplane

import (
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"strings"
)

type FactoryProgress struct {
	SpecURL     string             `json:"spec_url,omitempty"`
	RepairRound int                `json:"repair_round"`
	PRURL       string             `json:"pr_url,omitempty"`
	HeadSHA     string             `json:"head_sha,omitempty"`
	Branch      string             `json:"branch,omitempty"`
	Verdict     string             `json:"verdict,omitempty"`
	Findings    []protocol.Finding `json:"findings,omitempty"`
}

func formatFindings(findings []protocol.Finding) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "%s %s:%d: %s\nImpact: %s\nCorrection: %s\n", f.Severity, f.File, f.Line, f.Problem, f.Impact, f.Correction)
	}
	return b.String()
}
