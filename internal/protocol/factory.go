package protocol

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

type FactoryContext struct {
	CommitOnly bool   `json:"commit_only,omitempty"`
	SpecPath   string `json:"spec_path,omitempty"`
	Recipe     string `json:"recipe,omitempty"`
	SpecURL    string `json:"spec_url,omitempty"`
	Context    string `json:"context,omitempty"`
	IssueURL   string `json:"issue_url,omitempty"`
	Role       string `json:"role"`
	PRURL      string `json:"pr_url,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

type Finding struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Problem    string `json:"problem"`
	Impact     string `json:"impact"`
	Correction string `json:"correction"`
}

// FactoryResult adds a versioned routing contract without changing legacy steps.
type FactoryResult struct {
	PRDescription string    `json:"pr_description,omitempty"`
	Labels        []string  `json:"labels,omitempty"`
	Resolved      bool      `json:"resolved,omitempty"`
	SpecURL       string    `json:"spec_url,omitempty"`
	Report        string    `json:"report,omitempty"`
	Version       int       `json:"version"`
	Role          string    `json:"role"`
	NeedsSpec     bool      `json:"needs_spec,omitempty"`
	Verdict       string    `json:"verdict,omitempty"`
	Findings      []Finding `json:"findings,omitempty"`
	PRURL         string    `json:"pr_url,omitempty"`
	HeadSHA       string    `json:"head_sha,omitempty"`
	Branch        string    `json:"branch,omitempty"`
}

var gitSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var pullPath = regexp.MustCompile(`^/[^/]+/[^/]+/pull/[1-9][0-9]*$`)

func (r FactoryResult) Validate() error {
	if r.Version != 1 {
		return errors.New("unsupported factory result version")
	}
	if r.PRDescription != "" && ((r.Role != "spec" && r.Role != "implement" && r.Role != "primary") || len(r.PRDescription) > 8192) {
		return errors.New("public PR descriptions are limited to 8 KiB and Spec or Implement")
	}
	switch r.Role {
	case "triage", "spec", "implement", "review", "primary":
	default:
		return errors.New("invalid factory role")
	}
	if r.Resolved && (r.Role != "triage" || r.NeedsSpec) {
		return errors.New("only triage can resolve a request without changes, without requesting a spec")
	}
	if r.NeedsSpec && r.Role != "triage" {
		return errors.New("only triage may request a spec")
	}
	if len(r.Labels) > 10 || (len(r.Labels) > 0 && r.Role != "triage") {
		return errors.New("only triage may suggest up to ten labels")
	}
	for _, label := range r.Labels {
		if strings.TrimSpace(label) != label || label == "" || len(label) > 200 || strings.ContainsAny(label, "\r\n") {
			return errors.New("invalid suggested label")
		}
	}
	if r.HeadSHA != "" && !gitSHA.MatchString(r.HeadSHA) {
		return errors.New("factory head must be a full Git SHA")
	}
	if r.SpecURL != "" {
		u, err := url.Parse(r.SpecURL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || !(strings.Contains(u.Path, "/blob/") || pullPath.MatchString(u.Path)) {
			return errors.New("spec must be a GitHub file or pull request URL")
		}
	}
	if r.PRURL != "" {
		u, err := url.Parse(r.PRURL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !pullPath.MatchString(u.Path) {
			return errors.New("factory PR must be a canonical GitHub pull request URL")
		}
	}
	if r.Role != "review" && (r.Verdict != "" || len(r.Findings) != 0) {
		return errors.New("only review may return a verdict or findings")
	}
	if r.Verdict != "" {
		switch r.Verdict {
		case "accepted":
			if len(r.Findings) != 0 {
				return errors.New("accepted review cannot have findings")
			}
		case "revision_needed", "human_decision_needed":
			if len(r.Findings) == 0 {
				return errors.New("review findings are required")
			}
		default:
			return errors.New("invalid factory review verdict")
		}
	}
	if len(r.Findings) > 50 {
		return errors.New("too many review findings")
	}
	for _, f := range r.Findings {
		if (f.Severity != "p0" && f.Severity != "p1" && f.Severity != "p2" && f.Severity != "p3") || f.Line < 1 || strings.TrimSpace(f.File) == "" || strings.TrimSpace(f.Problem) == "" || strings.TrimSpace(f.Impact) == "" || strings.TrimSpace(f.Correction) == "" {
			return errors.New("invalid factory finding")
		}
	}
	return nil
}
